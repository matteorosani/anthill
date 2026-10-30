package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

var (
	wsRepo  = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}
	wsItem  = domain.Ref{Repo: wsRepo, Kind: domain.KindItem, Number: 12}
	wsTrail = domain.NewTrailers(wsItem, domain.RunnerClaudeCode, "mrosani@LAPTOP-42")
	wsRoot  = filepath.Join(string(filepath.Separator)+"home", "me", ".anthill", "worktrees")
)

const wsBranch = "ant/gh-12-fix-login-timeout-on-safari"

type wsFixture struct {
	git   *testkit.SourceControl
	clone ports.Clone
	ws    *app.Workspace
}

func newWorkspace(t *testing.T) wsFixture {
	t.Helper()
	git := testkit.NewSourceControl()
	clone := git.AddClone("/src/web", wsRepo, "main")
	return wsFixture{git: git, clone: clone, ws: app.NewWorkspace(git, wsRoot)}
}

func (f wsFixture) request(labels ...string) app.PrepareRequest {
	settings, _ := domain.Defaults().ForItem(labels)
	return app.PrepareRequest{
		Clone: f.clone, Repo: wsRepo, Item: wsItem, Title: "Fix login timeout on Safari",
		Settings: settings, Trailers: wsTrail,
	}
}

func (f wsFixture) prepare(t *testing.T, labels ...string) app.AntWorkspace {
	t.Helper()
	got, err := f.ws.Prepare(context.Background(), f.request(labels...))
	if err != nil {
		t.Fatalf("Prepare() = %v", err)
	}
	return got
}

func TestPrepareNewAnt(t *testing.T) {
	f := newWorkspace(t)
	got := f.prepare(t)
	want := app.AntWorkspace{
		Branch:   wsBranch,
		Base:     "main",
		Worktree: filepath.Join(wsRoot, "gh-acme-web", "ant", "gh-12-fix-login-timeout-on-safari"),
	}
	if got != want {
		t.Errorf("Prepare() = %+v, want %+v", got, want)
	}
	if f.git.Trailers(got.Worktree) != wsTrail {
		t.Errorf("worktree trailers = %+v", f.git.Trailers(got.Worktree))
	}
}

// Scenario "Ticket targets a release branch".
func TestPrepareFromReleaseBranch(t *testing.T) {
	f := newWorkspace(t)
	tip := f.git.SetRemoteBranch("/src/web", "release/2.4", domain.BranchOwner{}, "release fix")
	got := f.prepare(t, "base:release/2.4")
	if got.Base != "release/2.4" {
		t.Errorf("base = %q", got.Base)
	}
	info, _ := f.git.Branch(context.Background(), f.clone, got.Branch, "")
	if info.Commit != tip.Hash {
		t.Errorf("branch starts at %s, want the fetched release/2.4 tip %s", info.Commit, tip.Hash)
	}
}

// Scenario "Base branch missing".
func TestPrepareBaseBranchMissing(t *testing.T) {
	f := newWorkspace(t)
	_, err := f.ws.Prepare(context.Background(), f.request("base:release/2.4"))
	if !errors.Is(err, domain.ErrBaseBranchMissing) || err.Error() != "base branch release/2.4 not found" {
		t.Errorf("Prepare() = %v", err)
	}
	if got := f.git.LocalBranches("/src/web"); len(got) != 0 || len(f.git.Worktrees("/src/web")) != 0 {
		t.Errorf("created branches %v or worktrees %v", got, f.git.Worktrees("/src/web"))
	}
}

// Scenario "Name already taken remotely by someone else".
func TestPrepareNameTaken(t *testing.T) {
	f := newWorkspace(t)
	bob := domain.BranchOwner{Item: "gh-12", Instance: "bob@desktop"}
	tip := f.git.SetRemoteBranch("/src/web", wsBranch, bob, "bob's work")
	got := f.prepare(t)
	if got.Branch != wsBranch+"-2" {
		t.Errorf("branch = %q, want the -2 suffix", got.Branch)
	}
	if remote := f.git.RemoteCommits("/src/web", wsBranch); remote[len(remote)-1] != tip {
		t.Errorf("the existing branch changed: %v", remote)
	}
	if slices.Contains(f.git.LocalBranches("/src/web"), wsBranch) {
		t.Error("the foreign branch was checked out locally")
	}
}

