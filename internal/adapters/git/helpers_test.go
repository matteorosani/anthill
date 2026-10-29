package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/git"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// webURL is the configured remote of the test repository. A
// url.<bare>.insteadOf rule in the test's global configuration sends it
// to a local bare repository, so no test reaches the network.
const webURL = "https://github.com/acme/web"

var (
	webRepo  = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}
	gh12     = domain.Ref{Repo: webRepo, Kind: domain.KindItem, Number: 12}
	trailers = domain.NewTrailers(gh12, domain.RunnerClaudeCode, "mrosani@LAPTOP-42")
)

// clearedVariables could make git behave differently from one machine to
// another, so the tests remove them.
var clearedVariables = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR",
	"GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
	"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	"GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_EDITOR", "GIT_TERMINAL_PROMPT",
	"GH_TOKEN", "GITHUB_TOKEN",
}

// isolateGit makes git read only a fresh global configuration in a
// temporary directory, with a fixed identity and default branch, and
// never the developer's or the CI runner's configuration. It returns the
// temporary directory.
func isolateGit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, name := range clearedVariables {
		// t.Setenv restores the old value when the test ends; Unsetenv
		// then removes it for the test.
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "gitconfig")
	config := `[user]
	name = Ann Developer
	email = ann@example.com
[init]
	defaultBranch = main
[core]
	autocrlf = false
	safecrlf = false
[commit]
	gpgsign = false
[advice]
	detachedHead = false
`
	if err := os.WriteFile(global, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("HOME", home)
	return dir
}

// gitIn runs git in dir with the test's environment plus env, and fails
// the test when it fails.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, nil, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimRight(out, "\r\n")
}

// gitTry runs git in dir with the test's environment, or env when it is
// not nil, and returns its combined output.
func gitTry(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fixture is a bare remote, a seed clone that plays the other developers
// and the provider, and the developer's clone that Anthill works with.
type fixture struct {
	t     *testing.T
	tmp   string
	bare  string
	seed  string
	clone string
	root  string
	git   *git.Git
	c     ports.Clone
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	tmp := isolateGit(t)
	f := &fixture{
		t:     t,
		tmp:   tmp,
		bare:  filepath.Join(tmp, "remote.git"),
		seed:  filepath.Join(tmp, "seed"),
		clone: filepath.Join(tmp, "clone"),
		root:  filepath.Join(tmp, "worktrees"),
		git:   git.New(nil),
		ctx:   context.Background(),
	}
	gitIn(t, tmp, "init", "--quiet", "--bare", f.bare)
	gitIn(t, tmp, "config", "--global", "url."+filepath.ToSlash(f.bare)+".insteadOf", webURL)
	gitIn(t, tmp, "clone", "--quiet", webURL, f.seed)
	writeFile(t, filepath.Join(f.seed, "README.md"), "# web\n")
	gitIn(t, f.seed, "add", "README.md")
	gitIn(t, f.seed, "commit", "--quiet", "-m", "initial commit")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main")
	gitIn(t, tmp, "clone", "--quiet", webURL, f.clone)
	c, err := f.git.OpenClone(f.ctx, f.clone, webRepo)
	if err != nil {
		t.Fatalf("OpenClone() = %v", err)
	}
	f.c = c
	return f
}

// seedCommit commits a file in the seed clone with the message.
func (f *fixture) seedCommit(file, content, message string) string {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.seed, file), content)
	gitIn(f.t, f.seed, "add", file)
	gitIn(f.t, f.seed, "commit", "--quiet", "-m", message)
	return gitIn(f.t, f.seed, "rev-parse", "HEAD")
}

// remoteTip returns the commit of a branch on the bare remote, or "".
func (f *fixture) remoteTip(branch string) string {
	f.t.Helper()
	out, _ := gitTry(f.bare, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return strings.TrimSpace(out)
}

// commitIn commits a new file in a worktree and returns the commit.
func (f *fixture) commitIn(dir, file, message string) string {
	f.t.Helper()
	writeFile(f.t, filepath.Join(dir, file), message+"\n")
	gitIn(f.t, dir, "add", file)
	gitIn(f.t, dir, "commit", "--quiet", "-m", message)
	return gitIn(f.t, dir, "rev-parse", "HEAD")
}
