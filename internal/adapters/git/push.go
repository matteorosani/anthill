package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Push runs the repository's pre-push hook itself (runPrePush), then
// pushes the branch's commit to the remote URL from a transfer
// repository, with hooks off. Neither reads the clone's configuration
// while holding credentials (design D4). The refspec has no +, so the
// remote refuses anything but a fast-forward.
func (g *Git) Push(ctx context.Context, c ports.Clone, worktree, branch string, owner domain.BranchOwner) (string, error) {
	sha, err := g.resolve(ctx, c.Path, localRef(branch))
	if err != nil {
		return "", err
	} else if sha == "" {
		return "", fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	dir := worktree
	if dir == "" {
		dir = c.Path
	}
	t, err := g.openTransfer(ctx, c)
	if err != nil {
		return "", err
	}
	defer t.close()

	hookOut, err := g.runPrePush(ctx, t, dir, branch, sha)
	if err != nil {
		return hookOut, err
	}
	ref := localRef(branch)
	r, err := t.remote(ctx, "push", "--porcelain", "--no-verify", c.URL, sha+":"+ref)
	out := g.redactor.Redact(strings.TrimSpace(strings.Join([]string{hookOut, r.stdout, r.stderr}, "\n")))
	if err == nil {
		// As git push would, update the remote-tracking branch.
		_, err = g.run(ctx, c.Path, "update-ref", remoteRef(c, branch), sha)
		return out, err
	}
	if !rejected(r.stdout) {
		return out, err
	}
	// Fetch the remote branch, so the ant can merge it, and look at what
	// the push would have dropped.
	if err := t.fetch(ctx, "+"+ref+":"+ref); err != nil {
		return out, err
	}
	if err := t.toClone(ctx, false, "+"+ref+":"+remoteRef(c, branch)); err != nil {
		return out, err
	}
	trailers, err := g.output(ctx, c.Path, "log", "--format=%x1e%(trailers:only,unfold)", remoteRef(c, branch), "--not", sha)
	if err != nil {
		return out, err
	}
	for _, block := range strings.Split(trailers, "\x1e") {
		if parseOwner(block) == owner {
			return out, fmt.Errorf("push %s: %w", branch, ports.ErrHistoryRewritten)
		}
	}
	return out, fmt.Errorf("push %s: %w", branch, ports.ErrConflict)
}

// ErrPrePushHook is returned, wrapped, when the repository's pre-push
// hook refuses a push.
var ErrPrePushHook = errors.New("the repository's pre-push hook refused the push")

// runHookAlias is a shell alias that runs its first argument with the
// others. Git runs it with its own sh, which on Windows is the one that
// comes with Git for Windows, so a hook runs as git would run it.
const runHookAlias = `alias.anthill-run-hook=!f() { exec "$@"; }; f`

// runPrePush runs the repository's pre-push hook, if any, in dir with the
// agent's environment, as git push would: with the remote's name and URL
// as arguments, and one line on its input with the local ref and commit
// and the remote ref and its current commit (zeros when the remote has no
// such branch). It returns the hook's output. A hook that fails refuses
// the push with ErrPrePushHook.
func (g *Git) runPrePush(ctx context.Context, t *transfer, dir, branch, sha string) (string, error) {
	hooks, err := g.repoHooksDir(ctx, t.c, dir)
	if err != nil {
		return "", fmt.Errorf("finding the repository's hooks: %w", err)
	}
	hook := filepath.Join(hooks, "pre-push")
	info, err := os.Stat(hook)
	// Like git, skip a missing hook and, except on Windows where git does
	// not look at it, one that is not executable.
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		return "", nil
	}
	remoteSHA, err := t.remoteTip(ctx, branch)
	if err != nil {
		return "", err
	}
	if remoteSHA == "" {
		remoteSHA = strings.Repeat("0", len(sha))
	}
	ref := localRef(branch)
	stdin := fmt.Sprintf("%s %s %s %s\n", ref, sha, ref, remoteSHA)
	r, err := g.runIn(ctx, agentEnv, dir, strings.NewReader(stdin),
		"-c", runHookAlias, "anthill-run-hook", filepath.ToSlash(hook), t.c.Remote, t.c.URL)
	out := strings.TrimSpace(r.stdout + "\n" + r.stderr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out, ctxErr
		}
		return g.redactor.Redact(out), fmt.Errorf("push %s: %w", branch, ErrPrePushHook)
	}
	return out, nil
}