// A pushed branch of this instance for this item is reused, for example
// after the local record was lost.
func TestPrepareReusesOwnPushedBranch(t *testing.T) {
	f := newWorkspace(t)
	f.git.SetRemoteBranch("/src/web", wsBranch, wsTrail.Owner(), "earlier work")
	got := f.prepare(t)
	if got.Branch != wsBranch {
		t.Errorf("branch = %q", got.Branch)
	}
	commits, _ := f.git.NewCommits(context.Background(), f.clone, got.Worktree, "main")
	if len(commits) != 1 || commits[0].Subject != "earlier work" {
		t.Errorf("worktree commits = %v", commits)
	}
}

// Scenario "Second run for the same item".
func TestPrepareSecondRun(t *testing.T) {
	f := newWorkspace(t)
	first := f.prepare(t)
	req := f.request()
	req.Branch, req.Worktree = first.Branch, first.Worktree
	second, err := f.ws.Prepare(context.Background(), req)
	if err != nil || second != first {
		t.Errorf("second Prepare() = %+v, %v; want %+v", second, err, first)
	}
	if got := f.git.LocalBranches("/src/web"); !slices.Equal(got, []string{wsBranch}) {
		t.Errorf("branches = %v", got)
	}
	if got := f.git.Worktrees("/src/web"); len(got) != 1 {
		t.Errorf("worktrees = %v", got)
	}
}

// Scenario "Local database lost": the worktree is recreated from the
// pushed branch.
func TestPrepareRecreatesWorktree(t *testing.T) {
	ctx := context.Background()
	f := newWorkspace(t)
	first := f.prepare(t)
	f.git.Commit(first.Worktree, "work")
	if _, err := f.git.Push(ctx, f.clone, "", first.Branch, wsTrail.Owner()); err != nil {
		t.Fatal(err)
	}
	_ = f.git.RemoveWorktree(ctx, f.clone, first.Worktree, false)
	_ = f.git.DeleteBranch(ctx, f.clone, first.Branch, false)

	req := f.request()
	req.Branch = first.Branch
	got, err := f.ws.Prepare(ctx, req)
	if err != nil || got != first {
		t.Fatalf("Prepare() = %+v, %v", got, err)
	}
	commits, _ := f.git.NewCommits(ctx, f.clone, got.Worktree, "main")
	if len(commits) != 1 || commits[0].Subject != "work" {
		t.Errorf("recreated worktree commits = %v", commits)
	}
}

func TestPrepareUndoesBranchWhenWorktreeFails(t *testing.T) {
	f := newWorkspace(t)
	boom := errors.New("disk full")
	f.git.Fail(testkit.GitAddWorktree, boom)
	if _, err := f.ws.Prepare(context.Background(), f.request()); !errors.Is(err, boom) {
		t.Errorf("Prepare() = %v", err)
	}
	if got := f.git.LocalBranches("/src/web"); len(got) != 0 {
		t.Errorf("branch left behind: %v", got)
	}
}

func TestPrepareRejectsBadTrailers(t *testing.T) {
	f := newWorkspace(t)
	req := f.request()
	req.Trailers.Instance = ""
	if _, err := f.ws.Prepare(context.Background(), req); err == nil {
		t.Error("Prepare() accepted an empty instance")
	}
}

func (f wsFixture) push(ant app.AntWorkspace, force bool) (string, error) {
	return f.ws.Push(context.Background(), app.PushRequest{
		Clone: f.clone, Worktree: ant.Worktree, Owner: wsTrail.Owner(),
		Policy: domain.PushRequest{Kind: domain.PushAnt, Allowed: ant.Branch, Base: ant.Base, Protected: []string{"release/*"}, Force: force},
	})
}

func TestPushAntBranch(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	c := f.git.Commit(ant.Worktree, "fix")
	if _, err := f.push(ant, false); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if remote := f.git.RemoteCommits("/src/web", ant.Branch); remote[len(remote)-1] != c {
		t.Errorf("remote = %v", remote)
	}
	if _, err := f.push(ant, true); err == nil || err.Error() != domain.MessageForcePush {
		t.Errorf("force-push: %v", err)
	}
}

// Scenario "Ant attempts to push to main".
func TestPushToMainRefused(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	f.git.Checkout(ant.Worktree, "main")
	f.git.Commit(ant.Worktree, "on main")
	before := f.git.RemoteCommits("/src/web", "main")
	_, err := f.push(ant, false)
	if !errors.Is(err, domain.ErrPushRefused) || err.Error() != "push to main is not allowed" {
		t.Errorf("Push() = %v", err)
	}
	if after := f.git.RemoteCommits("/src/web", "main"); !slices.Equal(before, after) {
		t.Errorf("main changed: %v", after)
	}
}

