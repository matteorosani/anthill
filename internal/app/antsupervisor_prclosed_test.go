package app_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// stoppedPRs is a ClosedPRShepherd that records the PRs it was told to
// stop.
type stoppedPRs struct {
	mu      sync.Mutex
	active  []domain.Ref
	stopped []domain.Ref
}

func (s *stoppedPRs) ActivePRs() []domain.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.active)
}

func (s *stoppedPRs) StopPR(_ context.Context, pr domain.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, pr)
	return nil
}

func (s *stoppedPRs) Stopped() []domain.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.stopped)
}

// succeeded runs ant 12 to a ready PR, and returns the ant.
func (e *supEnv) succeeded() ports.AntID {
	e.t.Helper()
	id := e.mustAccept(12)
	e.commit(id, "Fix the login timeout")
	e.mustCallTool(ghRef(12), "push", `{}`)
	e.mustCallTool(ghRef(12), "open_pull_request", `{"title":"Fix the login timeout","body":"Raises the timeout.","draft":false}`)
	e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Done."))
	e.wantState(id, domain.AntSucceeded)
	return id
}

// closure returns PR closure over the env's supervisor and shepherd.
func (e *supEnv) closure(shepherd *stoppedPRs) *app.PRClosure {
	return app.NewPRClosure(app.PRClosureDeps{
		Store: e.store,
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) {
			return e.gh.Client("mrosani"), nil
		},
		Ants:     e.sup,
		Shepherd: shepherd,
	})
}

// check runs a PR closure check and lets what it starts run.
func (e *supEnv) check(c *app.PRClosure) {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Check(e.ctx) }()
	e.settle()
	if err := <-done; err != nil {
		e.t.Fatalf("Check() = %v", err)
	}
	e.settle()
}

// Task 16.7, scenario "PR merged by developer" (spec pr-shepherd, "PR
// closure is human"; spec branching-policy, "PR merged").
func TestPRMergedByDeveloper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.succeeded()
		rec := e.ant(id)
		e.wantParked(12)
		shepherd := &stoppedPRs{}
		c := e.closure(shepherd)

		// An open PR is left alone.
		e.check(c)
		e.wantState(id, domain.AntSucceeded)

		e.gh.EditPullRequest(*rec.PR, func(pr *domain.PullRequest) { pr.State = domain.PRMerged })
		e.check(c)

		rec = e.wantState(id, domain.AntDone)
		if r := lastReason(rec); r != app.ReasonPRMerged {
			t.Errorf("reason = %q", r)
		}
		if _, held := e.heldClaim(12); held {
			t.Error("the parked task claim was not released")
		}
		if item, _ := e.gh.Item(ghRef(12)); slices.Contains(item.Labels, domain.DefaultClaimLabel) {
			t.Errorf("labels = %v", item.Labels)
		}
		if got := e.git.Worktrees(supClone); slices.Contains(got, rec.Worktree) {
			t.Errorf("worktrees = %v", got)
		}
		if got := e.git.LocalBranches(supClone); slices.Contains(got, rec.Branch) {
			t.Errorf("local branches = %v", got)
		}
		// The provider kept the merged branch, so Anthill deletes it.
		if got := e.git.RemoteBranches(supClone); slices.Contains(got, rec.Branch) {
			t.Errorf("remote branches = %v", got)
		}
		if got := shepherd.Stopped(); !slices.Equal(got, []domain.Ref{*rec.PR}) {
			t.Errorf("shepherd stopped %v", got)
		}

		// A done ant is not checked again.
		e.check(c)
		if got := shepherd.Stopped(); len(got) != 1 {
			t.Errorf("shepherd stopped %v", got)
		}
		e.wantNoErrors()
	})
}

// Task 16.7: a PR closed while its ant's follow-up run is active ends the
// run, and the worktree the run left changes in is kept until the queen
// confirms (spec branching-policy, "Unpushed work").
func TestPRClosedDuringAFollowUpRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.succeeded()
		e.followUp(id, "Please also handle the empty password.")
		rec := e.ant(id)
		exec := e.execOf(ghRef(12))
		e.git.SetDirty(rec.Worktree, true)

		e.gh.EditPullRequest(*rec.PR, func(pr *domain.PullRequest) { pr.State = domain.PRClosed })
		e.check(e.closure(&stoppedPRs{}))

		rec = e.wantState(id, domain.AntDone)
		if r := lastReason(rec); !strings.HasPrefix(r, app.ReasonPRClosed) || !strings.Contains(r, app.ReasonCleanupNeedsConfirmation) {
			t.Errorf("reason = %q", r)
		}
		if !exec.Ended() {
			t.Error("the follow-up run was not ended")
		}
		e.wantActive()
		if _, held := e.heldClaim(12); held {
			t.Error("the task claim was not released")
		}
		if got := e.git.Worktrees(supClone); !slices.Contains(got, rec.Worktree) {
			t.Errorf("the worktree with uncommitted changes was removed: %v", got)
		}
		// A closed PR's branch stays on the remote.
		if got := e.git.RemoteBranches(supClone); !slices.Contains(got, rec.Branch) {
			t.Errorf("remote branches = %v", got)
		}
		e.wantNoErrors()
	})
}

// Task 16.7: the merge closes the item through "Closes #12"; the ant is
// done, not abandoned as for an item closed by hand (spec change-reaction,
// "Withdrawal of opt-in").
func TestItemClosedByTheMergeMakesTheAntDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.succeeded()
		rec := e.ant(id)
		e.gh.EditPullRequest(*rec.PR, func(pr *domain.PullRequest) { pr.State = domain.PRMerged })

		done := make(chan error, 1)
		go func() { done <- e.sup.Withdraw(e.ctx, id, domain.ReasonItemClosed) }()
		e.settle()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		rec = e.wantState(id, domain.AntDone)
		if r := lastReason(rec); r != app.ReasonPRMerged {
			t.Errorf("reason = %q", r)
		}

		// With the PR still open, closing the item withdraws the ant.
		e2 := newSupEnv(t, 2)
		id2 := e2.succeeded()
		go func() { done <- e2.sup.Withdraw(e2.ctx, id2, domain.ReasonItemClosed) }()
		e2.settle()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		e2.wantState(id2, domain.AntAbandoned)
		e.wantNoErrors()
	})
}

// Task 16.7: PR closure also checks the human PRs the shepherd works on,
// which have no ant.
func TestPRClosureChecksTheShepherdsPRs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		human := domain.Ref{Repo: shop, Kind: domain.KindPR, Number: 50}
		open := domain.Ref{Repo: shop, Kind: domain.KindPR, Number: 51}
		e.gh.PutPullRequest(domain.PullRequest{Ref: human, SourceBranch: "feature/login", Author: "ada", HeadCommit: "c1"})
		e.gh.PutPullRequest(domain.PullRequest{Ref: open, SourceBranch: "feature/logout", Author: "ada", HeadCommit: "c1"})
		e.gh.EditPullRequest(human, func(pr *domain.PullRequest) { pr.State = domain.PRClosed })
		shepherd := &stoppedPRs{active: []domain.Ref{human, open}}

		e.check(e.closure(shepherd))
		if got := shepherd.Stopped(); !slices.Equal(got, []domain.Ref{human}) {
			t.Errorf("shepherd stopped %v", got)
		}
		e.wantNoErrors()
	})
}
