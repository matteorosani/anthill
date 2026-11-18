package app_test

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

const (
	recWorktreeRoot = "/home/me/.anthill/worktrees"
	recBranch       = "ant/gh-12-fix-bug-12"
)

// recEnv is a supervisor environment whose local store starts empty, as
// after it was deleted, while the provider and the clone keep what an
// earlier store's ants left: claims, branches and PRs.
type recEnv struct {
	*supEnv
	recovery *app.SessionRecovery
}

func newRecEnv(t *testing.T) *recEnv {
	t.Helper()
	e := &recEnv{supEnv: newSupEnv(t, 2)}
	e.recovery = app.NewSessionRecovery(app.SessionRecoveryDeps{
		Store:     e.store,
		Claims:    e.claiming,
		Git:       e.git,
		Workspace: app.NewWorkspace(e.git, recWorktreeRoot),
		Sources: func(context.Context) ([]app.RecoverySource, error) {
			client := e.gh.Client("mrosani")
			return []app.RecoverySource{{
				Repo: shop, Clone: e.clone, Settings: e.settings,
				CodeHost: client, Tracker: client, TrackerRepo: shop,
			}}, nil
		},
		Clock:       e.clock,
		Interrupted: e.sup,
	})
	return e
}

// oldClaim makes instance claim item n through a claim service over its
// own store, the one that is later lost, and parks it when parked is set.
func (e *recEnv) oldClaim(n int, instance, login string, parked bool) {
	e.t.Helper()
	route := func(context.Context, domain.Ref) (app.ClaimTarget, error) {
		return app.ClaimTarget{Provider: e.gh.Client(login), Instance: instance, Label: domain.DefaultClaimLabel}, nil
	}
	old := app.NewClaiming(route, testkit.NewStateStore(), e.clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
	done := make(chan error, 1)
	go func() {
		_, err := old.Acquire(e.ctx, ghRef(n), domain.ClaimTask)
		if err == nil && parked {
			err = old.Park(e.ctx, ghRef(n))
		}
		done <- err
	}()
	e.settle()
	if err := <-done; err != nil {
		e.t.Fatalf("claiming gh-%d for %s: %v", n, instance, err)
	}
}

// pushedBranch puts an ant branch with two commits of owner on the
// remote.
func (e *recEnv) pushedBranch(name string, item int, instance string) {
	e.git.SetRemoteBranch(supClone, name, domain.BranchOwner{Item: ghRef(item).String(), Instance: instance}, "fix", "add test")
}

func (e *recEnv) openPR(n int, branch string, draft bool) {
	item := ghRef(12)
	e.gh.PutPullRequest(domain.PullRequest{
		Ref: ghPR(n), Title: "Fix bug 12", SourceBranch: branch, TargetBranch: "main",
		Author: "mrosani", Draft: draft, Item: &item,
	})
}

func (e *recEnv) recover() app.RecoveryReport {
	e.t.Helper()
	type result struct {
		report app.RecoveryReport
		err    error
	}
	done := make(chan result, 1)
	go func() {
		r, err := e.recovery.Recover(e.ctx)
		done <- result{r, err}
	}()
	e.settle()
	r := <-done
	if r.err != nil {
		e.t.Fatalf("Recover() = %v", r.err)
	}
	return r.report
}

func (e *recEnv) heldClaim(n int) (ports.HeldClaim, bool) {
	e.t.Helper()
	held, err := e.store.HeldClaims(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	i := slices.IndexFunc(held, func(h ports.HeldClaim) bool { return h.Ref == ghRef(n) && h.Kind == domain.ClaimTask })
	if i < 0 {
		return ports.HeldClaim{}, false
	}
	return held[i], true
}

// Task 14.8, scenario "Local database lost": the local store is deleted
// while this instance holds a parked claim on #12 with an open ant PR. At
// startup the record for gh-12 is rebuilt, the worktree is recreated from
// the pushed branch, and the next follow-up starts a new session with a
// summary brief.
func TestLocalDatabaseLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRecEnv(t)
		e.oldClaim(12, instanceA, "mrosani", true)
		e.pushedBranch(recBranch, 12, instanceA)
		e.openPR(34, recBranch, false)

		report := e.recover()
		if len(report.Rebuilt) != 1 {
			t.Fatalf("rebuilt %v, want the ant of gh-12", report.Rebuilt)
		}
		id := report.Rebuilt[0]
		rec := e.wantState(id, domain.AntSucceeded)
		wantTree := domain.WorktreePath(recWorktreeRoot, shop, recBranch)
		if rec.Item != ghRef(12) || rec.Branch != recBranch || rec.BaseBranch != "main" || rec.PR == nil || *rec.PR != ghPR(34) ||
			rec.SessionID != "" || rec.Worktree != wantTree || rec.Runner != string(e.settings.Runner) || rec.Base != e.settings.Budget {
			t.Errorf("rebuilt record %+v", rec)
		}
		if lastReason(rec) != app.ReasonRebuilt {
			t.Errorf("reason %q", lastReason(rec))
		}
		if !slices.Contains(e.git.Worktrees(supClone), wantTree) || !slices.Contains(e.git.LocalBranches(supClone), recBranch) {
			t.Errorf("worktrees %v, local branches %v", e.git.Worktrees(supClone), e.git.LocalBranches(supClone))
		}
		if h, ok := e.heldClaim(12); !ok || !h.Parked || h.Instance != instanceA {
			t.Errorf("held claim %+v, %v", h, ok)
		}
		if c := e.restartWrapUps(12); len(c) != 0 {
			t.Errorf("restart wrap-ups %+v", c)
		}
		marks := countOps(e.gh, testkit.OpMark, "mrosani")
		if runs := e.runs(id); len(runs) != 0 {
			t.Errorf("runs %+v", runs)
		}

		// A developer comments on the PR: the follow-up run starts a new
		// session with the summary brief, in the recreated worktree, and
		// the parked claim is renewed.
		change := domain.ChangeEvent{Kind: domain.ChangeCommentAdded, Ref: ghPR(34), Actor: "lbianchi", Body: "Please also handle the empty password."}
		if err := e.store.StoreChange(e.ctx, ports.StoredChange{Ant: id, Source: 1, Event: change, StoredAt: e.clock.Now()}); err != nil {
			t.Fatal(err)
		}
		e.trigger(id)
		e.wantState(id, domain.AntRunning)
		x := e.execOf(ghRef(12))
		if x.Spec.Session != "" || x.Spec.Run.Worktree != wantTree {
			t.Errorf("run spec %+v", x.Spec)
		}
		if b := e.briefs.last(); b.kind != "summary" || b.item != ghRef(12) || len(b.changes) != 1 {
			t.Errorf("brief %+v", b)
		}
		if runs := e.runs(id); len(runs) != 1 || runs[0].Kind != ports.RunFollowUp {
			t.Errorf("runs %+v", runs)
		}
		if h, _ := e.heldClaim(12); h.Parked {
			t.Error("the claim stayed parked during the follow-up run")
		}
		// The run went through the next-run path: the claim was renewed,
		// not acquired again.
		if n := countOps(e.gh, testkit.OpPost, "mrosani"); n != 1 {
			t.Errorf("%d claim comments posted, want only the original one", n)
		}
		if n := countOps(e.gh, testkit.OpMark, "mrosani"); n != marks {
			t.Errorf("the claim was marked %d more times", n-marks)
		}
	})
}