// Scenario "Amend after the first push".
func TestPushAfterAmend(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	f.git.Commit(ant.Worktree, "fix")
	if _, err := f.push(ant, false); err != nil {
		t.Fatal(err)
	}
	f.git.Amend(ant.Worktree, "fix, amended")
	_, err := f.push(ant, false)
	if !errors.Is(err, ports.ErrHistoryRewritten) || !strings.HasPrefix(err.Error(), domain.MessageHistoryRewritten) {
		t.Errorf("Push() = %v", err)
	}
}

// Rebase and amend are fine before the first push.
func TestPushAmendBeforeFirstPush(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	f.git.Commit(ant.Worktree, "fix")
	f.git.Amend(ant.Worktree, "fix, amended")
	if _, err := f.push(ant, false); err != nil {
		t.Errorf("Push() = %v", err)
	}
}

// Scenario "Push rejected because the remote moved" (ant-tools).
func TestPushRemoteMoved(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	f.git.Commit(ant.Worktree, "fix")
	_, _ = f.push(ant, false)
	f.git.SetRemoteBranch("/src/web", ant.Branch, domain.BranchOwner{}, "reviewer suggestion")
	// A fetch in between, for another ant, does not make it a rewrite.
	_ = f.git.Fetch(context.Background(), f.clone)
	f.git.Commit(ant.Worktree, "more")
	out, err := f.push(ant, false)
	if !errors.Is(err, ports.ErrConflict) || out == "" {
		t.Errorf("Push() = %q, %v", out, err)
	}
}

func TestPushAutoFix(t *testing.T) {
	ctx := context.Background()
	f := newWorkspace(t)
	f.git.SetRemoteBranch("/src/web", "feature/login", domain.BranchOwner{}, "human work")
	_ = f.git.Fetch(ctx, f.clone)
	_ = f.git.CreateBranch(ctx, f.clone, "feature/login", "feature/login")
	fix := domain.Trailers{Item: "gh-pr-34", Agent: "claude-code", Instance: "mrosani@LAPTOP-42"}
	_ = f.git.AddWorktree(ctx, f.clone, "/tmp/fix", "feature/login", fix)
	f.git.Commit("/tmp/fix", "fix finding")
	push := func(optedIn bool) error {
		_, err := f.ws.Push(ctx, app.PushRequest{Clone: f.clone, Worktree: "/tmp/fix", Owner: fix.Owner(), Policy: domain.PushRequest{
			Kind: domain.PushAutoFix, Allowed: "feature/login", OptedIn: optedIn, Base: "main",
		}})
		return err
	}
	if err := push(false); !errors.Is(err, domain.ErrPushRefused) {
		t.Errorf("push to a PR that did not opt in: %v", err)
	}
	if err := push(true); err != nil {
		t.Errorf("push to an opted-in PR: %v", err)
	}
}

func (f wsFixture) cleanup(ant app.AntWorkspace, pr domain.PRState, confirmed bool) (domain.CleanupPlan, error) {
	head := ""
	if remote := f.git.RemoteCommits("/src/web", ant.Branch); len(remote) > 0 {
		head = remote[len(remote)-1].Hash
	}
	return f.ws.Cleanup(context.Background(), app.CleanupRequest{
		Clone: f.clone, Branch: ant.Branch, Worktree: ant.Worktree, PR: pr, PRHead: head, Confirmed: confirmed,
	})
}

// Scenario "PR merged".
func TestCleanupPRMerged(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	f.git.Commit(ant.Worktree, "fix")
	_, _ = f.push(ant, false)
	plan, err := f.cleanup(ant, domain.PRMerged, false)
	if err != nil || !plan.RemoveWorktree || !plan.DeleteLocalBranch || !plan.DeleteRemoteBranch {
		t.Fatalf("Cleanup() = %+v, %v", plan, err)
	}
	if len(f.git.Worktrees("/src/web")) != 0 || len(f.git.LocalBranches("/src/web")) != 0 {
		t.Errorf("left worktrees %v, branches %v", f.git.Worktrees("/src/web"), f.git.LocalBranches("/src/web"))
	}
	if got := f.git.RemoteBranches("/src/web"); !slices.Equal(got, []string{"main"}) {
		t.Errorf("remote branches = %v", got)
	}
}

