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
	if _, err := g.run(ctx, c.Path, "worktree", "add", "--quiet", path, branch); err != nil {
		return err
	}
	if err := g.setUpWorktree(ctx, c, path, trailers); err != nil {
		_, _ = g.run(context.WithoutCancel(ctx), c.Path, "worktree", "remove", "--force", path)
		return fmt.Errorf("setting up worktree %s: %w", path, err)
	}
	return nil
}

func (g *Git) setUpWorktree(ctx context.Context, c ports.Clone, path string, trailers domain.Trailers) error {
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
	settings := [][2]string{
		{"core.hooksPath", filepath.ToSlash(hooks)},
		{configItem, trailers.Item},
		{configAgent, trailers.Agent},
		{configInstance, trailers.Instance},
	}
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
