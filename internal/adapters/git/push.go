package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Push runs git push from the ant's worktree, so that the repository's
// pre-push hook checks the ant's code, with what the worktree's own
// configuration takes away from agents given back for this one command:
// the repository's hooks, the developer's credential helpers, askpass
// program and ssh command (restoredConfig). It runs with Anthill's own
// environment, never the agent's. An empty worktree pushes from the
// clone. The refspec has no +, so the remote refuses anything but a
// fast-forward.
func (g *Git) Push(ctx context.Context, c ports.Clone, worktree, branch string, owner domain.BranchOwner) (string, error) {
	if sha, err := g.resolve(ctx, c.Path, localRef(branch)); err != nil {
		return "", err
	} else if sha == "" {
		return "", fmt.Errorf("branch %s: %w", branch, ports.ErrNotFound)
	}
	dir := worktree
	if dir == "" {
		dir = c.Path
	}
	overrides, err := g.restoredConfig(ctx, c)
	if err != nil {
		return "", fmt.Errorf("reading the developer's git configuration: %w", err)
	}
	ref := localRef(branch)
	r, err := g.run(ctx, dir, append(overrides, "push", "--porcelain", c.Remote, ref+":"+ref)...)
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

// configLayers are the configuration files git reads before a
// worktree's own, in git's order: the later wins or appends.
var configLayers = []string{"--system", "--global", "--local"}

// restoredConfig returns the -c options that undo, for one command run
// in an ant worktree, the settings that block agents (setUpWorktree and
// domain.AgentGitConfig). It reads the clone's system, global and local
// configuration, not the worktree's:
//   - core.hooksPath: the repository's own hooks, as repo_hooks finds
//     them. A relative path stays relative, to the worktree where git
//     runs the hook;
//   - credential helpers: an empty credential.helper clears the list,
//     then every credential.helper and credential.<url>.helper entry is
//     added again in git's order;
//   - core.askPass and core.sshCommand: the developer's values, or for
//     ssh the default ssh, or GIT_SSH when that is how the developer set
//     it. A GIT_SSH_COMMAND in the environment wins over both anyway.
func (g *Git) restoredConfig(ctx context.Context, c ports.Clone) ([]string, error) {
	var opts []string
	set := func(key, value string) { opts = append(opts, "-c", key+"="+value) }

	hooks, err := g.lastValue(ctx, c, "core.hooksPath")
	if err != nil {
		return nil, err
	}
	if hooks == "" {
		common, err := g.output(ctx, c.Path, "rev-parse", "--git-common-dir")
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(common) {
			common = filepath.Join(c.Path, common)
		}
		hooks = filepath.ToSlash(filepath.Join(common, "hooks"))
	}
	set("core.hooksPath", hooks)

	set("credential.helper", "")
	for _, layer := range configLayers {
		out, err := g.configGet(ctx, c, layer, "--get-regexp", `^credential\..*helper$`)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if key, value, ok := strings.Cut(line, " "); ok {
				set(key, value)
			} else if line != "" {
				set(line, "")
			}
		}
	}

	askPass, err := g.lastValue(ctx, c, "core.askPass")
	if err != nil {
		return nil, err
	}
	set("core.askPass", askPass)
	ssh, err := g.lastValue(ctx, c, "core.sshCommand")
	if err != nil {
		return nil, err
	}
	if ssh == "" {
		ssh = "ssh"
		if prog := os.Getenv("GIT_SSH"); prog != "" {
			ssh = "'" + strings.ReplaceAll(prog, "'", `'\''`) + "'"
		}
	}
	set("core.sshCommand", ssh)
	return opts, nil
}

// lastValue is the value of key that git would use from the clone's
// system, global and local configuration, or "" when none sets it.
func (g *Git) lastValue(ctx context.Context, c ports.Clone, key string) (string, error) {
	value := ""
	for _, layer := range configLayers {
		out, err := g.configGet(ctx, c, layer, "--get", key)
		if err != nil {
			return "", err
		}
		if out != "" {
			value = out
		}
	}
	return value, nil
}

// configGet runs git config on one layer, with includes, and returns its
// output, or "" when nothing matches.
func (g *Git) configGet(ctx context.Context, c ports.Clone, layer string, args ...string) (string, error) {
	out, err := g.output(ctx, c.Path, append([]string{"config", layer, "--includes"}, args...)...)
	if err != nil && exitCode(err) == 1 {
		return "", nil
	}
	return out, err
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
