package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// watch reads the events of one run until its last (design D21). It is
// the only reader of the run's events:
//   - started moves the ant from starting to running;
//   - session id is stored with the ant at once, so that a later run can
//     resume it (design D17), and with the run when it ends;
//   - activity goes to the RunActivity sink;
//   - usage is kept, and the last reported value goes to RunEnds when the
//     run ends;
//   - the agent's last message is kept for the summary of a run that
//     exits without finishing;
//   - exited and ended at the deadline end the run: see exited.
func (s *AntSupervisor) watch(ctx context.Context, r *activeRun, events <-chan ports.ExecEvent) {
	defer s.wg.Done()
	for ev := range events {
		switch ev.Kind {
		case ports.ExecStarted:
			s.report(s.started(ctx, r))
		case ports.ExecSessionID:
			s.report(s.sessionID(ctx, r, ev.SessionID))
		case ports.ExecActivity:
			if s.deps.Activity != nil {
				s.deps.Activity.Activity(r.ant, r.run.ID, ev.Line)
			}
		case ports.ExecUsage:
			r.usage = ev.Usage
		case ports.ExecLastMessage:
			r.lastMessage = ev.Message
		case ports.ExecExited, ports.ExecEndedAtDeadline:
			if ev.Usage != (ports.Usage{}) {
				r.usage = ev.Usage
			}
			s.report(s.exited(ctx, r, ev))
		}
	}
}

// ReasonRunStarted is the reason of the move from starting to running.
const ReasonRunStarted = "run started"

func (s *AntSupervisor) started(ctx context.Context, r *activeRun) error {
	defer s.lockAnt(r.ant)()
	rec, err := s.store.Ant(ctx, r.ant)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", r.ant, err)
	}
	if rec.State() != domain.AntStarting {
		return nil
	}
	_, err = s.transition(ctx, rec, domain.AntRunning, ReasonRunStarted)
	return err
}

func (s *AntSupervisor) sessionID(ctx context.Context, r *activeRun, id string) error {
	defer s.lockAnt(r.ant)()
	r.run.SessionID = id
	rec, err := s.store.Ant(ctx, r.ant)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", r.ant, err)
	}
	if rec.SessionID == id {
		return nil
	}
	rec.SessionID = id
	if err := s.store.SaveAnt(ctx, rec); err != nil {
		return fmt.Errorf("storing the session of ant %d: %w", r.ant, err)
	}
	return nil
}

// Endings of a run that the supervisor records when the service that
// ended it gave none.
const (
	ReasonExitedWithoutFinish = "exited without finish"
	ReasonBudgetUsedUp        = "budget used up"
)

// EndingRun records how the service that ends the ant's active run wants
// the run recorded, such as stopped by the queen or ended after a
// question, before it stops the run through the executor. The ant's
// state is the service's to set; the supervisor records the run with
// this outcome when its process has ended. It does nothing for an ant
// without an active run.
func (s *AntSupervisor) EndingRun(item domain.Ref, outcome ports.RunOutcome, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[item]; ok {
		r.outcome, r.reason = outcome, reason
	}
}

// exited ends a run whose process has ended: it records the run with its
// outcome and usage, revokes its tool token, and frees its slot for the
// queued ants. When no service had ended the run, its ant still has an
// active run: the exit is an exit without finishing (spec ant-execution,
// "Exit without finishing"), or a time-out when the executor ended it at
// the deadline (spec ant-timebox).
func (s *AntSupervisor) exited(ctx context.Context, r *activeRun, ev ports.ExecEvent) error {
	err := s.endRun(ctx, r, ev)
	s.deps.Tokens.Revoke(r.token)
	s.unreserve(r.item)
	// A run that ended is a trigger (design D22): a finishing ant's finish
	// is carried out now that its process has ended.
	err = errors.Join(err, s.Trigger(ctx, r.ant))
	s.slotFreed(ctx)
	return err
}

func (s *AntSupervisor) endRun(ctx context.Context, r *activeRun, ev ports.ExecEvent) error {
	defer s.lockAnt(r.ant)()
	s.mu.Lock()
	outcome, reason := r.outcome, r.reason
	s.mu.Unlock()
	rec, err := s.store.Ant(ctx, r.ant)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", r.ant, err)
	}
	var (
		errs  []error
		ended domain.AntState
	)
	if state := rec.State(); state.HasActiveRun() && !finished(rec) {
		to := domain.AntFailed
		outcome, reason = ports.OutcomeFailed, ReasonExitedWithoutFinish
		if ev.Kind == ports.ExecEndedAtDeadline && domain.CanTransition(state, domain.AntTimedOut) {
			to, outcome, reason = domain.AntTimedOut, ports.OutcomeTimedOut, ReasonBudgetUsedUp
		}
		if rec, err = s.transition(ctx, rec, to, reason); err != nil {
			errs = append(errs, err)
		} else {
			ended = to
		}
	} else if outcome == "" {
		outcome, reason = outcomeOf(rec)
	}
	run := r.run
	run.EndedAt, run.Outcome, run.Reason = s.clock.Now(), outcome, reason
	if err := s.deps.Runs.EndRun(ctx, run, r.usage, r.resumed); err != nil {
		errs = append(errs, fmt.Errorf("recording the end of run %d: %w", run.ID, err))
	}
	if ended != "" {
		errs = append(errs, s.endedWithoutFinish(ctx, rec, r, ended, reason))
	}
	if ended == "" && rec.State() == domain.AntAwaitingInput {
		// The run ended after ask_question: the claim stays held without
		// renewal until the answer starts the next run (spec
		// work-claiming, "parked").
		if _, err := s.park(ctx, rec); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// finished reports whether the ant's active run called finish: the ant is
// finishing with its finish stored. Its run ends however its process
// ends, and the finish is carried out.
func finished(rec ports.AntRecord) bool {
	return rec.State() == domain.AntFinishing && rec.Finish != nil
}

// outcomeOf is the outcome of a run whose ant some service moved out of
// its active run without saying how to record the run.
func outcomeOf(rec ports.AntRecord) (ports.RunOutcome, string) {
	reason := ""
	if n := len(rec.History); n > 0 {
		reason = rec.History[n-1].Reason
	}
	switch rec.State() {
	case domain.AntFinishing:
		if rec.Finish != nil {
			return finishOutcome(rec.Finish.Status), finishReason(rec.Finish.Status)
		}
	case domain.AntFailed:
		return ports.OutcomeFailed, reason
	case domain.AntTimedOut:
		return ports.OutcomeTimedOut, reason
	}
	return ports.OutcomeStopped, reason
}
