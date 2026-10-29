package git_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// authServer is a git server on 127.0.0.1 that answers every request
// with 401, so git asks for credentials. It records whether any request
// carried them.
type authServer struct {
	*httptest.Server
	mu         sync.Mutex
	requests   int
	authorized int
}

func newAuthServer(t *testing.T) *authServer {
	t.Helper()
	s := &authServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		if r.Header.Get("Authorization") != "" {
			s.authorized++
		}
		s.mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *authServer) counts() (requests, authorized int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.authorized
}

// credentialHelper configures, in the test's global configuration, a
// credential helper that would hand out a password, and returns the file
// it writes to when git calls it.
func credentialHelper(t *testing.T, tmp string) string {
	t.Helper()
	called := filepath.Join(tmp, "helper-called")
	helper := filepath.Join(tmp, "helper.sh")
	writeFile(t, helper, "#!/bin/sh\necho \"$1\" >> '"+filepath.ToSlash(called)+"'\necho username=ann\necho password=secret\n")
	if err := os.Chmod(helper, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, tmp, "config", "--global", "credential.helper", "!sh '"+filepath.ToSlash(helper)+"'")
	return called
}

// skipWithoutHTTP skips when this git cannot speak HTTP at all.
func skipWithoutHTTP(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "remote helper") && strings.Contains(out, "http") {
		t.Skipf("this git has no HTTP support: %s", out)
	}
}

// Scenario "Stray git push" (ant-execution), task 6.5: a push from the
// agent's environment fails without any real credential.
func TestAgentPushFails(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	f.commitIn(ant.Worktree, "a.txt", "work")
	agentEnv := domain.AgentEnv(os.Environ())

	t.Run("pre-push hook refuses a push to the real remote", func(t *testing.T) {
		out, err := gitTry(ant.Worktree, agentEnv, "push", "origin", "HEAD")
		if err == nil || !strings.Contains(out, domain.MessageUsePushTool) {
			t.Errorf("git push: %v\n%s", err, out)
		}
		if f.remoteTip(ant.Branch) != "" {
			t.Error("the branch reached the remote")
		}
	})

	t.Run("no credentials for https", func(t *testing.T) {
		server := newAuthServer(t)
		called := credentialHelper(t, f.tmp)
		url := server.URL + "/acme/web.git"

		// Control: from the developer's clone, git asks the helper.
		out, err := gitTry(f.clone, nil, "push", url, "main")
		skipWithoutHTTP(t, out)
		if err == nil || !exists(called) {
			t.Fatalf("control push: %v, helper called %v\n%s", err, exists(called), out)
		}
		if err := os.Remove(called); err != nil {
			t.Fatal(err)
		}
		_, before := server.counts()

		cases := []struct {
			name string
			dir  string
			env  []string
		}{
			// The worktree's own configuration alone.
			{"worktree configuration", ant.Worktree, append(os.Environ(), "GIT_TERMINAL_PROMPT=0")},
			// The agent's environment alone, outside the worktree.
			{"agent environment", f.clone, agentEnv},
			// Both, as an agent runs.
			{"agent in its worktree", ant.Worktree, agentEnv},
		}
		for _, c := range cases {
			out, err := gitTry(c.dir, c.env, "push", "--no-verify", url, "HEAD:refs/heads/x")
			if err == nil {
				t.Errorf("%s: push succeeded\n%s", c.name, out)
			}
			if exists(called) {
				t.Errorf("%s: the credential helper was called\n%s", c.name, out)
			}
		}
		requests, authorized := server.counts()
		if requests == 0 || authorized != before {
			t.Errorf("server saw %d requests, %d with credentials after the control's %d", requests, authorized, before)
		}
	})

	t.Run("no ssh", func(t *testing.T) {
		out, err := gitTry(ant.Worktree, agentEnv, "push", "--no-verify", "git@127.0.0.1:acme/web.git", "HEAD")
		if err == nil || !strings.Contains(out, domain.MessageUsePushTool) {
			t.Errorf("ssh push: %v\n%s", err, out)
		}
	})
}