// Task 14.8: the worktree is kept when it is still there, and a second
// recovery finds the record and rebuilds nothing.
func TestRecoveryKeepsWorktreeAndIsIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRecEnv(t)
		e.oldClaim(12, instanceA, "mrosani", true)
		e.pushedBranch(recBranch, 12, instanceA)
		e.openPR(34, recBranch, false)
		first := e.recover()
		trees := e.git.Worktrees(supClone)
		if again := e.recover(); len(again.Rebuilt) != 0 {
			t.Errorf("second recovery rebuilt %v", again.Rebuilt)
		}
		if got := e.git.Worktrees(supClone); !slices.Equal(got, trees) {
			t.Errorf("worktrees %v, want %v", got, trees)
		}
		ants, err := e.store.Ants(e.ctx)
		if err != nil || len(ants) != 1 || ants[0].ID != first.Rebuilt[0] {
			t.Errorf("ants %+v, %v", ants, err)
		}
	})
}

// Task 14.8: only items whose task claim this instance holds are rebuilt.
// An ant branch and PR of a colleague, a claim of a colleague on an item
// with this instance's old branch, and a branch whose claim was released
// are all left alone.
func TestRecoveryOnlyRebuildsOwnClaims(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRecEnv(t)
		// gh-13: a colleague's claim, branch and PR.
		e.oldClaim(13, instanceB, "lbianchi", true)
		e.pushedBranch("ant/gh-13-fix-bug-13", 13, instanceB)
		// gh-14: this instance's old branch, but a colleague holds the
		// claim now.
		e.pushedBranch("ant/gh-14-fix-bug-14", 14, instanceA)
		e.oldClaim(14, instanceB, "lbianchi", false)
		// gh-15: an old branch without any claim.
		e.pushedBranch("ant/gh-15-fix-bug-15", 15, instanceA)

		if report := e.recover(); len(report.Rebuilt) != 0 {
			t.Errorf("rebuilt %v", report.Rebuilt)
		}
		if ants, _ := e.store.Ants(e.ctx); len(ants) != 0 {
			t.Errorf("ants %+v", ants)
		}
		if held, _ := e.store.HeldClaims(e.ctx); len(held) != 0 {
			t.Errorf("held claims %+v", held)
		}
	})
}

