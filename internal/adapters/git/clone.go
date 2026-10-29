package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// sameFile reports whether two paths name the same file or directory.
// Comparing with os.SameFile, rather than as strings, copes with symbolic
// links (macOS's /var is /private/var), case-insensitive file systems and
// the C:/ paths git prints on Windows.
func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(filepath.FromSlash(b))
	return err == nil && os.SameFile(sa, sb)
}

// absolute resolves a path git printed, which may be relative to dir.
func absolute(dir, p string) string {
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// OpenClone checks that path is the top of a git clone, not a linked
// worktree, with a remote whose configured URL points to repo. It prefers
// a matching remote named origin. The URL is read as configured, before
// any url.<base>.insteadOf rewriting.
func (g *Git) OpenClone(ctx context.Context, path string, repo domain.Repository) (ports.Clone, error) {
	noClone := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ports.ErrNoClone, fmt.Sprintf(format, args...))
	}
	if !filepath.IsAbs(path) {
		return ports.Clone{}, noClone("%s is not an absolute path", path)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ports.Clone{}, noClone("%s is not a directory", path)
	}
	out, err := g.output(ctx, path, "rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || exitCode(err) < 0 {
			return ports.Clone{}, err
		}
		return ports.Clone{}, noClone("%s is not a git working copy", path)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || !sameFile(path, lines[0]) {
		return ports.Clone{}, noClone("%s is not the top of a git working copy", path)
	}
	if !sameFile(absolute(path, lines[1]), absolute(path, lines[2])) {
		return ports.Clone{}, noClone("%s is a linked worktree, not the clone", path)
	}

	out, err = g.output(ctx, path, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil && exitCode(err) != 1 {
		return ports.Clone{}, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		key, url, ok := strings.Cut(line, " ")
		if !ok || !repo.MatchesRemoteURL(url) {
			continue
		}
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url"))
	}
	switch {
	case len(names) == 0:
		return ports.Clone{}, noClone("%s has no remote for %s", path, repo)
	case slices.Contains(names, "origin"):
		return ports.Clone{Path: path, Remote: "origin"}, nil
	default:
		return ports.Clone{Path: path, Remote: names[0]}, nil
	}
}

// Fetch runs git fetch --prune on the clone's remote. It updates only
// remote-tracking branches.
func (g *Git) Fetch(ctx context.Context, c ports.Clone) error {
	_, err := g.run(ctx, c.Path, "fetch", "--prune", "--quiet", c.Remote)
	return err
}

// RemoteDefaultBranch reads <remote>/HEAD, or asks the remote when the
// clone does not know it.
func (g *Git) RemoteDefaultBranch(ctx context.Context, c ports.Clone) (string, error) {
	prefix := "refs/remotes/" + c.Remote + "/"
	ref, err := g.output(ctx, c.Path, "symbolic-ref", "--quiet", prefix+"HEAD")
	if err == nil && strings.HasPrefix(ref, prefix) {
		return strings.TrimPrefix(ref, prefix), nil
	}
	out, err := g.output(ctx, c.Path, "ls-remote", "--symref", c.Remote, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if target, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			name, _, _ := strings.Cut(target, "\t")
			return name, nil
		}
	}
	return "", fmt.Errorf("the remote %s reports no default branch: %w", c.Remote, ports.ErrNotFound)
}

func localRef(branch string) string { return "refs/heads/" + branch }

func remoteRef(c ports.Clone, branch string) string {
	return "refs/remotes/" + c.Remote + "/" + branch
}

// resolve returns the commit a ref points to, or "" when there is none.
func (g *Git) resolve(ctx context.Context, dir, ref string) (string, error) {
	out, err := g.output(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if exitCode(err) == 1 {
		return "", nil
	}
	return out, err
}

// Branch looks up the local branch and the fetched remote branch, and
// reads the owners from the trailers of their commits after base.
func (g *Git) Branch(ctx context.Context, c ports.Clone, name, base string) (ports.BranchInfo, error) {
	local, err := g.resolve(ctx, c.Path, localRef(name))
	if err != nil {
		return ports.BranchInfo{}, err
	}
	remote, err := g.resolve(ctx, c.Path, remoteRef(c, name))
	if err != nil {
		return ports.BranchInfo{}, err
	}
	if local == "" && remote == "" {
		return ports.BranchInfo{}, fmt.Errorf("branch %s: %w", name, ports.ErrNotFound)
	}
	info := ports.BranchInfo{Name: name, Commit: local, Local: local != "", Remote: remote != ""}
	if local == "" {
		info.Commit = remote
	}
	if base == "" {
		return info, nil
	}
	args := []string{"log", "--format=%x1e%(trailers:only,unfold)"}
	for _, tip := range []string{local, remote} {
		if tip != "" {
			args = append(args, tip)
		}
	}
	if b, err := g.resolve(ctx, c.Path, remoteRef(c, base)); err != nil {
		return ports.BranchInfo{}, err
	} else if b != "" {
		args = append(args, "--not", b)
	}
	out, err := g.output(ctx, c.Path, args...)
	if err != nil {
		return ports.BranchInfo{}, err
	}
	for _, block := range strings.Split(out, "\x1e") {
		owner := parseOwner(block)
		if owner.Item != "" && owner.Instance != "" && !slices.Contains(info.Owners, owner) {
			info.Owners = append(info.Owners, owner)
		}
	}
	return info, nil
}

// parseOwner reads the Anthill-Item and Anthill-Instance trailers from
// the trailer lines of one commit. Trailer keys ignore case, as in git.
func parseOwner(trailers string) domain.BranchOwner {
	var o domain.BranchOwner
	for _, line := range strings.Split(trailers, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch {
		case strings.EqualFold(strings.TrimSpace(key), domain.TrailerItem):
			o.Item = value
		case strings.EqualFold(strings.TrimSpace(key), domain.TrailerInstance):
			o.Instance = value
		}
	}
	return o
}

// CreateBranch runs git branch --no-track name <remote>/from. git branch
// creates the ref without checking anything out.
func (g *Git) CreateBranch(ctx context.Context, c ports.Clone, name, from string) error {
	start, err := g.resolve(ctx, c.Path, remoteRef(c, from))
	if err != nil {
		return err
	}
	if start == "" {
		return fmt.Errorf("remote branch %s/%s: %w", c.Remote, from, ports.ErrNotFound)
	}
	_, err = g.run(ctx, c.Path, "branch", "--no-track", name, remoteRef(c, from))
	return err
}

// DeleteBranch deletes a local branch with git branch -D, or a remote one
// with git push --delete.
func (g *Git) DeleteBranch(ctx context.Context, c ports.Clone, name string, remote bool) error {
	if !remote {
		sha, err := g.resolve(ctx, c.Path, localRef(name))
		if err != nil {
			return err
		}
		if sha == "" {
			return fmt.Errorf("branch %s: %w", name, ports.ErrNotFound)
		}
		_, err = g.run(ctx, c.Path, "branch", "-D", name)
		return err
	}
	notFound := fmt.Errorf("remote branch %s: %w", name, ports.ErrNotFound)
	// Some servers accept deleting a branch that does not exist, so ask
	// first; ls-remote --exit-code exits with 2 when nothing matches.
	if _, err := g.run(ctx, c.Path, "ls-remote", "--exit-code", "--heads", c.Remote, localRef(name)); exitCode(err) == 2 {
		return notFound
	} else if err != nil {
		return err
	}
	r, err := g.run(ctx, c.Path, "push", "--quiet", c.Remote, "--delete", localRef(name))
	if err != nil && strings.Contains(r.stderr, "remote ref does not exist") {
		return notFound
	}
	return err
}