func (f *fixture) push(ant app.AntWorkspace) (string, error) {
	return f.workspace().Push(f.ctx, app.PushRequest{
		Clone: f.c, Worktree: ant.Worktree, Owner: trailers.Owner(),
		Policy: domain.PushRequest{Kind: domain.PushAnt, Allowed: ant.Branch, Base: ant.Base, Protected: []string{"release/*"}},
	})
}

// The push policy with a real remote (task 6.6).
func TestPushPolicy(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()

	// Rebase and amend are allowed before the first push.
	f.commitIn(ant.Worktree, "a.txt", "work")
	gitIn(t, ant.Worktree, "commit", "--quiet", "--amend", "-m", "work, amended")
	out, err := f.push(ant)
	if err != nil {
		t.Fatalf("Push() = %v\n%s", err, out)
	}
	if f.remoteTip(ant.Branch) != gitIn(t, ant.Worktree, "rev-parse", "HEAD") {
		t.Error("the branch did not reach the remote")
	}

	// Scenario "Amend after the first push".
	pushed := f.remoteTip(ant.Branch)
	gitIn(t, ant.Worktree, "commit", "--quiet", "--amend", "-m", "work, amended again")
	_, err = f.push(ant)
	if !errors.Is(err, ports.ErrHistoryRewritten) || !strings.HasPrefix(err.Error(), domain.MessageHistoryRewritten) {
		t.Errorf("Push() after amending a pushed commit = %v", err)
	}
	if f.remoteTip(ant.Branch) != pushed {
		t.Error("the rewritten history reached the remote")
	}
	gitIn(t, ant.Worktree, "reset", "--quiet", "--hard", "origin/"+ant.Branch)

	// Scenario "Push rejected because the remote moved" (ant-tools), also
	// after a fetch: someone else's commit is not a rewrite.
	gitIn(t, f.seed, "fetch", "--quiet", "origin")
	gitIn(t, f.seed, "checkout", "--quiet", "-b", ant.Branch, "origin/"+ant.Branch)
	theirs := f.seedCommit("review.txt", "suggestion\n", "reviewer suggestion")
	gitIn(t, f.seed, "push", "--quiet", "origin", ant.Branch)
	if err := f.git.Fetch(f.ctx, f.c); err != nil {
		t.Fatal(err)
	}
	f.commitIn(ant.Worktree, "b.txt", "more work")
	out, err = f.push(ant)
	if !errors.Is(err, ports.ErrConflict) || !strings.Contains(out, "rejected") {
		t.Errorf("Push() after the remote moved = %v\n%s", err, out)
	}
	// Scenario "Base moved while PR is open": merge, never rebase and
	// force, then push again.
	gitIn(t, ant.Worktree, "merge", "--quiet", "--no-edit", "origin/"+ant.Branch)
	if out, err := f.push(ant); err != nil {
		t.Fatalf("Push() after merging = %v\n%s", err, out)
	}
	if got := gitIn(t, f.bare, "merge-base", "--is-ancestor", theirs, "refs/heads/"+ant.Branch); got != "" {
		t.Errorf("merge-base output %q", got)
	}
	if !strings.HasSuffix(gitIn(t, f.bare, "log", "-1", "--format=%B", "refs/heads/"+ant.Branch), "Anthill-Instance: mrosani@LAPTOP-42") {
		t.Error("the merge commit has no trailers")
	}
}

