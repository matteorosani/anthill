package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matteorosani/anthill/internal/ports"
)

// transfer is a transfer repository (design D4): a short-lived bare
// repository in Anthill's data folder that fetches and pushes for a
// clone. It reads the clone's objects through objects/info/alternates and
// has no configuration of its own, so only the system and global
// configuration apply to it, the developer's credential helper included.
// Nothing an agent writes in the clone, such as a hook, a pushurl, an
// insteadOf rule or a credential helper, reaches the commands that hold
// credentials. Fetched objects and refs reach the clone through a local
// fetch from the transfer repository, without credentials.
type transfer struct {
	g   *Git
	c   ports.Clone
	dir string
}

// transferOptions apply to every command in a transfer repository: no
// automatic garbage collection or maintenance, since the repository is
// removed straight after, and no git for-each-ref run in the clone to list
// the alternate's refs, since it would read the clone's configuration
// with credentials. openTransfer copies those refs instead.
var transferOptions = []string{
	"-c", "gc.auto=0",
	"-c", "maintenance.auto=false",
	"-c", "core.alternateRefsCommand=true",
}

// openTransfer creates a transfer repository for the clone c. The caller
// must close it.
func (g *Git) openTransfer(ctx context.Context, c ports.Clone) (*transfer, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("the clone %s has no checked remote URL", c.Path)
	}
	common, err := g.output(ctx, c.Path, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	objects := filepath.Join(absolute(c.Path, common), "objects")
	// The commits the clone already has from the remote, which the
	// transfer repository offers the remote when fetching, so that the
	// remote sends only what is new.
	tips, err := g.output(ctx, c.Path, "for-each-ref", "--format=%(objectname)", "refs/remotes/"+c.Remote+"/")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(g.transferRoot, 0o700); err != nil {
		return nil, fmt.Errorf("creating the transfer folder: %w", err)
	}
	dir, err := os.MkdirTemp(g.transferRoot, "repo-")
	if err != nil {
		return nil, fmt.Errorf("creating a transfer repository: %w", err)
	}
	t := &transfer{g: g, c: c, dir: dir}
	if err := t.init(ctx, objects, tips); err != nil {
		t.close()
		return nil, fmt.Errorf("creating a transfer repository: %w", err)
	}
	return t, nil
}

func (t *transfer) init(ctx context.Context, objects, tips string) error {
	// An empty --template copies no hooks from the developer's template
	// directory.
	if _, err := t.g.run(ctx, t.dir, "init", "--quiet", "--bare", "--template=", "."); err != nil {
		return err
	}
	info := filepath.Join(t.dir, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.ToSlash(objects)+"\n"), 0o600); err != nil {
		return err
	}
	var seen []string
	var stdin strings.Builder
	for _, sha := range strings.Fields(tips) {
		if !slices.Contains(seen, sha) {
			fmt.Fprintf(&stdin, "create refs/haves/%d %s\n", len(seen), sha)
			seen = append(seen, sha)
		}
	}
	if len(seen) == 0 {
		return nil
	}
	_, err := t.g.runIn(ctx, agentEnv, t.dir, strings.NewReader(stdin.String()), "update-ref", "--stdin")
	return err
}

// close removes the transfer repository.
func (t *transfer) close() {
	_ = os.RemoveAll(t.dir)
}

// remote runs a git command that talks to the remote, with Anthill's
// environment and hooks off, in the transfer repository.
func (t *transfer) remote(ctx context.Context, args ...string) (result, error) {
	return t.g.runWith(ctx, anthillEnv, t.dir, nil, transferOptions, args...)
}

// fetch fetches refspecs from the remote URL into the transfer
// repository.
func (t *transfer) fetch(ctx context.Context, refspecs ...string) error {
	args := append([]string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", t.c.URL}, refspecs...)
	_, err := t.remote(ctx, args...)
	return err
}

// toClone fetches refspecs from the transfer repository into the clone,
// with the agent's environment: a local fetch that needs no credentials.
func (t *transfer) toClone(ctx context.Context, prune bool, refspecs ...string) error {
	args := []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules"}
	if prune {
		args = append(args, "--prune")
	}
	args = append(append(args, t.dir), refspecs...)
	_, err := t.g.run(ctx, t.c.Path, args...)
	return err
}

// remoteTip asks the remote for the commit of branch, or "" when the
// remote has no such branch.
func (t *transfer) remoteTip(ctx context.Context, branch string) (string, error) {
	r, err := t.remote(ctx, "ls-remote", "--heads", t.c.URL, localRef(branch))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(r.stdout, "\n") {
		if sha, ref, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t"); ok && ref == localRef(branch) {
			return sha, nil
		}
	}
	return "", nil
}
