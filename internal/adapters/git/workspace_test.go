package git_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
)

const antBranch = "ant/gh-12-fix-login-timeout-on-safari"

func (f *fixture) workspace() *app.Workspace {
	return app.NewWorkspace(f.git, f.root)
}

func (f *fixture) request(labels ...string) app.PrepareRequest {
	settings, _ := domain.Defaults().ForItem(labels)
	return app.PrepareRequest{
		Clone: f.c, Repo: webRepo, Item: gh12, Title: "Fix login timeout on Safari",
		Settings: settings, Trailers: trailers,
	}
}

func (f *fixture) prepare(labels ...string) app.AntWorkspace {
	f.t.Helper()
	ant, err := f.workspace().Prepare(f.ctx, f.request(labels...))
	if err != nil {
		f.t.Fatalf("Prepare() = %v", err)
	}
	return ant
}

// snapshot is everything about the developer's clone that starting an
// ant must leave as it was.
type snapshot struct {
	head, branch, status, diff, staged, stash, readme, untracked string
	config                                                       []string
}

func (f *fixture) snapshot() snapshot {
	f.t.Helper()
	return snapshot{
		head:      gitIn(f.t, f.clone, "rev-parse", "HEAD"),
		branch:    gitIn(f.t, f.clone, "symbolic-ref", "HEAD"),
		status:    gitIn(f.t, f.clone, "status", "--porcelain", "--untracked-files=all"),
		diff:      gitIn(f.t, f.clone, "diff"),
		staged:    gitIn(f.t, f.clone, "diff", "--cached"),
		stash:     gitIn(f.t, f.clone, "stash", "list"),
		readme:    readFile(f.t, filepath.Join(f.clone, "README.md")),
		untracked: readFile(f.t, filepath.Join(f.clone, "notes.txt")),
		config:    strings.Split(gitIn(f.t, f.clone, "config", "--local", "--list"), "\n"),
	}
}

// Scenario "Developer has local changes" (task 6.2).
func TestPrepareLeavesDeveloperWorkingCopyAlone(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.clone, "README.md"), "# web\nunsaved edit\n")
	writeFile(t, filepath.Join(f.clone, "staged.txt"), "staged\n")
	gitIn(t, f.clone, "add", "staged.txt")
	writeFile(t, filepath.Join(f.clone, "notes.txt"), "untracked\n")
	before := f.snapshot()

	ant := f.prepare()

	after := f.snapshot()
	// The only change to the clone's configuration is the one line that
	// allows per-worktree settings.
	var added []string
	for _, line := range after.config {
		if !slices.Contains(before.config, line) {
			added = append(added, line)
		}
	}
	if !slices.Equal(added, []string{"extensions.worktreeconfig=true"}) {
		t.Errorf("configuration lines added to the clone: %v", added)
	}
	after.config = before.config
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the developer's working copy changed:\nbefore %+v\nafter  %+v", before, after)
	}

	want := filepath.Join(f.root, "gh-acme-web", "ant", "gh-12-fix-login-timeout-on-safari")
	if ant.Worktree != want || ant.Branch != antBranch || ant.Base != "main" {
		t.Errorf("Prepare() = %+v, want worktree %s", ant, want)
	}
	if rel, err := filepath.Rel(f.clone, ant.Worktree); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("worktree %s is inside the clone", ant.Worktree)
	}
	if got := readFile(t, filepath.Join(ant.Worktree, "README.md")); got != "# web\n" {
		t.Errorf("worktree README = %q, want the committed one", got)
	}
	if exists(filepath.Join(ant.Worktree, "staged.txt")) || exists(filepath.Join(ant.Worktree, "notes.txt")) {
		t.Error("the developer's uncommitted files reached the worktree")
	}
	if got := gitIn(t, ant.Worktree, "symbolic-ref", "--short", "HEAD"); got != antBranch {
		t.Errorf("worktree branch = %s", got)
	}
}

// Scenario "Ticket targets a release branch" (task 6.1).
func TestPrepareFromFetchedReleaseBranch(t *testing.T) {
	f := newFixture(t)
	tip := f.seedCommit("fix.txt", "fix\n", "release fix")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main:release/2.4")
	ant := f.prepare("base:release/2.4")
	if ant.Base != "release/2.4" {
		t.Errorf("base = %q", ant.Base)
	}
	if got := gitIn(t, ant.Worktree, "rev-parse", "HEAD"); got != tip {
		t.Errorf("ant branch starts at %s, want origin/release/2.4 %s", got, tip)
	}
}

