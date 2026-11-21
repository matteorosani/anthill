package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// A detached worktree commits on top of its commit, counts its commits
// from it, and pushes to a branch only while the branch is still there.
func TestSourceControlPushTo(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewSourceControl()
	c := s.AddClone("/clone", scRepo, "main")
	head := s.SetRemoteBranch("/clone", "feature/login", domain.BranchOwner{}, "Human work")
	if err := s.AddDetachedWorktree(ctx, c, "/fix", head.Hash); err != nil {
		t.Fatal(err)
	}
	tr := domain.Trailers{Item: "gh-pr-34", Agent: "codex", Instance: "me@pc"}
	if err := s.SetTrailers(ctx, c, "/fix", tr); err != nil || s.Trailers("/fix") != tr {
		t.Fatalf("SetTrailers() = %v, trailers %+v", err, s.Trailers("/fix"))
	}
	fix := s.Commit("/fix", "Remove the unused import")
	if got, err := s.NewCommits(ctx, c, "/fix", head.Hash); err != nil || len(got) != 1 || got[0] != fix {
		t.Fatalf("NewCommits() = %v, %v", got, err)
	}
	if _, err := s.PushTo(ctx, c, "/fix", ports.PushTarget{Branch: "feature/login", Expect: "other"}); !errors.Is(err, ports.ErrConflict) {
		t.Errorf("PushTo() with a moved branch = %v", err)
	}
	if _, err := s.PushTo(ctx, c, "/fix", ports.PushTarget{Branch: "feature/login", Expect: head.Hash}); err != nil {
		t.Fatal(err)
	}
	if got := s.RemoteCommits("/clone", "feature/login"); got[len(got)-1] != fix {
		t.Errorf("remote = %v", got)
	}

	const url = "https://github.com/ada/web.git"
	forkHead := s.SetForkBranch("/clone", url, "feature/x", "Contributor work")
	if err := s.AddDetachedWorktree(ctx, c, "/fix2", forkHead.Hash); err != nil {
		t.Fatal(err)
	}
	fix2 := s.Commit("/fix2", "Fix")
	fork := domain.Repository{Provider: domain.GitHub, Org: "ada", Name: "web"}
	if _, err := s.PushTo(ctx, c, "/fix2", ports.PushTarget{Branch: "feature/x", Repo: scRepo, URL: url}); err == nil {
		t.Error("PushTo() to a URL of another repository succeeded")
	}
	if _, err := s.PushTo(ctx, c, "/fix2", ports.PushTarget{Branch: "feature/x", Expect: forkHead.Hash, Repo: fork, URL: url}); err != nil {
		t.Fatal(err)
	}
	if got := s.ForkCommits(url, "feature/x"); got[len(got)-1] != fix2 {
		t.Errorf("fork = %v", got)
	}
}