func TestCleanupPRMergedProviderDeletedBranch(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	c := f.git.Commit(ant.Worktree, "fix")
	_, _ = f.push(ant, false)
	f.git.DeleteRemoteBranch("/src/web", ant.Branch)
	// After a squash merge the commits are on no remote branch, but the
	// PR's head commit says they were pushed.
	plan, err := f.ws.Cleanup(context.Background(), app.CleanupRequest{
		Clone: f.clone, Branch: ant.Branch, Worktree: ant.Worktree, PR: domain.PRMerged, PRHead: c.Hash,
	})
	if err != nil || plan.DeleteRemoteBranch || !plan.RemoveWorktree {
		t.Errorf("Cleanup() = %+v, %v", plan, err)
	}
}

func TestCleanupPRClosedKeepsRemoteBranch(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	f.git.Commit(ant.Worktree, "fix")
	_, _ = f.push(ant, false)
	if _, err := f.cleanup(ant, domain.PRClosed, false); err != nil {
		t.Fatal(err)
	}
	if got := f.git.RemoteBranches("/src/web"); !slices.Contains(got, ant.Branch) {
		t.Errorf("remote branch deleted: %v", got)
	}
}

// Scenario "Unpushed work".
func TestCleanupUnpushedWork(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	c := f.git.Commit(ant.Worktree, "not pushed")
	_, err := f.ws.Cleanup(context.Background(), app.CleanupRequest{
		Clone: f.clone, Branch: ant.Branch, Worktree: ant.Worktree, Abandoned: true, Requested: true,
	})
	var confirm *app.NeedsConfirmationError
	if !errors.As(err, &confirm) || !errors.Is(err, app.ErrNeedsConfirmation) || !slices.Equal(confirm.Unpushed, []ports.Commit{c}) {
		t.Fatalf("Cleanup() = %v", err)
	}
	if err.Error() != "the branch has 1 unpushed commits: confirm to delete them" {
		t.Errorf("message = %q", err)
	}
	if len(f.git.Worktrees("/src/web")) != 1 || len(f.git.LocalBranches("/src/web")) != 1 {
		t.Error("removed work before confirmation")
	}
	plan, err := f.ws.Cleanup(context.Background(), app.CleanupRequest{
		Clone: f.clone, Branch: ant.Branch, Worktree: ant.Worktree, Abandoned: true, Requested: true, Confirmed: true,
	})
	if err != nil || !plan.RemoveWorktree || len(f.git.Worktrees("/src/web")) != 0 || len(f.git.LocalBranches("/src/web")) != 0 {
		t.Errorf("confirmed Cleanup() = %+v, %v", plan, err)
	}
}

func TestCleanupUncommittedChanges(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	f.git.SetDirty(ant.Worktree, true)
	_, err := f.cleanup(ant, domain.PRClosed, false)
	var confirm *app.NeedsConfirmationError
	if !errors.As(err, &confirm) || !confirm.Uncommitted || err.Error() != "the worktree has uncommitted changes: confirm to delete them" {
		t.Fatalf("Cleanup() = %v", err)
	}
	plan, err := f.cleanup(ant, domain.PRClosed, true)
	if err != nil || !plan.Force || len(f.git.Worktrees("/src/web")) != 0 {
		t.Errorf("confirmed Cleanup() = %+v, %v", plan, err)
	}
}

func TestCleanupNotDue(t *testing.T) {
	f := newWorkspace(t)
	ant := f.prepare(t)
	plan, err := f.cleanup(ant, domain.PROpen, true)
	if err != nil || plan != (domain.CleanupPlan{}) || len(f.git.Worktrees("/src/web")) != 1 {
		t.Errorf("Cleanup() of an open PR = %+v, %v", plan, err)
	}
}

func TestCleanupWorktreeAlreadyGone(t *testing.T) {
	ctx := context.Background()
	f := newWorkspace(t)
	ant := f.prepare(t)
	_ = f.git.RemoveWorktree(ctx, f.clone, ant.Worktree, false)
	plan, err := f.cleanup(ant, domain.PRClosed, false)
	if err != nil || plan.RemoveWorktree || !plan.DeleteLocalBranch || len(f.git.LocalBranches("/src/web")) != 0 {
		t.Errorf("Cleanup() = %+v, %v", plan, err)
	}
}