// The base branch comes from the repository setting, then the remote's
// default branch (task 6.1).
func TestPrepareBaseFromSettingAndRemoteDefault(t *testing.T) {
	f := newFixture(t)
	tip := f.seedCommit("dev.txt", "dev\n", "develop work")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main:develop")
	req := f.request()
	req.Settings = domain.ConfigLayers{RepositoryFile: &domain.SettingValues{BaseBranch: ptr("develop")}}.Resolve()
	ant, err := f.workspace().Prepare(f.ctx, req)
	if err != nil || ant.Base != "develop" || gitIn(t, ant.Worktree, "rev-parse", "HEAD") != tip {
		t.Errorf("Prepare() with base-branch: develop = %+v, %v", ant, err)
	}
}

// Scenario "Base branch missing" (task 6.1).
func TestPrepareBaseBranchMissing(t *testing.T) {
	f := newFixture(t)
	_, err := f.workspace().Prepare(f.ctx, f.request("base:release/2.4"))
	if !errors.Is(err, domain.ErrBaseBranchMissing) || err.Error() != "base branch release/2.4 not found" {
		t.Errorf("Prepare() = %v", err)
	}
	if branches := gitIn(t, f.clone, "branch", "--list", "ant/*"); branches != "" || exists(f.root) {
		t.Errorf("created branches %q or the worktree root", branches)
	}
}

// Scenario "Name already taken remotely by someone else" (task 6.3).
func TestPrepareNameTakenBySomeoneElse(t *testing.T) {
	f := newFixture(t)
	gitIn(t, f.seed, "checkout", "--quiet", "-b", antBranch)
	theirs := f.seedCommit("bob.txt", "bob\n", "bob's work\n\nAnthill-Item: gh-12\nAnthill-Agent: codex\nAnthill-Instance: bob@desktop")
	gitIn(t, f.seed, "push", "--quiet", "origin", antBranch)

	ant := f.prepare()
	if ant.Branch != antBranch+"-2" {
		t.Errorf("branch = %q, want the -2 suffix", ant.Branch)
	}
	if got := f.remoteTip(antBranch); got != theirs {
		t.Errorf("the existing branch moved to %s", got)
	}
	if exists(filepath.Join(ant.Worktree, "bob.txt")) {
		t.Error("the new branch was based on the foreign one")
	}
}

// A pushed branch of this instance for this item is reused.
func TestPrepareReusesOwnPushedBranch(t *testing.T) {
	f := newFixture(t)
	gitIn(t, f.seed, "checkout", "--quiet", "-b", antBranch)
	ours := f.seedCommit("work.txt", "work\n", "earlier work\n\nAnthill-Item: gh-12\nAnthill-Agent: claude-code\nAnthill-Instance: mrosani@LAPTOP-42")
	gitIn(t, f.seed, "push", "--quiet", "origin", antBranch)
	ant := f.prepare()
	if ant.Branch != antBranch || gitIn(t, ant.Worktree, "rev-parse", "HEAD") != ours {
		t.Errorf("Prepare() = %+v", ant)
	}
}

// Scenario "Second run for the same item", and a worktree recreated from
// the local branch when it is gone.
func TestPrepareSecondRunReusesWorktree(t *testing.T) {
	f := newFixture(t)
	first := f.prepare()
	work := f.commitIn(first.Worktree, "a.txt", "work")
	req := f.request()
	req.Branch, req.Worktree = first.Branch, first.Worktree
	second, err := f.workspace().Prepare(f.ctx, req)
	if err != nil || second != first {
		t.Errorf("second Prepare() = %+v, %v", second, err)
	}
	if branches := gitIn(t, f.clone, "branch", "--list", "ant/*", "--format=%(refname:short)"); branches != antBranch {
		t.Errorf("branches = %q", branches)
	}

	gitIn(t, f.clone, "worktree", "remove", first.Worktree)
	third, err := f.workspace().Prepare(f.ctx, req)
	if err != nil || third != first || gitIn(t, third.Worktree, "rev-parse", "HEAD") != work {
		t.Errorf("Prepare() after the worktree was removed = %+v, %v", third, err)
	}
}

// commitMessage returns the full message of HEAD in dir.
func commitMessage(t *testing.T, dir string) string {
	t.Helper()
	return gitIn(t, dir, "log", "-1", "--format=%B")
}

