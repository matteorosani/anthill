package git_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// fixWorktree checks out commit detached in a new worktree of the
// developer's clone, as a fix pass's temporary worktree is, and commits
// a fix in it.
func (f *fixture) fixWorktree(name, commit string) (string, string) {
	f.t.Helper()
	return f.fixWorktreeFile(name, commit, "fix.txt")
}

func (f *fixture) fixWorktreeFile(name, commit, file string) (string, string) {
	f.t.Helper()
	dir := filepath.Join(f.root, name)
	gitIn(f.t, f.clone, "worktree", "add", "--quiet", "--detach", dir, commit)
	gitIn(f.t, dir, "config", "user.name", "Anthill")
	gitIn(f.t, dir, "config", "user.email", "anthill@example.com")
	return dir, f.commitIn(dir, file, "Remove the unused import")
}

// Tasks 16.5 and 16.6: a verified fix pass is pushed to the PR branch
// from a detached worktree; a branch that moved takes nothing.
func TestPushToPRBranch(t *testing.T) {
	f := newFixture(t)
	gitIn(t, f.seed, "checkout", "--quiet", "-b", "feature/login")
	head := f.seedCommit("login.go", "package login\n", "Human work")
	gitIn(t, f.seed, "push", "--quiet", "origin", "feature/login")
	if err := f.git.Fetch(f.ctx, f.c); err != nil {
		t.Fatal(err)
	}

	dir, fix := f.fixWorktree("fix-34", head)
	// A fix pass counts its commits from the head it started at.
	if got, err := f.git.NewCommits(f.ctx, f.c, dir, head); err != nil || len(got) != 1 || got[0].Hash != fix {
		t.Fatalf("NewCommits() = %v, %v", got, err)
	}
	out, err := f.git.PushTo(f.ctx, f.c, dir, ports.PushTarget{Branch: "feature/login", Expect: head})
	if err != nil {
		t.Fatalf("PushTo() = %v\n%s", err, out)
	}
	if f.remoteTip("feature/login") != fix {
		t.Error("the fix did not reach the PR branch")
	}
	if got := gitIn(t, f.clone, "rev-parse", "origin/feature/login"); got != fix {
		t.Errorf("remote-tracking branch at %s, want %s", got, fix)
	}

	// Scenario "Author pushed during the fix": the branch is no longer at
	// the head the pass started from.
	gitIn(t, f.seed, "pull", "--quiet", "origin", "feature/login")
	theirs := f.seedCommit("login.go", "package login // edited\n", "Author's push")
	gitIn(t, f.seed, "push", "--quiet", "origin", "feature/login")
	dir2, _ := f.fixWorktreeFile("fix-34-2", fix, "fix2.txt")
	if _, err := f.git.PushTo(f.ctx, f.c, dir2, ports.PushTarget{Branch: "feature/login", Expect: fix}); !errors.Is(err, ports.ErrConflict) {
		t.Errorf("PushTo() with the branch moved = %v", err)
	}
	// Without Expect, the remote rejects the push as non-fast-forward.
	if out, err := f.git.PushTo(f.ctx, f.c, dir2, ports.PushTarget{Branch: "feature/login"}); !errors.Is(err, ports.ErrConflict) {
		t.Errorf("PushTo() not fast-forward = %v\n%s", err, out)
	}
	if f.remoteTip("feature/login") != theirs {
		t.Error("the author's commit was overwritten")
	}
}

// Task 16.6: a fork that allows maintainer edits gets the push at its
// own URL, and only when the URL points to the fork.
func TestPushToFork(t *testing.T) {
	f := newFixture(t)
	fork := filepath.Join(f.tmp, "fork.git")
	const forkURL = "https://github.com/ada/web.git"
	gitIn(t, f.tmp, "clone", "--quiet", "--bare", f.bare, fork)
	gitIn(t, f.tmp, "config", "--global", "url."+filepath.ToSlash(fork)+".insteadOf", forkURL)
	gitIn(t, f.seed, "checkout", "--quiet", "-b", "feature/login")
	head := f.seedCommit("login.go", "package login\n", "Contributor work")
	gitIn(t, f.seed, "push", "--quiet", forkURL, "feature/login")
	// A fix pass fetches the PR's head commit, which GitHub also offers
	// from the base repository as refs/pull/<n>/head.
	gitIn(t, f.clone, "fetch", "--quiet", forkURL, "feature/login")
	forkRepo := domain.Repository{Provider: domain.GitHub, Org: "ada", Name: "web"}

	dir, fix := f.fixWorktree("fix-35", head)
	if _, err := f.git.PushTo(f.ctx, f.c, dir, ports.PushTarget{Branch: "feature/login", Expect: head, Repo: webRepo, URL: forkURL}); err == nil {
		t.Error("PushTo() to a URL that is not the fork's succeeded")
	}
	out, err := f.git.PushTo(f.ctx, f.c, dir, ports.PushTarget{Branch: "feature/login", Expect: head, Repo: forkRepo, URL: forkURL})
	if err != nil {
		t.Fatalf("PushTo() = %v\n%s", err, out)
	}
	if got := gitIn(t, fork, "rev-parse", "refs/heads/feature/login"); got != fix {
		t.Errorf("fork branch at %s, want %s", got, fix)
	}
	if f.remoteTip("feature/login") != "" {
		t.Error("the fix reached the base repository")
	}
}

// Task 16.5: the commits of a fix pass carry the trailers it sets.
func TestSetTrailers(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	fix := domain.Trailers{Item: "gh-pr-34", Agent: "codex", Instance: "mrosani@LAPTOP-42"}
	if err := f.git.SetTrailers(f.ctx, f.c, ant.Worktree, fix); err != nil {
		t.Fatal(err)
	}
	f.commitIn(ant.Worktree, "a.txt", "Remove the unused import")
	msg := gitIn(t, ant.Worktree, "log", "-1", "--format=%B")
	for _, want := range []string{"Anthill-Item: gh-pr-34", "Anthill-Agent: codex", "Anthill-Instance: mrosani@LAPTOP-42"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if err := f.git.SetTrailers(f.ctx, f.c, filepath.Join(f.root, "none"), fix); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("SetTrailers() on no worktree = %v", err)
	}
}