// Scenario "Ant attempts to push to main" (task 6.6).
func TestPushToMainRefused(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	main := f.remoteTip("main")
	gitIn(t, ant.Worktree, "checkout", "--quiet", "--ignore-other-worktrees", "main")
	f.commitIn(ant.Worktree, "a.txt", "on main")
	_, err := f.push(ant)
	if !errors.Is(err, domain.ErrPushRefused) || err.Error() != "push to main is not allowed" {
		t.Errorf("Push() = %v", err)
	}
	if f.remoteTip("main") != main {
		t.Error("main moved")
	}

	// A protected pattern and a detached HEAD are refused too.
	gitIn(t, ant.Worktree, "checkout", "--quiet", "-b", "release/2.4")
	if _, err := f.push(ant); err == nil || err.Error() != "push to release/2.4 is not allowed" {
		t.Errorf("Push() to a protected branch = %v", err)
	}
	gitIn(t, ant.Worktree, "checkout", "--quiet", "--detach")
	if _, err := f.push(ant); !errors.Is(err, domain.ErrPushRefused) {
		t.Errorf("Push() from a detached HEAD = %v", err)
	}
}

func TestNewCommitsAndUnpushed(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	c1 := f.commitIn(ant.Worktree, "a.txt", "first")
	c2 := f.commitIn(ant.Worktree, "b.txt", "second")
	commits, err := f.git.NewCommits(f.ctx, f.c, ant.Worktree, "main")
	if err != nil || len(commits) != 2 || commits[0] != (ports.Commit{Hash: c1, Subject: "first"}) || commits[1].Hash != c2 {
		t.Errorf("NewCommits() = %v, %v", commits, err)
	}
	if _, err := f.git.NewCommits(f.ctx, f.c, ant.Worktree, "release/9"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("NewCommits() from a missing base = %v", err)
	}
	if unpushed, err := f.git.Unpushed(f.ctx, f.c, ant.Branch, ""); err != nil || len(unpushed) != 2 {
		t.Errorf("Unpushed() = %v, %v", unpushed, err)
	}
	if unpushed, _ := f.git.Unpushed(f.ctx, f.c, ant.Branch, c1); len(unpushed) != 1 || unpushed[0].Hash != c2 {
		t.Errorf("Unpushed(c1) = %v", unpushed)
	}
	if unpushed, _ := f.git.Unpushed(f.ctx, f.c, ant.Branch, strings.Repeat("0", 40)); len(unpushed) != 2 {
		t.Errorf("Unpushed() with an unknown commit = %v", unpushed)
	}
	if _, err := f.git.Unpushed(f.ctx, f.c, "none", ""); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Unpushed() of a missing branch = %v", err)
	}
	if _, err := f.push(ant); err != nil {
		t.Fatal(err)
	}
	if unpushed, _ := f.git.Unpushed(f.ctx, f.c, ant.Branch, ""); len(unpushed) != 0 {
		t.Errorf("Unpushed() after pushing = %v", unpushed)
	}
}

func (f *fixture) cleanup(ant app.AntWorkspace, req app.CleanupRequest) (domain.CleanupPlan, error) {
	req.Clone, req.Branch, req.Worktree = f.c, ant.Branch, ant.Worktree
	return f.workspace().Cleanup(f.ctx, req)
}

// Scenario "PR merged" (task 6.7), with a squash merge after which the
// provider kept the branch.
func TestCleanupPRMerged(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	f.commitIn(ant.Worktree, "a.txt", "work")
	if _, err := f.push(ant); err != nil {
		t.Fatal(err)
	}
	head := f.remoteTip(ant.Branch)
	gitIn(t, f.seed, "fetch", "--quiet", "origin")
	gitIn(t, f.seed, "merge", "--quiet", "--squash", "origin/"+ant.Branch)
	gitIn(t, f.seed, "commit", "--quiet", "-m", "Fix login (#34)")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main")

	plan, err := f.cleanup(ant, app.CleanupRequest{PR: domain.PRMerged, PRHead: head})
	if err != nil || !plan.RemoveWorktree || !plan.DeleteLocalBranch || !plan.DeleteRemoteBranch {
		t.Fatalf("Cleanup() = %+v, %v", plan, err)
	}
	if exists(ant.Worktree) {
		t.Error("the worktree is still there")
	}
	if got := gitIn(t, f.clone, "branch", "--list", ant.Branch); got != "" {
		t.Errorf("local branch left: %q", got)
	}
	if f.remoteTip(ant.Branch) != "" {
		t.Error("the remote branch is still there")
	}
	if strings.Contains(gitIn(t, f.clone, "worktree", "list"), ant.Worktree) {
		t.Error("the worktree is still registered")
	}
}

