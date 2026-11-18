package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ExitWrapUp is the wrap-up of an ant that ended without finishing (spec
// ant-execution, "Exit without finishing"): push the ant branch when it
// has new commits, turn an open ant PR into a draft, post the failure
// summary, and park the claim. The ant is already failed with reason when
// it is called, with its lock held.
//
// TODO(#90): replace with the helper of exit without finishing once it
// lands; it is a seam until then.
type ExitWrapUp interface {
	WrapUp(ctx context.Context, rec ports.AntRecord, reason string) error
}

// Retrier retries a failed ant the way the queen does: the same worktree,
// the session resumed with a note about the failure (spec ant-execution,
// "Retry after crash"). It is called without the ant's lock.
//
// TODO(#91): replace with the retry queen action once it lands.
type Retrier interface {
	Retry(ctx context.Context, ant ports.AntID) error
}

// RestartDeps are what reconciliation hands an interrupted ant to.
type RestartDeps struct {
	WrapUp ExitWrapUp
	// Retry resumes the ants of repositories that set auto-resume. Nil
	// handles them as exits without finishing too.
	Retry Retrier
}

// Reconcile handles, once at daemon start and before any run starts, the
// ants the previous daemon left (spec ant-execution, "Restart
// resilience"):
//   - an ant with an active run in the store has no process any more. Its
//     open runs end failed with reason "interrupted by restart" and
//     unknown usage, and the ant becomes failed with the same reason.
//     When its repository sets auto-resume, it is retried, which resumes
//     its session; otherwise it gets the wrap-up of an exit without
//     finishing. Its claim is kept either way.
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
		resume, err := s.interrupted(ctx, rec.ID, deps)
		if err != nil {
			errs = append(errs, fmt.Errorf("reconciling ant %d of %s: %w", rec.ID, rec.Item, err))
			continue
		}
		if resume {
			if err := deps.Retry.Retry(ctx, rec.ID); err != nil {
				errs = append(errs, fmt.Errorf("resuming ant %d of %s: %w", rec.ID, rec.Item, err))
			}
		}
	}
	s.slotFreed(ctx)
	return errors.Join(errs...)
}

// interrupted ends the runs of an ant whose run the restart interrupted
// and makes it failed. It reports whether the ant is to be resumed;
// otherwise it has had the wrap-up.
func (s *AntSupervisor) interrupted(ctx context.Context, id ports.AntID, deps RestartDeps) (resume bool, err error) {
	defer s.lockAnt(id)()
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reading the ant: %w", err)
	}
	if !rec.State().HasActiveRun() || s.hasRun(rec.Item) {
		return false, nil
	}
	ws, err := s.deps.Route(ctx, rec.Item)
	if err != nil {
		return false, fmt.Errorf("finding the workspace: %w", err)
	}
	runs, err := s.store.Runs(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reading the runs: %w", err)
	}
	now := s.clock.Now()
	for _, run := range runs {
		if !run.EndedAt.IsZero() {
			continue
		}
		run.EndedAt, run.Outcome, run.Reason = now, ports.OutcomeFailed, ReasonInterruptedByRestart
		// The usage the runner reported before the restart is lost
		// with the process: it is unknown. A run that resumed a session
		// started with that session id.
		if err := s.deps.Runs.EndRun(ctx, run, ports.Usage{}, run.SessionID); err != nil {
			return false, fmt.Errorf("recording the end of run %d: %w", run.ID, err)
		}
	}
	if rec, err = s.transition(ctx, rec, domain.AntFailed, ReasonInterruptedByRestart); err != nil {
		return false, err
	}
	if ws.Settings.AutoResume && deps.Retry != nil {
		return true, nil
	}
	if deps.WrapUp == nil {
		return false, nil
	}
	return false, deps.WrapUp.WrapUp(ctx, rec, ReasonInterruptedByRestart)
}

// hasRun reports whether the item has a run this supervisor started or
// is starting.
func (s *AntSupervisor) hasRun(item domain.Ref) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runs[item]
	return ok
}
