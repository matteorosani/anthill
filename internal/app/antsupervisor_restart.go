package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// WrapUpRequest is what the wrap-up of an exit without finishing is asked
// to do, and WrapUpResult what it did. They have the shape of the
// wrap-up helper's WrapUp and WrappedUp.
//
// TODO(#90): replace with WrapUp and WrappedUp once the helper of exit
// without finishing lands.
type WrapUpRequest struct {
	Reason     string
	OpenDraft  bool
	Summary    string
	OpenPoints []string
}

// WrapUpResult is what the wrap-up did. See WrapUpRequest.
type WrapUpResult struct {
	Pushed bool
	PR     *domain.Ref
	Opened bool
}

// ExitWrapUp is the wrap-up of an ant that ended without finishing (spec
// ant-execution, "Exit without finishing"): push the ant branch when it
// has new commits, turn an open ant PR into a draft, post the summary,
// and park the claim. It never changes the ant's state: the ant is
// already failed when it is called. run is the interrupted run, or 0
// when it is not known. The caller notifies the queen.
//
// TODO(#90): replace with the supervisor's wrapUp helper once it lands.
type ExitWrapUp interface {
	WrapUp(ctx context.Context, rec ports.AntRecord, run ports.RunID, w WrapUpRequest) (ports.AntRecord, WrapUpResult, error)
}

// Retrier retries a failed ant the way the queen does: the same worktree,
// the session resumed with a note about the failure (spec ant-execution,
// "Retry after crash"). It is called without the ant's lock.
//
// TODO(#91): replace with the supervisor's Retry once it lands.
type Retrier interface {
	Retry(ctx context.Context, ant ports.AntID) error
}

// RestartDeps are what reconciliation hands an interrupted ant to.
type RestartDeps struct {
	WrapUp ExitWrapUp
	// Retry resumes the ants of repositories that set auto-resume. Nil
	// handles them as exits without finishing too.
	Retry Retrier
	// PendingFinish reports whether a finishing ant has its finish
	// stored, which the playbook then carries out. Nil means none has.
	//
	// TODO(#89): replace with the finish stored on the ant's record once
	// finish handling lands.
	PendingFinish func(ports.AntRecord) bool
}

// SummaryInterruptedByRestart is the summary of the wrap-up of an ant
// whose run a restart interrupted.
const SummaryInterruptedByRestart = "Anthill restarted while this ant's run was active, so the run ended without finishing. The queen can retry it, which resumes the session, or abandon it."

// Reconcile handles, once at daemon start and before any run starts, the
// ants the previous daemon left (spec ant-execution, "Restart
// resilience"):
//   - an ant that is starting, running or awaiting an extension has no
//     process any more. Its open runs end failed with reason "interrupted
//     by restart" and unknown usage, and the ant becomes failed with the
//     same reason. When its repository sets auto-resume, it is retried,
//     which resumes its session; otherwise it gets the wrap-up of an exit
//     without finishing, and the queen is told. Its claim is kept either
//     way.
//   - a finishing ant whose finish is stored is triggered, so that the
//     playbook carries out the finish. One without a stored finish is
//     interrupted as above.
//   - queued ants start, in queue order, while slots are free and their
//     run is still due.
//
// Ants already handled are left alone, so calling it again does nothing
// new, and an ant whose run this supervisor started is never touched, so
// no work is started twice. Errors of one ant do not stop the others.
func (s *AntSupervisor) Reconcile(ctx context.Context, deps RestartDeps) error {
	var active []domain.AntState
	for _, st := range domain.AntStates {
		if st.HasActiveRun() {
			active = append(active, st)
		}
	}
	ants, err := s.store.Ants(ctx, active...)
	if err != nil {
		return fmt.Errorf("listing the ants with an active run: %w", err)
	}
	var errs []error
	for _, rec := range ants {
		next, err := s.interrupted(ctx, rec.ID, deps)
		if err != nil {
			errs = append(errs, fmt.Errorf("reconciling ant %d of %s: %w", rec.ID, rec.Item, err))
			continue
		}
		switch next {
		case afterRestartResume:
			err = deps.Retry.Retry(ctx, rec.ID)
		case afterRestartTrigger:
			err = s.Trigger(ctx, rec.ID)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("continuing ant %d of %s after the restart: %w", rec.ID, rec.Item, err))
		}
	}
	s.slotFreed(ctx)
	return errors.Join(errs...)
}