// After a merge, a provider that deleted the branch: Anthill deletes only
// the local side.
func TestCleanupPRMergedProviderDeletedBranch(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	f.commitIn(ant.Worktree, "a.txt", "work")
	if _, err := f.push(ant); err != nil {
		t.Fatal(err)
	}
	head := f.remoteTip(ant.Branch)
	gitIn(t, f.seed, "push", "--quiet", "origin", "--delete", ant.Branch)
	plan, err := f.cleanup(ant, app.CleanupRequest{PR: domain.PRMerged, PRHead: head})
	if err != nil || plan.DeleteRemoteBranch || !plan.RemoveWorktree || exists(ant.Worktree) {
		t.Errorf("Cleanup() = %+v, %v", plan, err)
	}
}

// Scenario "Unpushed work" (task 6.7), and uncommitted changes.
func TestCleanupUnpushedWork(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	f.commitIn(ant.Worktree, "a.txt", "not pushed")
	abandoned := app.CleanupRequest{Abandoned: true, Requested: true}
	_, err := f.cleanup(ant, abandoned)
	var confirm *app.NeedsConfirmationError
	if !errors.As(err, &confirm) || len(confirm.Unpushed) != 1 || confirm.Unpushed[0].Subject != "not pushed" {
		t.Fatalf("Cleanup() = %v", err)
	}
	if !exists(ant.Worktree) || gitIn(t, f.clone, "branch", "--list", ant.Branch) == "" {
		t.Fatal("work removed without confirmation")
	}

	writeFile(t, filepath.Join(ant.Worktree, "draft.txt"), "draft\n")
	_, err = f.cleanup(ant, abandoned)
	if !errors.As(err, &confirm) || !confirm.Uncommitted {
		t.Fatalf("Cleanup() with uncommitted changes = %v", err)
	}

	abandoned.Confirmed = true
	plan, err := f.cleanup(ant, abandoned)
	if err != nil || !plan.Force || exists(ant.Worktree) || gitIn(t, f.clone, "branch", "--list", ant.Branch) != "" {
		t.Errorf("confirmed Cleanup() = %+v, %v", plan, err)
	}
}

func TestWorktreeStatusAndRemove(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	st, err := f.git.WorktreeStatus(f.ctx, f.c, ant.Worktree)
	if err != nil || st != (ports.WorktreeStatus{Branch: ant.Branch}) {
		t.Errorf("WorktreeStatus() = %+v, %v", st, err)
	}
	writeFile(t, filepath.Join(ant.Worktree, "x.txt"), "x\n")
	if st, _ := f.git.WorktreeStatus(f.ctx, f.c, ant.Worktree); !st.Dirty {
		t.Error("an untracked file is not dirty")
	}
	if err := f.git.RemoveWorktree(f.ctx, f.c, ant.Worktree, false); err == nil {
		t.Error("removed a dirty worktree without force")
	}
	if err := f.git.RemoveWorktree(f.ctx, f.c, ant.Worktree, true); err != nil || exists(ant.Worktree) {
		t.Errorf("RemoveWorktree(force) = %v", err)
	}
	if _, err := f.git.WorktreeStatus(f.ctx, f.c, ant.Worktree); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("WorktreeStatus() of a removed worktree = %v", err)
	}
	if err := f.git.RemoveWorktree(f.ctx, f.c, ant.Worktree, false); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("RemoveWorktree() of a removed worktree = %v", err)
	}
	if err := f.git.AddWorktree(f.ctx, f.c, ant.Worktree, "none", trailers); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("AddWorktree() of a missing branch = %v", err)
	}
	if err := f.git.AddWorktree(f.ctx, f.c, ant.Worktree, ant.Branch, domain.Trailers{}); err == nil {
		t.Error("AddWorktree() without trailers succeeded")
	}
}
