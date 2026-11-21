package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The reasons recorded when an ant's PR ends.
const (
	ReasonPRMerged = "PR merged"
	ReasonPRClosed = "PR closed"
)

// ReasonCleanupNeedsConfirmation is added to the reason of an ant made
// done whose worktree was kept because cleanup would delete unpushed
// commits or uncommitted changes (spec branching-policy, "Unpushed
// work"). The transition is visible in the queen console, where the
// queen cleans the worktree up after confirming.
const ReasonCleanupNeedsConfirmation = "worktree kept: cleaning it up needs the queen's confirmation"

// reasonPRClosed is the reason recorded for a PR that ended in state.
func reasonPRClosed(state domain.PRState) string {
	if state == domain.PRMerged {
		return ReasonPRMerged
	}
	return ReasonPRClosed
}

// PRClosed ends an ant's work because a developer merged or closed its
// PR (spec pr-shepherd, "PR closure is human"): any active run is
// ended, an open extension request is closed, the ant becomes done, its
// task claim, parked or not, is released, and its worktree and local
// branch are cleaned up (spec branching-policy, "Worktree cleanup"). A
// cleanup that would delete unsaved work keeps the worktree, and the
// ant's reason says so. pr is the PR as just read. A PR that has not
// ended, or a final ant, is left alone.
//
// An active run is ended before the cleanup, and PRClosed waits for its
// process to end, at most StopGrace and the executor's kill.
func (s *AntSupervisor) PRClosed(ctx context.Context, id ports.AntID, pr domain.PullRequest) error {
	if !pr.State.Ended() {
		return nil
	}
	reason := reasonPRClosed(pr.State)
	unlock := s.lockAnt(id)
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		unlock()
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	if rec.State().Final() {
		unlock()
		return nil
	}
	var exec ports.ExecID
	if rec.State().HasActiveRun() {
		exec = s.endingRun(rec.Item, ports.OutcomeStopped, reason)
	}
	closed := rec.Budget.Open
	rec.Budget.Open = nil
	unlock()

	var errs []error
	if exec != "" {
		if err := s.deps.Executor.Stop(exec, StopGrace); err != nil {
			errs = append(errs, fmt.Errorf("ending the run of ant %d: %w", id, err))
		}
	}
	cleanupErr := s.cleanUpClosed(ctx, rec, pr)
	if errors.Is(cleanupErr, ErrNeedsConfirmation) {
		reason += "; " + ReasonCleanupNeedsConfirmation
		cleanupErr = nil
	}
	errs = append(errs, cleanupErr)

	unlock = s.lockAnt(id)
	rec, err = s.store.Ant(ctx, id)
	if err == nil && !rec.State().Final() {
		rec.Budget.Open = nil
		_, err = s.transition(ctx, rec, domain.AntDone, reason)
	}
	unlock()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	errs = append(errs, s.closeExtension(ctx, id, closed, "closed: the ant's PR ended"))
	if err := s.deps.Claims.Release(ctx, rec.Item, domain.ClaimTask); err != nil {
		errs = append(errs, fmt.Errorf("releasing the claim on %s: %w", rec.Item, err))
	}
	return errors.Join(errs...)
}

// cleanUpClosed removes the worktree and local branch of an ant whose PR
// ended, and the remote branch of a merged PR the provider kept. It
// returns a *NeedsConfirmationError, and removes nothing, when that
// would delete unsaved work.
func (s *AntSupervisor) cleanUpClosed(ctx context.Context, rec ports.AntRecord, pr domain.PullRequest) error {
	if rec.Branch == "" {
		return nil
	}
	ws, err := s.deps.Route(ctx, rec.Item)
	if err != nil {
		return fmt.Errorf("finding the clone of %s: %w", rec.Item, err)
	}
	_, err = s.deps.Workspace.Cleanup(ctx, PRCleanup(ws.Clone, rec, pr))
	if err != nil && !errors.Is(err, ErrNeedsConfirmation) {
		return fmt.Errorf("cleaning up ant %d: %w", rec.ID, err)
	}
	return err
}

// closedByPR makes the ant done when its PR has ended, and reports
// whether it did. Withdraw calls it when the item was closed: a merged
// PR that closes its item makes the ant done, not abandoned.
func (s *AntSupervisor) closedByPR(ctx context.Context, id ports.AntID) (bool, error) {
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return false, fmt.Errorf("reading ant %d: %w", id, err)
	}
	if rec.PR == nil || rec.State().Final() {
		return false, nil
	}
	host, err := s.deps.CodeHost(ctx, rec.PR.Repo)
	if err != nil {
		return false, err
	}
	pr, _, err := host.PullRequest(ctx, *rec.PR)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", rec.PR, err)
	}
	if !pr.State.Ended() {
		return false, nil
	}
	return true, s.PRClosed(ctx, id, pr)
}

// PRCleanup is the cleanup of an ant's worktree and branches after its
// PR, as just read: the remote branch goes only when the PR was merged,
// and commits up to the PR's head count as pushed (spec branching-policy,
// "Worktree cleanup").
func PRCleanup(clone ports.Clone, rec ports.AntRecord, pr domain.PullRequest) CleanupRequest {
	return CleanupRequest{
		Clone:    clone,
		Branch:   rec.Branch,
		Worktree: rec.Worktree,
		PR:       pr.State,
		PRHead:   pr.HeadCommit,
	}
}
