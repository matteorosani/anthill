package testkit

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// CodeHost joins a SourceControl's remote to a Provider, as on GitHub,
// where the remote is the code host: the head commit of an open PR is the
// last commit of its source branch. Git wraps the source control, so that
// a push moves the head of the open PRs of its branch, and Client wraps a
// provider client, so that a new PR starts at its branch's last commit.
// Without it the two fakes know nothing of each other, and an ant PR has
// no head commit to review.
type CodeHost struct {
	p     *Provider
	git   *SourceControl
	clone string
	repo  domain.Repository
}

// NewCodeHost joins the remote of the clone at clonePath, a clone of
// repo, to p.
func NewCodeHost(p *Provider, git *SourceControl, clonePath string, repo domain.Repository) *CodeHost {
	return &CodeHost{p: p, git: git, clone: clonePath, repo: repo}
}

// Git returns the source control whose pushes move PR heads.
func (h *CodeHost) Git() *CodeHostGit {
	return &CodeHostGit{SourceControl: h.git, host: h}
}

// Client returns a client of the provider for login whose new PRs start
// at their source branch's last commit.
func (h *CodeHost) Client(login string) *CodeHostClient {
	return &CodeHostClient{ProviderClient: h.p.Client(login), host: h}
}

// head is the last commit of a remote branch, or "".
func (h *CodeHost) head(branch string) string {
	commits := h.git.RemoteCommits(h.clone, branch)
	if len(commits) == 0 {
		return ""
	}
	return commits[len(commits)-1].Hash
}

// moved sets the head commit of the open PRs of branch, as a push does.
func (h *CodeHost) moved(branch string) {
	head := h.head(branch)
	if head == "" {
		return
	}
	p := h.p
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.prs {
		pr := &e.pr
		if pr.Ref.Repo == h.repo && pr.State == domain.PROpen && pr.SourceBranch == branch && !pr.Fork && pr.HeadCommit != head {
			pr.HeadCommit = head
			p.touchPR(e)
		}
	}
}

// CodeHostGit is a SourceControl whose pushes move PR heads.
type CodeHostGit struct {
	*SourceControl
	host *CodeHost
}

var _ ports.SourceControl = (*CodeHostGit)(nil)

// Push pushes as SourceControl.Push does, then moves the heads of the
// branch's open PRs.
func (g *CodeHostGit) Push(ctx context.Context, clone ports.Clone, worktree, branch string, owner domain.BranchOwner) (string, error) {
	out, err := g.SourceControl.Push(ctx, clone, worktree, branch, owner)
	if err == nil && clone.Path == g.host.clone {
		g.host.moved(branch)
	}
	return out, err
}

// PushTo pushes as SourceControl.PushTo does, then moves the heads of the
// target branch's open PRs, unless it was pushed to a fork.
func (g *CodeHostGit) PushTo(ctx context.Context, clone ports.Clone, worktree string, target ports.PushTarget) (string, error) {
	out, err := g.SourceControl.PushTo(ctx, clone, worktree, target)
	if err == nil && target.URL == "" && clone.Path == g.host.clone {
		g.host.moved(target.Branch)
	}
	return out, err
}

// CodeHostClient is a provider client whose new PRs start at their source
// branch's last commit.
type CodeHostClient struct {
	*ProviderClient
	host *CodeHost
}

// OpenPullRequest opens the PR as ProviderClient.OpenPullRequest does,
// with the source branch's last commit as its head.
func (c *CodeHostClient) OpenPullRequest(ctx context.Context, n ports.NewPullRequest) (domain.PullRequest, error) {
	pr, err := c.ProviderClient.OpenPullRequest(ctx, n)
	if err != nil || n.Repo != c.host.repo {
		return pr, err
	}
	head := c.host.head(n.Source)
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.prs[pr.Ref]
	e.pr.HeadCommit = head
	return clonePR(e.pr), nil
}
