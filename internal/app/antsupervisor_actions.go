package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The queen actions on an ant (glossary; spec ant-execution, "Stopping an
// ant" and "Abandoning an ant"; spec ant-timebox): stop, retry, continue
// and abandon, and the queen's decision on an extension request. The
// supervisor carries them out outside the playbook (design D22). A retry
// or a continue queues the ant and asks for its run like any trigger, so
// capacity and queued stay with startRun: the run starts at once when a
// slot is free, and when one frees otherwise.

// The reasons of the queen actions' transitions.
const (
	ReasonStopped   = "stopped by the queen"
	ReasonAbandoned = "abandoned by the queen"
	ReasonRetry     = "retry"
	ReasonContinue  = "continue"
)

// ExtensionDecider applies the queen's decision on an ant's open
// extension request (spec ant-timebox, "Late extension decisions"). The
// ant tools implement it: a request_extension call still waiting returns
// the decision, and an active run is told otherwise.
type ExtensionDecider interface {
	DecideExtension(ctx context.Context, ant ports.AntID, minutes int) (domain.ExtensionRequest, domain.ExtensionEffect, error)
}

var _ ExtensionDecider = (*AntTools)(nil)

// ActionRefusedError is a queen action the ant's state does not allow.
// Nothing was done.
type ActionRefusedError struct {
	Ant    ports.AntID
	Action string
	State  domain.AntState
}

func (e *ActionRefusedError) Error() string {
	return fmt.Sprintf("cannot %s ant %d: it is %s", e.Action, e.Ant, e.State)
}

// Stop ends the ant's active run within 10 seconds with its session
// saved, pushes nothing, keeps the worktree, parks the task claim and
// makes the ant stopped (spec ant-execution, "Stopping an ant"). reason
// is recorded with the transition, ReasonStopped when empty, such as
// for an /anthill stop command. A finishing ant cannot be stopped: its
// finish is being carried out.
func (s *AntSupervisor) Stop(ctx context.Context, id ports.AntID, reason string) error {
	if reason == "" {
		reason = ReasonStopped
	}
	unlock := s.lockAnt(id)
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		unlock()
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	switch rec.State() {
	case domain.AntStarting, domain.AntRunning, domain.AntAwaitingExtension:
	default:
		unlock()
		return &ActionRefusedError{Ant: id, Action: "stop", State: rec.State()}
	}
	exec := s.endingRun(rec.Item, ports.OutcomeStopped, reason)
	rec, err = s.transition(ctx, rec, domain.AntStopped, reason)
	if err != nil {
		unlock()
		return err
	}
	park := s.deps.Claims.Park(ctx, rec.Item)
	if park != nil {
		park = fmt.Errorf("parking the claim on %s: %w", rec.Item, park)
	}
	unlock()
	return errors.Join(park, s.stopExec(exec))
}

// Abandon ends any active run within 10 seconds, releases the task claim
// and makes the ant abandoned (spec ant-execution, "Abandoning an ant").
// The worktree is kept until the queen cleans it up. An open extension
// request is closed. reason is recorded with the transition,
// ReasonAbandoned when empty.
func (s *AntSupervisor) Abandon(ctx context.Context, id ports.AntID, reason string) error {
	if reason == "" {
		reason = ReasonAbandoned
	}
	unlock := s.lockAnt(id)
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		unlock()
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	if rec.State().Final() {
		unlock()
		return &ActionRefusedError{Ant: id, Action: "abandon", State: rec.State()}
	}
	var exec ports.ExecID
	if rec.State().HasActiveRun() {
		exec = s.endingRun(rec.Item, ports.OutcomeStopped, reason)
	}
	closed := rec.Budget.Open
	rec.Budget.Open = nil
	rec, err = s.transition(ctx, rec, domain.AntAbandoned, reason)
	if err != nil {
		unlock()
		return err
	}
	errs := []error{s.closeExtension(ctx, id, closed, "closed: the ant was abandoned")}
	unlock()
	errs = append(errs, s.stopExec(exec))
	if err := s.deps.Claims.Release(ctx, rec.Item, domain.ClaimTask); err != nil {
		errs = append(errs, fmt.Errorf("releasing the claim on %s: %w", rec.Item, err))
	}
	return errors.Join(errs...)
}

// Retry starts a new run for a failed or stopped ant that resumes its
// session in the same worktree, with the remaining budget and a note
// about how the previous run ended (spec ant-execution, "Stop and retry
// later", "Retry after crash"). The run is of the kind of the run it
// replaces. When capacity is full, the ant is queued and its run starts
// when a slot frees. Retrying an ant already queued for a retry does
// nothing.
func (s *AntSupervisor) Retry(ctx context.Context, id ports.AntID) error {
	defer s.lockAnt(id)()
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	switch rec.State() {
	case domain.AntFailed, domain.AntStopped:
	case domain.AntQueued:
		if from := queuedFrom(rec.History); from == domain.AntFailed || from == domain.AntStopped {
			return nil
		}
		fallthrough
	default:
		return &ActionRefusedError{Ant: id, Action: "retry", State: rec.State()}
	}
	if _, err := s.transition(ctx, rec, domain.AntQueued, ReasonRetry); err != nil {
		return err
	}
	return s.decide(ctx, id)
}

// Continue starts a new run for a timed-out ant with a new budget that
// resumes its session in the same worktree (spec ant-timebox, "Queen
// continues a timed-out ant"); its commits go to the existing draft PR.
// A budget of 0 or less is the ant's base budget. An open extension
// request is closed. When capacity is full, the ant is queued and its
// run starts when a slot frees.
func (s *AntSupervisor) Continue(ctx context.Context, id ports.AntID, budget time.Duration) error {
	defer s.lockAnt(id)()
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	if rec.State() != domain.AntTimedOut {
		return &ActionRefusedError{Ant: id, Action: "continue", State: rec.State()}
	}
	if budget <= 0 {
		budget = rec.Base
	}
	ant, err := restoreAnt(rec)
	if err != nil {
		return err
	}
	closed, err := ant.StartNewBudget(budget)
	if err != nil {
		return err
	}
	rec.Budget = ant.Budget
	if _, err := s.transition(ctx, rec, domain.AntQueued, ReasonContinue); err != nil {
		return err
	}
	if closed != nil {
		if err := s.closeExtension(ctx, id, closed, closed.Message); err != nil {
			return err
		}
	}
	return s.decide(ctx, id)
}

// DecideExtension applies the queen's decision on the ant's open
// extension request: minutes is the time granted, 0 to deny (spec
// ant-timebox, "Extension requests" and "Late extension decisions"). An
// active run gets the decision, and its deadline moves with a grant. On
// a timed-out ant, a grant acts as continue with the granted minutes as
// the new budget, and a denial changes nothing. In any other state the
// request is closed.
func (s *AntSupervisor) DecideExtension(ctx context.Context, id ports.AntID, minutes int) (domain.ExtensionRequest, error) {
	if s.deps.Extensions == nil {
		return domain.ExtensionRequest{}, errors.New("extension decisions are not available")
	}
	req, effect, err := s.deps.Extensions.DecideExtension(ctx, id, minutes)
	if err != nil {
		return req, err
	}
	switch effect {
	case domain.EffectBudgetGrown, domain.EffectWrapUp:
		return req, s.BudgetChanged(ctx, id)
	case domain.EffectContinue:
		return req, s.continueGranted(ctx, id)
	}
	return req, nil
}

// continueGranted starts the run of a timed-out ant whose open request
// was granted late: the ant already has the granted minutes as its new
// budget.
func (s *AntSupervisor) continueGranted(ctx context.Context, id ports.AntID) error {
	defer s.lockAnt(id)()
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	if rec.State() != domain.AntTimedOut {
		return nil
	}
	if _, err := s.transition(ctx, rec, domain.AntQueued, ReasonContinue); err != nil {
		return err
	}
	return s.decide(ctx, id)
}

// Due says which run is due for an ant queued by a retry or a continue
// (DueRuns): a run of the kind of the last one that resumes the session,
// or gets a summary brief when it cannot, with the stored changes, the
// budget the ant has, and a note about how the previous run ended.
func (s *AntSupervisor) Due(ctx context.Context, rec ports.AntRecord) (*domain.StartRun, error) {
	if rec.State() != domain.AntQueued || len(rec.History) < 2 {
		return nil, nil
	}
	from, prev := rec.History[len(rec.History)-1].From, rec.History[len(rec.History)-2]
	var note string
	switch from {
	case domain.AntFailed, domain.AntStopped:
		note = domain.RetryNote(from, prev.Reason)
	case domain.AntTimedOut:
		note = domain.ContinueNote(rec.Budget.Total())
	default:
		return nil, nil
	}
	ws, err := s.deps.Route(ctx, rec.Item)
	if err != nil {
		return nil, fmt.Errorf("finding the workspace of %s: %w", rec.Item, err)
	}
	snap, err := s.snapshot(ctx, rec, ws.Settings)
	if err != nil {
		return nil, err
	}
	d := &domain.StartRun{Kind: snap.LastRun, Brief: domain.BriefSummary, Changes: snap.Stored, Note: note}
	switch {
	case snap.Runs == 0:
		d.Brief = domain.BriefTask
	case snap.Resumable:
		d.Brief = domain.BriefNone
	}
	return d, nil
}

// endingRun records how the ant's active run is to be recorded, as
// EndingRun does, and returns its process, or "" when it has not started.
func (s *AntSupervisor) endingRun(item domain.Ref, outcome ports.RunOutcome, reason string) ports.ExecID {
	s.EndingRun(item, outcome, reason)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[item]; ok {
		return r.exec
	}
	return ""
}

// stopExec ends a run's process tree within StopGrace. The run's events
// then record its end and free its slot.
func (s *AntSupervisor) stopExec(exec ports.ExecID) error {
	if exec == "" {
		return nil
	}
	if err := s.deps.Executor.Stop(exec, StopGrace); err != nil && !errors.Is(err, ports.ErrUnknownExecution) {
		return fmt.Errorf("stopping run %s: %w", exec, err)
	}
	return nil
}

// closeExtension stores an open extension request as closed, for the
// queen console, with the message.
func (s *AntSupervisor) closeExtension(ctx context.Context, id ports.AntID, req *domain.ExtensionRequest, message string) error {
	if req == nil {
		return nil
	}
	exts, err := s.store.Extensions(ctx, id)
	if err != nil {
		return fmt.Errorf("reading the extension requests of ant %d: %w", id, err)
	}
	for i := len(exts) - 1; i >= 0; i-- {
		if exts[i].DecidedAt.IsZero() {
			exts[i].Request = *req
			exts[i].Request.Status, exts[i].Request.Granted, exts[i].Request.Message = domain.ExtensionClosed, 0, message
			exts[i].DecidedAt = s.clock.Now()
			if _, err := s.store.SaveExtension(ctx, exts[i]); err != nil {
				return fmt.Errorf("closing the extension request of ant %d: %w", id, err)
			}
			return nil
		}
	}
	return nil
}
