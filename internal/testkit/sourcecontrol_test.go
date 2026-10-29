package testkit_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestSourceControlBranchAndWorktree(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	s.AddClone("/clone", "main")
	if def, _ := s.RemoteDefaultBranch(ctx, "/clone"); def != "main" {
		t.Errorf("RemoteDefaultBranch() = %s", def)
	}
	if err := s.CreateBranch(ctx, "/clone", "ant/gh-12-x", "origin/main"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("branch from an unfetched base: %v", err)
	}
	if err := s.Fetch(ctx, "/clone"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, "/clone", "ant/gh-12-x", "origin/main"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, "/clone", "ant/gh-12-x", "origin/main"); err == nil {
		t.Error("created a branch twice")
	}
	if err := s.AddWorktree(ctx, "/clone", "/wt/gh-12", "ant/gh-12-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorktree(ctx, "/clone", "/wt/gh-12", "ant/gh-12-x"); err == nil {
		t.Error("added a worktree twice")
	}
	c1 := s.Commit("/wt/gh-12", "fix login", "alice@laptop")
	c2 := s.Commit("/wt/gh-12", "add test", "alice@laptop")
	commits, err := s.NewCommits(ctx, "/wt/gh-12", "origin/main")
	if err != nil || !slices.Equal(commits, []ports.Commit{c1, c2}) {
		t.Errorf("NewCommits() = %v, %v", commits, err)
	}
	if commits, _ := s.NewCommits(ctx, "/wt/gh-12", c1.Hash); !slices.Equal(commits, []ports.Commit{c2}) {
		t.Errorf("NewCommits(c1) = %v", commits)
	}
	info, err := s.Branch(ctx, "/clone", "ant/gh-12-x")
	if err != nil || info.Commit != c2.Hash || info.Instance != "alice@laptop" || info.Remote {
		t.Errorf("Branch() = %+v, %v", info, err)
	}
	if _, err := s.Branch(ctx, "/clone", "none"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("missing branch: %v", err)
	}
	if err := s.RemoveWorktree(ctx, "/clone", "/wt/gh-12"); err != nil || len(s.Worktrees("/clone")) != 0 {
		t.Errorf("RemoveWorktree() = %v, worktrees %v", err, s.Worktrees("/clone"))
	}
	if err := s.DeleteBranch(ctx, "/clone", "ant/gh-12-x", false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBranch(ctx, "/clone", "ant/gh-12-x", false); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestSourceControlForeignBranch(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	s.AddClone("/clone", "main")
	tip := s.SetRemoteBranch("/clone", "ant/gh-12-x", "bob@desktop", "bob's work")
	info, err := s.Branch(ctx, "/clone", "ant/gh-12-x")
	if err != nil || !info.Remote || info.Instance != "bob@desktop" || info.Commit != tip.Hash {
		t.Errorf("Branch() = %+v, %v", info, err)
	}
}

func TestSourceControlPush(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	s.AddClone("/clone", "main")
	_ = s.Fetch(ctx, "/clone")
	_ = s.CreateBranch(ctx, "/clone", "ant/gh-12-x", "origin/main")
	_ = s.AddWorktree(ctx, "/clone", "/wt", "ant/gh-12-x")
	c1 := s.Commit("/wt", "fix", "alice@laptop")
	if _, err := s.Push(ctx, "/wt"); err != nil {
		t.Fatal(err)
	}
	remote := s.RemoteCommits("/clone", "ant/gh-12-x")
	if remote[len(remote)-1] != c1 {
		t.Errorf("remote = %v", remote)
	}
	if commits, _ := s.NewCommits(ctx, "/wt", "origin/ant/gh-12-x"); len(commits) != 0 {
		t.Errorf("unpushed after a push = %v", commits)
	}

	// The remote moved: the push is rejected, never forced.
	s.SetRemoteBranch("/clone", "ant/gh-12-x", "", "someone else")
	s.Commit("/wt", "more", "alice@laptop")
	out, err := s.Push(ctx, "/wt")
	if !errors.Is(err, ports.ErrConflict) || out == "" {
		t.Errorf("Push() = %q, %v", out, err)
	}
	if got := s.RemoteCommits("/clone", "ant/gh-12-x"); got[len(got)-1].Subject != "someone else" {
		t.Errorf("rejected push changed the remote: %v", got)
	}
}

func TestSourceControlFail(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	s.AddClone("/clone", "main")
	boom := errors.New("network down")
	s.Fail(testkit.GitFetch, boom)
	if err := s.Fetch(ctx, "/clone"); !errors.Is(err, boom) {
		t.Errorf("Fetch() = %v", err)
	}
	s.Fail(testkit.GitFetch, nil)
	if err := s.Fetch(ctx, "/clone"); err != nil {
		t.Errorf("Fetch() after clearing = %v", err)
	}
	if err := s.Fetch(ctx, "/none"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Fetch() of a missing clone = %v", err)
	}
}
