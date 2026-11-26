package testkit_test

import (
	"context"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// A new PR starts at its source branch's last commit, and a push moves
// the head of the branch's open PRs, as on a code host.
func TestCodeHostJoinsPushesAndPullRequests(t *testing.T) {
	ctx := context.Background()
	repo := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}
	p := testkit.NewProvider(testkit.NewClock(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)))
	sc := testkit.NewSourceControl()
	clone := sc.AddClone("/src/shop", repo, "main")
	host := testkit.NewCodeHost(p, sc, "/src/shop", repo)
	git, client := host.Git(), host.Client("mrosani")

	if err := git.Fetch(ctx, clone); err != nil {
		t.Fatal(err)
	}
	if err := git.CreateBranch(ctx, clone, "ant/gh-12-x", "main"); err != nil {
		t.Fatal(err)
	}
	if err := git.AddWorktree(ctx, clone, "/w/12", "ant/gh-12-x", domain.Trailers{Item: "gh-12", Agent: "claude-code", Instance: "mrosani@laptop"}); err != nil {
		t.Fatal(err)
	}
	first := git.Commit("/w/12", "fix: one")
	if _, err := git.Push(ctx, clone, "/w/12", "ant/gh-12-x", domain.BranchOwner{}); err != nil {
		t.Fatal(err)
	}
	pr, err := client.OpenPullRequest(ctx, ports.NewPullRequest{Repo: repo, Title: "Fix", Source: "ant/gh-12-x", Target: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.HeadCommit != first.Hash {
		t.Errorf("new PR head = %q, want %q", pr.HeadCommit, first.Hash)
	}

	second := git.Commit("/w/12", "fix: two")
	if _, err := git.Push(ctx, clone, "/w/12", "ant/gh-12-x", domain.BranchOwner{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.PullRequest(pr.Ref); got.HeadCommit != second.Hash {
		t.Errorf("PR head after the push = %q, want %q", got.HeadCommit, second.Hash)
	}
}
