package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// AddWorktree runs git worktree add, then gives the worktree its own
// configuration: Anthill's hooks, the trailer values and
// domain.AgentGitConfig. On failure it removes the worktree again.
func (g *Git) AddWorktree(ctx context.Context, c ports.Clone, path, branch string, trailers domain.Trailers) error {
	if err := trailers.Validate(); err != nil {
		return err
	}
	if sha, err := g.resolve(ctx, c.Path, localRef(branch)); err != nil {
		return err
	} else if sha == "" {
		return fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if r, err := g.run(ctx, c.Path, "worktree", "add", "--quiet", path, branch); err != nil {
		// A checkout that failed leaves the worktree behind.
		if ok, _ := g.worktreeRegistered(context.WithoutCancel(ctx), c, path); ok {
			_, _ = g.run(context.WithoutCancel(ctx), c.Path, "worktree", "remove", "--force", path)
		}
		if lfsFailed(r.stderr) {
			return fmt.Errorf("%w: %s: %w", ports.ErrUnsupported, MessageLFSUnsupported, err)
		}
		return err
	}
	if err := g.setUpWorktree(ctx, c, path, trailers); err != nil {
		_, _ = g.run(context.WithoutCancel(ctx), c.Path, "worktree", "remove", "--force", path)
		return fmt.Errorf("setting up worktree %s: %w", path, err)
	}
	return nil
}

// MessageLFSUnsupported explains why a worktree could not be created
// when its Git LFS files failed to download: the checkout runs without
// credentials (design D4).
const MessageLFSUnsupported = "repositories with Git LFS files that need credentials to download are not supported yet"

// lfsFailed reports whether git's output says the Git LFS filter failed.
// Git prints "smudge filter lfs failed" when the filter is required, as
// git lfs install sets it; git-lfs's own messages name Git LFS or
// git-lfs.
func lfsFailed(stderr string) bool {
	return strings.Contains(stderr, "smudge filter lfs failed") ||
		strings.Contains(stderr, "external filter 'git-lfs") ||
		strings.Contains(stderr, "git-lfs filter-process")
}

// AddDetachedWorktree checks out commit, detached, in a new worktree at
// path, for a shepherd round (design D9). A commit the clone lacks, such
// as the head of a PR from a fork, is fetched by its hash first through a
// transfer repository (design D4). The worktree gets Anthill's hooks, with
// the pre-push hook that refuses, and no commit trailers.
func (g *Git) AddDetachedWorktree(ctx context.Context, c ports.Clone, path, commit string) error {
	if !isHash(commit) {
		return fmt.Errorf("commit %q is not a full commit hash", commit)
	}
	sha, err := g.resolve(ctx, c.Path, commit)
	if err != nil {
		return err
	}
	if sha == "" {
		if err := g.fetchCommit(ctx, c, commit); err != nil {
			return fmt.Errorf("fetching commit %s: %w", commit, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if r, err := g.run(ctx, c.Path, "worktree", "add", "--quiet", "--detach", path, commit); err != nil {
		if ok, _ := g.worktreeRegistered(context.WithoutCancel(ctx), c, path); ok {
			_, _ = g.run(context.WithoutCancel(ctx), c.Path, "worktree", "remove", "--force", path)
		}
		if lfsFailed(r.stderr) {
			return fmt.Errorf("%w: %s: %w", ports.ErrUnsupported, MessageLFSUnsupported, err)
		}
		return err
	}
	if err := g.setUpHooks(ctx, c, path, nil); err != nil {
		_, _ = g.run(context.WithoutCancel(ctx), c.Path, "worktree", "remove", "--force", path)
		return fmt.Errorf("setting up worktree %s: %w", path, err)
	}
	return nil
}

// fetchCommit fetches one commit by its hash from the remote into the
// clone. The clone keeps no ref to it: the worktree checked out at it
// keeps it from garbage collection.
func (g *Git) fetchCommit(ctx context.Context, c ports.Clone, commit string) error {
	t, err := g.openTransfer(ctx, c)
	if err != nil {
		return err
	}
	defer t.close()
	ref := "refs/anthill/commit/" + commit
	if err := t.fetch(ctx, "+"+commit+":"+ref); err != nil {
		return err
	}
	if err := t.toClone(ctx, false, "+"+ref+":"+ref); err != nil {
		return err
	}
	_, err = g.run(ctx, c.Path, "update-ref", "-d", ref)
	return err
}

// isHash reports whether s is a full SHA-1 or SHA-256 commit hash.
func isHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (g *Git) setUpWorktree(ctx context.Context, c ports.Clone, path string, trailers domain.Trailers) error {
	return g.setUpHooks(ctx, c, path, [][2]string{
		{configItem, trailers.Item},
		{configAgent, trailers.Agent},
		{configInstance, trailers.Instance},
	})
}

// setUpHooks installs Anthill's hooks in a worktree and writes its
// worktree settings: the hooks path, the agent's git settings and extra.
func (g *Git) setUpHooks(ctx context.Context, c ports.Clone, path string, extra [][2]string) error {
	// The one setting Anthill writes to the clone's .git/config: it lets
	// git config --worktree write to this worktree's own file.
	enabled, err := g.output(ctx, c.Path, "config", "--local", "--type=bool", "--get", "extensions.worktreeConfig")
	if err != nil && exitCode(err) != 1 {
		return err
	}
	if enabled != "true" {
		if _, err := g.run(ctx, c.Path, "config", "--local", "extensions.worktreeConfig", "true"); err != nil {
			return err
		}
	}
	gitDir, err := g.output(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	hooks := filepath.Join(filepath.FromSlash(gitDir), hooksDirName)
	if err := writeHooks(hooks); err != nil {
		return err
	}
	settings := append([][2]string{{"core.hooksPath", filepath.ToSlash(hooks)}}, extra...)
	for _, kv := range append(settings, domain.AgentGitConfig...) {
		if _, err := g.run(ctx, path, "config", "--worktree", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// worktreeRegistered reports whether path is a worktree of the clone.
func (g *Git) worktreeRegistered(ctx context.Context, c ports.Clone, path string) (bool, error) {
	out, err := g.output(ctx, c.Path, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok && sameFile(path, p) {
			return true, nil
		}
	}
	return false, nil
}

// WorktreeStatus reads the worktree's branch and whether git status
// reports anything, untracked files included.
func (g *Git) WorktreeStatus(ctx context.Context, c ports.Clone, path string) (ports.WorktreeStatus, error) {
	ok, err := g.worktreeRegistered(ctx, c, path)
	if err != nil {
		return ports.WorktreeStatus{}, err
	}
	if !ok {
		return ports.WorktreeStatus{}, fmt.Errorf("worktree %s: %w", path, ports.ErrNotFound)
	}
	var st ports.WorktreeStatus
	ref, err := g.output(ctx, path, "symbolic-ref", "--quiet", "HEAD")
	switch {
	case err == nil:
		st.Branch = strings.TrimPrefix(ref, "refs/heads/")
	case exitCode(err) != 1: // 1 means a detached HEAD
		return ports.WorktreeStatus{}, err
	}
	out, err := g.output(ctx, path, "status", "--porcelain")
	if err != nil {
		return ports.WorktreeStatus{}, err
	}
	st.Dirty = out != ""
	return st, nil
}

// removeAttempts and removeBackoff bound the retries of git worktree
// remove on Windows, where a file another process still has open, such
// as an editor or a virus scanner, cannot be deleted for a moment.
const (
	removeAttempts = 5
	removeBackoff  = 200 * time.Millisecond
)

// RemoveWorktree runs git worktree remove, with --force when asked, then
// git worktree prune. On Windows it retries a few times.
func (g *Git) RemoveWorktree(ctx context.Context, c ports.Clone, path string, force bool) error {
	ok, err := g.worktreeRegistered(ctx, c, path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("worktree %s: %w", path, ports.ErrNotFound)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	for attempt := 1; ; attempt++ {
		_, err = g.run(ctx, c.Path, args...)
		if err == nil || runtime.GOOS != "windows" || attempt == removeAttempts || errors.Is(err, ctx.Err()) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * removeBackoff):
		}
	}
	if err != nil {
		return err
	}
	_, err = g.run(ctx, c.Path, "worktree", "prune")
	return err
}