// afterRestart is what Reconcile does with an ant once its lock is
// released.
type afterRestart int

const (
	afterRestartNothing afterRestart = iota
	afterRestartResume
	afterRestartTrigger
)

// interrupted ends the runs of an ant whose run the restart interrupted
// and makes it failed, then wraps it up unless it is to be resumed. A
// finishing ant with its finish stored is left to the playbook.
func (s *AntSupervisor) interrupted(ctx context.Context, id ports.AntID, deps RestartDeps) (afterRestart, error) {
	defer s.lockAnt(id)()
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return afterRestartNothing, fmt.Errorf("reading the ant: %w", err)
	}
	if !rec.State().HasActiveRun() || s.hasRun(rec.Item) {
		return afterRestartNothing, nil
	}
	if rec.State() == domain.AntFinishing && deps.PendingFinish != nil && deps.PendingFinish(rec) {
		return afterRestartTrigger, nil
	}
	ws, err := s.deps.Route(ctx, rec.Item)
	if err != nil {
		return afterRestartNothing, fmt.Errorf("finding the workspace: %w", err)
	}
	runs, err := s.store.Runs(ctx, id)
	if err != nil {
		return afterRestartNothing, fmt.Errorf("reading the runs: %w", err)
	}
	now := s.clock.Now()
	var last ports.RunID
	for _, run := range runs {
		if !run.EndedAt.IsZero() {
			continue
		}
		run.EndedAt, run.Outcome, run.Reason = now, ports.OutcomeFailed, ReasonInterruptedByRestart
		// The usage the runner reported before the restart is lost
		// with the process: it is unknown. A run that resumed a session
		// started with that session id.
		if err := s.deps.Runs.EndRun(ctx, run, ports.Usage{}, run.SessionID); err != nil {
			return afterRestartNothing, fmt.Errorf("recording the end of run %d: %w", run.ID, err)
		}
		last = run.ID
	}
	if rec, err = s.transition(ctx, rec, domain.AntFailed, ReasonInterruptedByRestart); err != nil {
		return afterRestartNothing, err
	}
	if ws.Settings.AutoResume && deps.Retry != nil {
		return afterRestartResume, nil
	}
	return afterRestartNothing, wrapUpInterrupted(ctx, deps.WrapUp, rec, last, s.deps.Notifier)
}

// wrapUpInterrupted gives an ant whose run a restart interrupted the
// wrap-up of an exit without finishing, and tells the queen.
func wrapUpInterrupted(ctx context.Context, w ExitWrapUp, rec ports.AntRecord, run ports.RunID, notifier ports.Notifier) error {
	var err error
	if w != nil {
		_, _, err = w.WrapUp(ctx, rec, run, WrapUpRequest{Reason: ReasonInterruptedByRestart, Summary: SummaryInterruptedByRestart})
	}
	if notifier != nil {
		item := rec.Item
		// A notification that cannot be shown is not a reason to fail.
		_ = notifier.Notify(ctx, ports.Notification{
			Kind: domain.NotifyFailed, Title: "Ant interrupted by restart",
			Body: item.String() + ": " + ReasonInterruptedByRestart, Ref: &item,
		})
	}
	if err != nil {
		return fmt.Errorf("wrapping up after the restart: %w", err)
	}
	return nil
}

// hasRun reports whether the item has a run this supervisor started or
// is starting.
func (s *AntSupervisor) hasRun(item domain.Ref) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runs[item]
	return ok
}