// repoHooksDir finds the repository's own hooks directory as git would
// without Anthill's worktree setting, and as repo_hooks does in the
// worktree hooks: core.hooksPath from the clone's system, global and
// local configuration, else the common .git/hooks. A relative path is
// relative to top, the working tree where the hook runs, so husky's
// .husky works.
func (g *Git) repoHooksDir(ctx context.Context, c ports.Clone, top string) (string, error) {
	dir := ""
	for _, layer := range []string{"--system", "--global", "--local"} {
		out, err := g.output(ctx, c.Path, "config", layer, "--includes", "--type=path", "--get", "core.hooksPath")
		if err != nil && exitCode(err) != 1 {
			return "", err
		}
		if out != "" {
			dir = out
		}
	}
	if dir == "" {
		common, err := g.output(ctx, c.Path, "rev-parse", "--git-common-dir")
		if err != nil {
			return "", err
		}
		return filepath.Join(absolute(c.Path, common), "hooks"), nil
	}
	return absolute(top, dir), nil
}

// rejected reports whether git push --porcelain printed a rejected ref.
func rejected(porcelain string) bool {
	for _, line := range strings.Split(porcelain, "\n") {
		if strings.HasPrefix(line, "!") && strings.Contains(line, "[rejected]") {
			return true
		}
	}
	return false
}

// commitLog runs git log with args and reads hash and subject pairs,
// oldest first.
func (g *Git) commitLog(ctx context.Context, dir string, args ...string) ([]ports.Commit, error) {
	out, err := g.output(ctx, dir, append([]string{"log", "--reverse", "--format=%H%x1f%s"}, args...)...)
	if err != nil {
		return nil, err
	}
	var commits []ports.Commit
	for _, line := range strings.Split(out, "\n") {
		if hash, subject, ok := strings.Cut(line, "\x1f"); ok {
			commits = append(commits, ports.Commit{Hash: hash, Subject: subject})
		}
	}
	return commits, nil
}

// NewCommits lists the commits of the worktree's HEAD after the fetched
// base branch.
func (g *Git) NewCommits(ctx context.Context, c ports.Clone, worktree, base string) ([]ports.Commit, error) {
	if sha, err := g.resolve(ctx, c.Path, remoteRef(c, base)); err != nil {
		return nil, err
	} else if sha == "" {
		return nil, fmt.Errorf("remote branch %s/%s: %w", c.Remote, base, ports.ErrNotFound)
	}
	return g.commitLog(ctx, worktree, "HEAD", "--not", remoteRef(c, base))
}

// Unpushed lists the commits of a local branch that no remote-tracking
// branch of the clone's remote has, and that are not pushed or before it.
func (g *Git) Unpushed(ctx context.Context, c ports.Clone, branch, pushed string) ([]ports.Commit, error) {
	if sha, err := g.resolve(ctx, c.Path, localRef(branch)); err != nil {
		return nil, err
	} else if sha == "" {
		return nil, fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	args := []string{localRef(branch), "--not", "--remotes=" + c.Remote}
	if pushed != "" {
		// A commit the clone does not have cannot hide anything.
		if sha, err := g.resolve(ctx, c.Path, pushed); err != nil {
			return nil, err
		} else if sha != "" {
			args = append(args, sha)
		}
	}
	return g.commitLog(ctx, c.Path, args...)
}