// Scenario "Commit trailer" (task 6.4).
func TestCommitInWorktreeCarriesTrailers(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	want := "Anthill-Item: gh-12\nAnthill-Agent: claude-code\nAnthill-Instance: mrosani@LAPTOP-42"

	f.commitIn(ant.Worktree, "a.txt", "Fix the login timeout")
	if got := commitMessage(t, ant.Worktree); got != "Fix the login timeout\n\n"+want {
		t.Errorf("message =\n%s", got)
	}
	if got := gitIn(t, ant.Worktree, "log", "-1", "--format=%an <%ae>"); got != "Ann Developer <ann@example.com>" {
		t.Errorf("author = %s, want the developer's identity", got)
	}

	// A message with a body, existing trailers and comment lines.
	writeFile(t, filepath.Join(ant.Worktree, "b.txt"), "b\n")
	gitIn(t, ant.Worktree, "add", "b.txt")
	msg := filepath.Join(f.tmp, "msg.txt")
	writeFile(t, msg, "Add a test\n\nExplain why.\n\nRefs: #12\n# a comment line\n")
	gitIn(t, ant.Worktree, "commit", "--quiet", "--cleanup=strip", "-F", msg)
	if got := commitMessage(t, ant.Worktree); got != "Add a test\n\nExplain why.\n\nRefs: #12\n"+want {
		t.Errorf("message with a body =\n%s", got)
	}

	// Amending keeps one of each trailer.
	gitIn(t, ant.Worktree, "commit", "--quiet", "--amend", "-m", "Add a test\n\nAnthill-Item: gh-99")
	if got := commitMessage(t, ant.Worktree); got != "Add a test\n\n"+want {
		t.Errorf("amended message =\n%s", got)
	}

	// The trailers are the worktree's own: a commit in the developer's
	// clone has none.
	f.commitIn(f.clone, "dev.txt", "developer commit")
	if got := commitMessage(t, f.clone); got != "developer commit" {
		t.Errorf("developer's commit message = %q", got)
	}
}

// installHook writes an executable sh hook.
func installHook(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	writeFile(t, path, "#!/bin/sh\n"+body)
	// Git for Windows ignores the mode and runs every hook through sh.
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// The repository's own hooks still run in an ant worktree, after
// Anthill's, with the same arguments and input (task 6.4).
func TestRepositoryHooksChained(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(f.tmp, "marker.txt")
	t.Setenv("ANTHILL_TEST_MARKER", filepath.ToSlash(marker))
	hooks := filepath.Join(f.clone, ".git", "hooks")
	installHook(t, hooks, "pre-commit", `echo "pre-commit $#" >> "$ANTHILL_TEST_MARKER"`+"\n")
	installHook(t, hooks, "commit-msg", `grep -q '^Anthill-Instance: mrosani@LAPTOP-42$' "$1" && echo "commit-msg saw trailers" >> "$ANTHILL_TEST_MARKER"`+"\n")
	installHook(t, hooks, "prepare-commit-msg", `echo "prepare-commit-msg $2" >> "$ANTHILL_TEST_MARKER"`+"\n")
	installHook(t, hooks, "pre-push", `echo "repository pre-push" >> "$ANTHILL_TEST_MARKER"`+"\n")
	ant := f.prepare()

	f.commitIn(ant.Worktree, "a.txt", "work")
	want := "pre-commit 0\nprepare-commit-msg message\ncommit-msg saw trailers\n"
	if got := readFile(t, marker); got != want {
		t.Errorf("hooks ran:\n%s\nwant\n%s", got, want)
	}

	// A failing repository pre-commit blocks the commit.
	installHook(t, hooks, "pre-commit", "echo 'lint failed' >&2\nexit 1\n")
	head := gitIn(t, ant.Worktree, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(ant.Worktree, "b.txt"), "b\n")
	gitIn(t, ant.Worktree, "add", "b.txt")
	out, err := gitTry(ant.Worktree, nil, "commit", "-m", "blocked")
	if err == nil || !strings.Contains(out, "lint failed") || gitIn(t, ant.Worktree, "rev-parse", "HEAD") != head {
		t.Errorf("commit with a failing pre-commit: %v\n%s", err, out)
	}

	// pre-push is never chained.
	if _, err := gitTry(ant.Worktree, nil, "push", "origin", "HEAD"); err == nil {
		t.Error("an agent push succeeded")
	}
	if strings.Contains(readFile(t, marker), "repository pre-push") {
		t.Error("the repository's pre-push ran in the worktree")
	}
}

// A relative core.hooksPath, as husky sets, is found from the top of the
// worktree.
func TestRelativeHooksPathChained(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(f.tmp, "marker.txt")
	t.Setenv("ANTHILL_TEST_MARKER", filepath.ToSlash(marker))
	installHook(t, filepath.Join(f.seed, ".husky"), "pre-commit", `echo "husky in $(basename "$PWD")" >> "$ANTHILL_TEST_MARKER"`+"\n")
	gitIn(t, f.seed, "add", "--chmod=+x", ".husky/pre-commit")
	gitIn(t, f.seed, "commit", "--quiet", "-m", "add husky")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main")
	gitIn(t, f.clone, "config", "core.hooksPath", ".husky")

	ant := f.prepare()
	f.commitIn(ant.Worktree, "a.txt", "work")
	if got := readFile(t, marker); got != "husky in gh-12-fix-login-timeout-on-safari\n" {
		t.Errorf("husky hook output = %q", got)
	}
	if !strings.HasSuffix(commitMessage(t, ant.Worktree), "Anthill-Instance: mrosani@LAPTOP-42") {
		t.Error("the trailers are missing when core.hooksPath is set")
	}
}

func ptr[T any](v T) *T { return &v }
