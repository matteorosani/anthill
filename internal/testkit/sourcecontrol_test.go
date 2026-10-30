package testkit_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

var (
	scRepo   = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}
	alice    = domain.Trailers{Item: "gh-12", Agent: "claude-code", Instance: "alice@laptop"}
	aliceOwn = alice.Owner()
)

func TestSourceControlOpenClone(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	want := s.AddClone("/clone", scRepo, "main")
	if c, err := s.OpenClone(ctx, "/clone", scRepo); err != nil || c != want || c.Remote != testkit.FakeRemote {
		t.Errorf("OpenClone() = %+v, %v", c, err)
	}
	other := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "api"}
	if _, err := s.OpenClone(ctx, "/clone", other); !errors.Is(err, ports.ErrNoClone) {
		t.Errorf("OpenClone() of another repository = %v", err)
	}
	if _, err := s.OpenClone(ctx, "/none", scRepo); !errors.Is(err, ports.ErrNoClone) {
		t.Errorf("OpenClone() of a missing path = %v", err)
	}
}

func TestSourceControlBranchAndWorktree(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	c := s.AddClone("/clone", scRepo, "main")
	if def, _ := s.RemoteDefaultBranch(ctx, c); def != "main" {
		t.Errorf("RemoteDefaultBranch() = %s", def)
	}
	if err := s.CreateBranch(ctx, c, "ant/gh-12-x", "main"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("branch from an unfetched base: %v", err)
	}
	if err := s.Fetch(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, c, "ant/gh-12-x", "main"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, c, "ant/gh-12-x", "main"); err == nil {
		t.Error("created a branch twice")
	}
	if err := s.AddWorktree(ctx, c, "/wt/gh-12", "ant/gh-12-x", domain.Trailers{}); err == nil {
		t.Error("added a worktree without trailers")
	}
	if err := s.AddWorktree(ctx, c, "/wt/gh-12", "ant/gh-12-x", alice); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorktree(ctx, c, "/wt/gh-12", "ant/gh-12-x", alice); err == nil {
		t.Error("added a worktree twice")
	}
	if s.Trailers("/wt/gh-12") != alice {
		t.Errorf("Trailers() = %+v", s.Trailers("/wt/gh-12"))
	}
	c1 := s.Commit("/wt/gh-12", "fix login")
	c2 := s.Commit("/wt/gh-12", "add test")
	commits, err := s.NewCommits(ctx, c, "/wt/gh-12", "main")
	if err != nil || !slices.Equal(commits, []ports.Commit{c1, c2}) {
		t.Errorf("NewCommits() = %v, %v", commits, err)
	}
	if unpushed, err := s.Unpushed(ctx, c, "ant/gh-12-x", ""); err != nil || !slices.Equal(unpushed, []ports.Commit{c1, c2}) {
		t.Errorf("Unpushed() = %v, %v", unpushed, err)
	}
	if unpushed, err := s.Unpushed(ctx, c, "ant/gh-12-x", c1.Hash); err != nil || !slices.Equal(unpushed, []ports.Commit{c2}) {
		t.Errorf("Unpushed(c1) = %v, %v", unpushed, err)
	}
	info, err := s.Branch(ctx, c, "ant/gh-12-x", "main")
	if err != nil || info.Commit != c2.Hash || !info.Local || info.Remote || !slices.Equal(info.Owners, []domain.BranchOwner{aliceOwn}) {
		t.Errorf("Branch() = %+v, %v", info, err)
	}
	if _, err := s.Branch(ctx, c, "none", ""); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("missing branch: %v", err)
	}
	s.SetDirty("/wt/gh-12", true)
	if st, err := s.WorktreeStatus(ctx, c, "/wt/gh-12"); err != nil || st.Branch != "ant/gh-12-x" || !st.Dirty {
		t.Errorf("WorktreeStatus() = %+v, %v", st, err)
	}
	if err := s.RemoveWorktree(ctx, c, "/wt/gh-12", false); err == nil {
		t.Error("removed a dirty worktree without force")
	}
	if err := s.RemoveWorktree(ctx, c, "/wt/gh-12", true); err != nil || len(s.Worktrees("/clone")) != 0 {
		t.Errorf("RemoveWorktree() = %v, worktrees %v", err, s.Worktrees("/clone"))
	}
	if _, err := s.WorktreeStatus(ctx, c, "/wt/gh-12"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("status of a removed worktree: %v", err)
	}
	if err := s.DeleteBranch(ctx, c, "ant/gh-12-x", false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBranch(ctx, c, "ant/gh-12-x", false); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestSourceControlForeignBranch(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	c := s.AddClone("/clone", scRepo, "main")
	bob := domain.BranchOwner{Item: "gh-12", Instance: "bob@desktop"}
	tip := s.SetRemoteBranch("/clone", "ant/gh-12-x", bob, "bob's work")
	_ = s.Fetch(ctx, c)
	info, err := s.Branch(ctx, c, "ant/gh-12-x", "main")
	if err != nil || !info.Remote || info.Local || info.Commit != tip.Hash || !slices.Equal(info.Owners, []domain.BranchOwner{bob}) {
		t.Errorf("Branch() = %+v, %v", info, err)
	}
	// The base's own commits carry no owners.
	if info, _ := s.Branch(ctx, c, "main", "main"); len(info.Owners) != 0 {
		t.Errorf("owners of main = %v", info.Owners)
	}
}

func newPushed(t *testing.T) (*testkit.SourceControl, ports.Clone) {
	t.Helper()
	ctx := context.Background()
	s := testkit.NewSourceControl()
	c := s.AddClone("/clone", scRepo, "main")
	_ = s.Fetch(ctx, c)
	_ = s.CreateBranch(ctx, c, "ant/gh-12-x", "main")
	_ = s.AddWorktree(ctx, c, "/wt", "ant/gh-12-x", alice)
	s.Commit("/wt", "fix")
	if _, err := s.Push(ctx, c, "", "ant/gh-12-x", aliceOwn); err != nil {
		t.Fatal(err)
	}
	return s, c
}

func TestSourceControlPush(t *testing.T) {
	ctx := context.Background()
	s, c := newPushed(t)
	remote := s.RemoteCommits("/clone", "ant/gh-12-x")
	if remote[len(remote)-1].Subject != "fix" {
		t.Errorf("remote = %v", remote)
	}
	if unpushed, _ := s.Unpushed(ctx, c, "ant/gh-12-x", ""); len(unpushed) != 0 {
		t.Errorf("unpushed after a push = %v", unpushed)
	}

	// The remote moved: the push is rejected, never forced, and the
	// remote branch is fetched so that it can be merged.
	moved := s.SetRemoteBranch("/clone", "ant/gh-12-x", domain.BranchOwner{}, "someone else")
	s.Commit("/wt", "more")
	out, err := s.Push(ctx, c, "", "ant/gh-12-x", aliceOwn)
	if !errors.Is(err, ports.ErrConflict) || out == "" {
		t.Errorf("Push() = %q, %v", out, err)
	}
	if got := s.RemoteCommits("/clone", "ant/gh-12-x"); got[len(got)-1] != moved {
		t.Errorf("rejected push changed the remote: %v", got)
	}
	if info, _ := s.Branch(ctx, c, "ant/gh-12-x", ""); !info.Remote {
		t.Error("the remote branch was not fetched")
	}
}

func TestSourceControlPushAfterAmend(t *testing.T) {
	ctx := context.Background()
	s, c := newPushed(t)
	s.Amend("/wt", "fix, amended")
	if _, err := s.Push(ctx, c, "", "ant/gh-12-x", aliceOwn); !errors.Is(err, ports.ErrHistoryRewritten) {
		t.Errorf("Push() after amend = %v", err)
	}
	if got := s.RemoteCommits("/clone", "ant/gh-12-x"); got[len(got)-1].Subject != "fix" {
		t.Errorf("rejected push changed the remote: %v", got)
	}
}

func TestSourceControlCheckoutAndRemoteDelete(t *testing.T) {
	ctx := context.Background()
	s, c := newPushed(t)
	s.Checkout("/wt", "main")
	if st, _ := s.WorktreeStatus(ctx, c, "/wt"); st.Branch != "main" {
		t.Errorf("branch after checkout = %q", st.Branch)
	}
	s.Checkout("/wt", "")
	if st, _ := s.WorktreeStatus(ctx, c, "/wt"); st.Branch != "" {
		t.Errorf("branch after detach = %q", st.Branch)
	}
	s.DeleteRemoteBranch("/clone", "ant/gh-12-x")
	_ = s.Fetch(ctx, c)
	if info, _ := s.Branch(ctx, c, "ant/gh-12-x", ""); info.Remote {
		t.Error("a deleted remote branch is still fetched")
	}
	if got := s.RemoteBranches("/clone"); !slices.Equal(got, []string{"main"}) {
		t.Errorf("RemoteBranches() = %v", got)
	}
	if got := s.LocalBranches("/clone"); !slices.Equal(got, []string{"ant/gh-12-x", "main"}) {
		t.Errorf("LocalBranches() = %v", got)
	}
	if err := s.DeleteBranch(ctx, c, "ant/gh-12-x", true); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("deleting a remote branch that is gone: %v", err)
	}
}

func TestSourceControlFail(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	c := s.AddClone("/clone", scRepo, "main")
	boom := errors.New("network down")
	s.Fail(testkit.GitFetch, boom)
	if err := s.Fetch(ctx, c); !errors.Is(err, boom) {
		t.Errorf("Fetch() = %v", err)
	}
	s.Fail(testkit.GitFetch, nil)
	if err := s.Fetch(ctx, c); err != nil {
		t.Errorf("Fetch() after clearing = %v", err)
	}
	if err := s.Fetch(ctx, ports.Clone{Path: "/none"}); !errors.Is(err, ports.ErrNoClone) {
		t.Errorf("Fetch() of a missing clone = %v", err)
	}
}