// Task 14.8: a parked claim without an open ant PR gives a failed ant,
// which the queen can retry or abandon; an item with the claim label but
// no branch is found through its claim and gets a record without a
// branch.
func TestRecoveryParkedClaimWithoutPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRecEnv(t)
		e.oldClaim(12, instanceA, "mrosani", true)
		e.pushedBranch(recBranch, 12, instanceA)
		e.oldClaim(13, instanceA, "mrosani", true)

		report := e.recover()
		if len(report.Rebuilt) != 2 {
			t.Fatalf("rebuilt %v", report.Rebuilt)
		}
		r12 := e.wantState(report.Rebuilt[0], domain.AntFailed)
		if r12.Item != ghRef(12) || r12.PR != nil || r12.Branch != recBranch || lastReason(r12) != app.ReasonRebuilt {
			t.Errorf("gh-12: %+v", r12)
		}
		r13 := e.wantState(report.Rebuilt[1], domain.AntFailed)
		if r13.Item != ghRef(13) || r13.Branch != "" || r13.Worktree != "" || r13.BaseBranch != "main" {
			t.Errorf("gh-13: %+v", r13)
		}
	})
}

// Task 14.8: a live claim means the store was lost while a run was
// active. The ant is rebuilt failed with "interrupted by restart" and gets
// the wrap-up of an exit without finishing, which parks the claim, and
// the queen is told.
func TestRecoveryLiveClaimInterrupted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRecEnv(t)
		e.oldClaim(12, instanceA, "mrosani", false)
		e.pushedBranch(recBranch, 12, instanceA)
		e.openPR(34, recBranch, true)

		report := e.recover()
		if len(report.Rebuilt) != 1 {
			t.Fatalf("rebuilt %v", report.Rebuilt)
		}
		rec := e.wantState(report.Rebuilt[0], domain.AntFailed)
		if lastReason(rec) != app.ReasonInterruptedByRestart || rec.PR == nil {
			t.Errorf("rebuilt %+v", rec)
		}
		if c := e.restartWrapUps(12); len(c) != 1 {
			t.Errorf("restart wrap-ups %+v", c)
		}
		if n := countKind(e.notifier, domain.NotifyFailed); n != 1 {
			t.Errorf("%d failure notifications", n)
		}
		if h, ok := e.heldClaim(12); !ok || !h.Parked {
			t.Errorf("held claim %+v, %v, want it adopted and parked", h, ok)
		}
	})
}

// Task 14.8: when an item has several ant branches, the one with this
// instance's commits is chosen, and a PR from another branch of the item
// is not its PR.
func TestRecoveryPicksTheOwnBranch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newRecEnv(t)
		e.oldClaim(12, instanceA, "mrosani", true)
		// A colleague's earlier branch and PR for #12, then this
		// instance's branch with a numeric suffix.
		e.pushedBranch(recBranch, 12, instanceB)
		e.openPR(33, recBranch, false)
		e.pushedBranch(recBranch+"-2", 12, instanceA)

		report := e.recover()
		if len(report.Rebuilt) != 1 {
			t.Fatalf("rebuilt %v", report.Rebuilt)
		}
		rec := e.wantState(report.Rebuilt[0], domain.AntFailed)
		if rec.Branch != recBranch+"-2" || rec.PR != nil {
			t.Errorf("rebuilt %+v", rec)
		}
	})
}

// countOps counts the provider requests of one kind by login.
func countOps(p *testkit.Provider, op testkit.Op, login string) int {
	n := 0
	for _, r := range p.Requests() {
		if r.Op == op && r.Login == login {
			n++
		}
	}
	return n
}
