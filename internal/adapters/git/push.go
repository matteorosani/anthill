package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Push runs git push from the clone, not the worktree, so it uses the
// developer's own credentials and not the worktree's settings that block
// agents. The refspec has no +, so the remote refuses anything but a
// fast-forward.
func (g *Git) Push(ctx context.Context, c ports.Clone, branch string, owner domain.BranchOwner) (string, error) {
	if sha, err := g.resolve(ctx, c.Path, localRef(branch)); err != nil {
		return "", err
	} else if sha == "" {
		return "", fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	ref := localRef(branch)
	r, err := g.run(ctx, c.Path, "push", "--porcelain", c.Remote, ref+":"+ref)
	out := strings.TrimSpace(r.stdout + "\n" + r.stderr)
	if err == nil {
		return out, nil
	}
	if !rejected(r.stdout) {
		return out, err
	}
	// Fetch the remote branch, so the ant can merge it, and look at what
	// the push would have dropped.
	if _, err := g.run(ctx, c.Path, "fetch", "--quiet", c.Remote, "+"+ref+":"+remoteRef(c, branch)); err != nil {
		return out, err
	}
	trailers, err := g.output(ctx, c.Path, "log", "--format=%x1e%(trailers:only,unfold)", remoteRef(c, branch), "--not", ref)
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
