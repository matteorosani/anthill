package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/matteorosani/anthill/internal/ports"
)

// PushTo pushes the HEAD of worktree to a branch of the clone's remote or
// of a fork, as Push does: the pre-push hook first, then a push from a
// transfer repository with hooks off. The refspec has no +, so the remote
// refuses anything but a fast-forward. A fork's URL is used only when it
// points to the fork's repository, so a URL from the provider cannot
// send the developer's credentials anywhere else (design D4).
func (g *Git) PushTo(ctx context.Context, c ports.Clone, worktree string, target ports.PushTarget) (string, error) {
	if target.Branch == "" {
		return "", fmt.Errorf("push needs a target branch")
	}
	sha, err := g.output(ctx, worktree, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	to := c
	if target.URL != "" {
		if !target.Repo.MatchesRemoteURL(target.URL) {
			return "", fmt.Errorf("push to %s: the URL %q does not point to %s", target.Branch, target.URL, target.Repo)
		}
		to.URL = target.URL
	}
	t, err := g.openTransfer(ctx, to)
	if err != nil {
		return "", err
	}
	defer t.close()

	if target.Expect != "" {
		tip, err := t.remoteTip(ctx, target.Branch)
		if err != nil {
			return "", err
		}
		if tip != target.Expect {
			return "", fmt.Errorf("push %s: the branch is at %q, not %s: %w", target.Branch, tip, target.Expect, ports.ErrConflict)
		}
	}
	hookOut, err := g.runPrePush(ctx, t, worktree, target.Branch, sha)
	if err != nil {
		return hookOut, err
	}
	ref := localRef(target.Branch)
	r, err := t.remote(ctx, "push", "--porcelain", "--no-verify", to.URL, sha+":"+ref)
	out := g.redactor.Redact(strings.TrimSpace(strings.Join([]string{hookOut, r.stdout, r.stderr}, "\n")))
	switch {
	case err == nil && target.URL == "":
		// As git push would, update the remote-tracking branch.
		_, err = g.run(ctx, c.Path, "update-ref", remoteRef(c, target.Branch), sha)
		return out, err
	case err == nil:
		return out, nil
	case rejected(r.stdout):
		return out, fmt.Errorf("push %s: %w", target.Branch, ports.ErrConflict)
	}
	return out, err
}
