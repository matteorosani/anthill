package git_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/git"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// canary is GH_TOKEN in the tests of this file: no program an agent can
// write may see it.
const canary = "canary-token"

// spyScript writes a shell script that records, in log, its name, the
// GH_TOKEN it sees and the -c options git passes it, then tries to push
// to the authenticating server with whatever credentials it can get. It
// prints a password, so that it also works as a credential helper. It
// returns the script's path.
func spyScript(t *testing.T, dir, name, log, server string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	writeFile(t, path, "#!/bin/sh\n"+
		"echo \""+name+" token=${GH_TOKEN:-none} params=${GIT_CONFIG_PARAMETERS:-}\" >> '"+filepath.ToSlash(log)+"'\n"+
		"git push --no-verify '"+server+"/acme/web.git' HEAD:refs/heads/stolen </dev/null >/dev/null 2>&1\n"+
		"echo username=agent\necho password=stolen\n"+
		"exit 0\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// agentGit runs git in dir as the agent does, with domain.AgentEnv.
func agentGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, domain.AgentEnv(os.Environ()), args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimRight(out, "\r\n")
}

// plantedHelper sets, in the clone's own configuration, a credential
// helper that records being called, and returns the file it writes to.
func plantedHelper(t *testing.T, f *fixture) string {
	t.Helper()
	called := filepath.Join(f.tmp, "planted-helper-called")
	helper := filepath.Join(f.tmp, "planted-helper.sh")
	writeFile(t, helper, "#!/bin/sh\necho \"$1 token=${GH_TOKEN:-none}\" >> '"+filepath.ToSlash(called)+"'\necho username=agent\necho password=stolen\n")
	if err := os.Chmod(helper, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.clone, "config", "--local", "credential.helper", "!sh '"+filepath.ToSlash(helper)+"'")
	return called
}

// Task 6.8 and the scenarios "Planted pre-push hook" and "Planted push
// URL" (ant-execution): hooks, settings and helpers an agent can write in
// the clone, the worktree or its branch neither see a credential nor
// redirect a push or a fetch, through a whole ant's life: prepare, status,
// fetch and push.
func TestPlantedSettingsGetNoCredentials(t *testing.T) {
	type plant func(t *testing.T, f *fixture, spy func(name string) string, evil string)
	setHook := func(name string) plant {
		return func(t *testing.T, f *fixture, spy func(string) string, _ string) {
			script := spy(name)
			writeFile(t, filepath.Join(f.clone, ".git", "hooks", name), readFile(t, script))
			if err := os.Chmod(filepath.Join(f.clone, ".git", "hooks", name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	tests := []struct {
		name  string
		plant plant
		// husky commits a pre-push hook to the ant's branch, under the
		// clone's core.hooksPath.
		husky bool
	}{
		{"pre-push hook in .git/hooks", setHook("pre-push"), false},
		{"post-checkout hook in .git/hooks", setHook("post-checkout"), false},
		{"reference-transaction hook in .git/hooks", setHook("reference-transaction"), false},
		{"committed husky pre-push hook", func(t *testing.T, f *fixture, _ func(string) string, _ string) {
			gitIn(t, f.clone, "config", "--local", "core.hooksPath", ".husky")
		}, true},
		{"core.fsmonitor", func(t *testing.T, f *fixture, spy func(string) string, _ string) {
			gitIn(t, f.clone, "config", "--local", "core.fsmonitor", filepath.ToSlash(spy("fsmonitor")))
		}, false},
		{"include.path", func(t *testing.T, f *fixture, spy func(string) string, evil string) {
			included := filepath.Join(f.tmp, "included.gitconfig")
			writeFile(t, included, "[core]\n\tfsmonitor = "+filepath.ToSlash(spy("included-fsmonitor"))+"\n"+
				"\thooksPath = "+filepath.ToSlash(filepath.Dir(spy("pre-push")))+"\n"+
				"[remote \"origin\"]\n\tpushurl = "+filepath.ToSlash(evil)+"\n"+
				"[url \""+filepath.ToSlash(evil)+"\"]\n\tinsteadOf = "+webURL+"\n")
			gitIn(t, f.clone, "config", "--local", "include.path", filepath.ToSlash(included))
		}, false},
		{"credential helper", func(t *testing.T, f *fixture, _ func(string) string, _ string) {
			plantedHelper(t, f)
		}, false},
		{"pushurl", func(t *testing.T, f *fixture, _ func(string) string, evil string) {
			gitIn(t, f.clone, "config", "--local", "remote.origin.pushurl", filepath.ToSlash(evil))
		}, false},
		{"pushInsteadOf", func(t *testing.T, f *fixture, _ func(string) string, evil string) {
			gitIn(t, f.clone, "config", "--local", "url."+filepath.ToSlash(evil)+".pushInsteadOf", webURL)
		}, false},
		{"insteadOf", func(t *testing.T, f *fixture, _ func(string) string, evil string) {
			gitIn(t, f.clone, "config", "--local", "url."+filepath.ToSlash(evil)+".insteadOf", webURL)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			server := newAuthServer(t)
			developerHelper := credentialHelper(t, f.tmp)
			t.Setenv("GH_TOKEN", canary)
			log := filepath.Join(f.tmp, "spy.log")
			spies := filepath.Join(f.tmp, "spies")
			spy := func(name string) string { return spyScript(t, spies, name, log, server.URL) }
			evil := filepath.Join(f.tmp, "evil.git")
			gitIn(t, f.tmp, "init", "--quiet", "--bare", evil)
			tt.plant(t, f, spy, evil)

			ant := f.prepare()
			if tt.husky {
				hook := filepath.Join(ant.Worktree, ".husky", "pre-push")
				writeFile(t, hook, readFile(t, spy("pre-push")))
				if err := os.Chmod(hook, 0o755); err != nil {
					t.Fatal(err)
				}
				agentGit(t, ant.Worktree, "add", "--chmod=+x", ".husky/pre-push")
				agentGit(t, ant.Worktree, "commit", "--quiet", "-m", "add husky hook")
			}
			if _, err := f.git.WorktreeStatus(f.ctx, f.c, ant.Worktree); err != nil {
				t.Fatal(err)
			}
			theirs := f.seedCommit("theirs.txt", "theirs\n", "someone else's work")
			gitIn(t, f.seed, "push", "--quiet", "origin", "main")
			if err := f.git.Fetch(f.ctx, f.c); err != nil {
				t.Fatalf("Fetch() = %v", err)
			}
			if got := gitIn(t, f.clone, "rev-parse", "refs/remotes/origin/main"); got != theirs {
				t.Errorf("after Fetch(), origin/main = %s, want the remote's %s", got, theirs)
			}
			// The agent commits, with its own environment.
			writeFile(t, filepath.Join(ant.Worktree, "a.txt"), "work\n")
			agentGit(t, ant.Worktree, "add", "a.txt")
			agentGit(t, ant.Worktree, "commit", "--quiet", "-m", "work")
			head := agentGit(t, ant.Worktree, "rev-parse", "HEAD")
			if out, err := f.push(ant); err != nil {
				t.Fatalf("Push() = %v\n%s", err, out)
			}

			if f.remoteTip(ant.Branch) != head {
				t.Error("the branch did not reach the recorded remote")
			}
			if out, _ := gitTry(evil, nil, "for-each-ref"); strings.TrimSpace(out) != "" {
				t.Errorf("the planted remote received refs:\n%s", out)
			}
			if exists(log) {
				for _, line := range strings.Split(strings.TrimSpace(readFile(t, log)), "\n") {
					if strings.Contains(line, canary) || !strings.Contains(line, "token=none") || strings.Contains(line, "helper.sh") {
						t.Errorf("a planted program saw a credential: %s", line)
					}
				}
			}
			if planted := filepath.Join(f.tmp, "planted-helper-called"); exists(planted) {
				t.Errorf("the planted credential helper was called: %s", readFile(t, planted))
			}
			if exists(developerHelper) {
				t.Errorf("the developer's credential helper was called for %s", readFile(t, developerHelper))
			}
			if _, authorized := server.counts(); authorized != 0 {
				t.Errorf("a planted program sent credentials %d times", authorized)
			}
		})
	}
}

// The spy really runs where git would run it, so the test above checks
// something: the repository's pre-push hook, committed or not, runs on
// Anthill's push, and the agent's own git status runs core.fsmonitor.
func TestSpiesRun(t *testing.T) {
	f := newFixture(t)
	server := newAuthServer(t)
	log := filepath.Join(f.tmp, "spy.log")
	spies := filepath.Join(f.tmp, "spies")
	gitIn(t, f.clone, "config", "--local", "core.fsmonitor", filepath.ToSlash(spyScript(t, spies, "fsmonitor", log, server.URL)))
	ant := f.prepare()
	gitIn(t, ant.Worktree, "status", "--porcelain")
	if !exists(log) || !strings.Contains(readFile(t, log), "fsmonitor") {
		t.Fatal("git status did not run core.fsmonitor")
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if _, err := f.git.WorktreeStatus(f.ctx, f.c, ant.Worktree); err != nil {
		t.Fatal(err)
	}
	if exists(log) {
		t.Errorf("WorktreeStatus() ran core.fsmonitor: %s", readFile(t, log))
	}
}

// Scenario "Git LFS files that need credentials" (ant-execution): a fake
// git-lfs on the PATH fails as the real one does without credentials.
func TestAddWorktreeLFSNeedsCredentials(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.seed, ".gitattributes"), "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	writeFile(t, filepath.Join(f.seed, "big.bin"), "version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("ab", 32)+"\nsize 12\n")
	gitIn(t, f.seed, "add", ".gitattributes", "big.bin")
	gitIn(t, f.seed, "commit", "--quiet", "-m", "add an LFS file")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main")

	bin := filepath.Join(f.tmp, "bin")
	writeFile(t, filepath.Join(bin, "git-lfs"), "#!/bin/sh\n"+
		"cat >/dev/null\n"+
		"echo 'Error downloading object: big.bin (abababa): Smudge error: Error downloading big.bin: batch response: Git credentials for https://github.com/acme/web.git not found.' >&2\n"+
		"exit 2\n")
	if err := os.Chmod(filepath.Join(bin, "git-lfs"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitIn(t, f.tmp, "config", "--global", "filter.lfs.smudge", "git-lfs smudge -- %f")
	gitIn(t, f.tmp, "config", "--global", "filter.lfs.required", "true")

	_, err := f.workspace().Prepare(f.ctx, f.request())
	if !errors.Is(err, ports.ErrUnsupported) || !strings.Contains(err.Error(), git.MessageLFSUnsupported) {
		t.Fatalf("Prepare() = %v, want ErrUnsupported with %q", err, git.MessageLFSUnsupported)
	}
	if strings.Contains(gitIn(t, f.clone, "worktree", "list", "--porcelain"), "ant/") {
		t.Error("the failed worktree is still registered")
	}
}

// A clone with no recorded URL cannot fetch or push: the clone's
// configuration is never asked for one.
func TestPushNeedsRecordedURL(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	c := f.c
	c.URL = ""
	if _, err := f.git.Push(f.ctx, c, ant.Worktree, ant.Branch, trailers.Owner()); err == nil {
		t.Error("Push() without a recorded URL succeeded")
	}
	if err := f.git.Fetch(f.ctx, c); err == nil {
		t.Error("Fetch() without a recorded URL succeeded")
	}
}

// The transfer repositories are removed after use.
func TestTransferRepositoriesRemoved(t *testing.T) {
	f := newFixture(t)
	ant := f.prepare()
	f.commitIn(ant.Worktree, "a.txt", "work")
	if out, err := f.push(ant); err != nil {
		t.Fatalf("Push() = %v\n%s", err, out)
	}
	entries, err := os.ReadDir(filepath.Join(f.home, git.TransferDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "no-hooks" {
			t.Errorf("left in the transfer folder: %s", e.Name())
		}
	}
}

// A hook planted in Anthill's empty hooks directory stops every git
// command instead of running, also Fetch, which holds credentials.
func TestPlantedHookInNoHooksRefused(t *testing.T) {
	f := newFixture(t)
	t.Setenv("GH_TOKEN", canary)
	ran := filepath.Join(f.tmp, "no-hooks-ran")
	noHooks := filepath.Join(f.home, git.TransferDirName, "no-hooks")
	hook := filepath.Join(noHooks, "reference-transaction")
	writeFile(t, hook, "#!/bin/sh\necho \"token=${GH_TOKEN:-none}\" >> '"+filepath.ToSlash(ran)+"'\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	f.seedCommit("theirs.txt", "theirs\n", "someone else's work")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main")
	before := gitIn(t, f.clone, "rev-parse", "refs/remotes/origin/main")

	if err := f.git.Fetch(f.ctx, f.c); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("Fetch() with a planted hook = %v", err)
	}
	if err := f.git.CreateBranch(f.ctx, f.c, "ant/x", "main"); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("CreateBranch() with a planted hook = %v", err)
	}
	if exists(ran) {
		t.Errorf("the planted hook ran: %s", readFile(t, ran))
	}
	if got := gitIn(t, f.clone, "rev-parse", "refs/remotes/origin/main"); got != before {
		t.Error("Fetch() updated the clone")
	}
	if strings.Contains(gitIn(t, f.clone, "branch", "--list", "ant/x"), "ant/x") {
		t.Error("the branch was created")
	}
	if !exists(hook) {
		t.Error("the planted hook was removed; it should be left for the developer to see")
	}
}

// The empty hooks directory is made owner-only again before each use,
// and a symbolic link in its place is refused.
func TestNoHooksDirectoryChecked(t *testing.T) {
	f := newFixture(t)
	noHooks := filepath.Join(f.home, git.TransferDirName, "no-hooks")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(noHooks, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := f.git.Fetch(f.ctx, f.c); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(noHooks); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("no-hooks mode = %v, %v; want 0700", info.Mode().Perm(), err)
		}
	}
	target := filepath.Join(f.tmp, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(noHooks); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, noHooks); err != nil {
		t.Skipf("cannot create a symbolic link: %v", err)
	}
	if err := f.git.Fetch(f.ctx, f.c); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("Fetch() with no-hooks a symbolic link = %v", err)
	}
}
