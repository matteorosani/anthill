package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// pushResult is the result of a push that reached the remote.
type pushResult struct {
	Status string `json:"status"`
	Output string `json:"output"`
}

// MessagePushConflict is the refusal of a push rejected because the
// remote branch has commits the worktree lacks (spec ant-tools, "Push
// rejected because the remote moved"). Push fetched them already.
func MessagePushConflict(remote, branch string) string {
	return fmt.Sprintf("the remote branch has commits this worktree lacks; they were fetched as %[1]s/%[2]s: merge them (git merge %[1]s/%[2]s) and call push again", remote, branch)
}

// push pushes the worktree's current branch under the branching policy,
// from the worktree with the developer's credentials and the
// repository's hooks (spec ant-tools, "Push tool"; spec
// branching-policy, "Push restrictions"). A rejection is returned as a
// refusal with git's output.
func (t *AntTools) push(ctx context.Context, c *toolCall) (any, error) {
	rec := c.ant
	ws, err := t.deps.Workspace(ctx, rec.Item)
	if err != nil {
		return nil, err
	}
	out, err := NewWorkspace(t.deps.Git, "").Push(ctx, PushRequest{
		Clone:    ws.Clone,
		Worktree: rec.Worktree,
		Owner:    domain.BranchOwner{Item: rec.Item.String(), Instance: c.claim.Instance},
		Policy: domain.PushRequest{
			Kind:      domain.PushAnt,
			Allowed:   rec.Branch,
			Base:      rec.BaseBranch,
			Protected: ws.Settings.ProtectedBranches,
		},
	})
	out = t.safe(c, out)
	var refused *domain.PushRefusedError
	switch {
	case err == nil:
		return pushResult{Status: "pushed", Output: out}, nil
	case errors.As(err, &refused):
		return nil, domain.RefuseTool("%s", refused.Reason)
	case errors.Is(err, ports.ErrConflict):
		return nil, domain.RefuseTool("push rejected: %s\n\ngit output:\n%s", MessagePushConflict(ws.Clone.Remote, rec.Branch), out)
	case errors.Is(err, ports.ErrHistoryRewritten):
		return nil, domain.RefuseTool("push rejected: %s\n\ngit output:\n%s", domain.MessageHistoryRewritten, out)
	}
	return nil, domain.RefuseTool("push failed: %s\n\ngit output:\n%s", t.safe(c, err.Error()), out)
}

// prResult is the result of open_pull_request and update_pull_request.
type prResult struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Draft bool   `json:"draft"`
	// ForcedDraft says why the PR is a draft whatever the agent chose.
	ForcedDraft string `json:"forced_draft,omitempty"`
}

// MessageSecondPR is the refusal of a second PR for the same item (spec
// ant-tools, "Second PR for the same item").
func MessageSecondPR(url string) string {
	return fmt.Sprintf("the item already has an open ant PR %s: use update_pull_request", url)
}

// openArgs are the arguments of open_pull_request.
type openArgs struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Draft bool   `json:"draft"`
}

// openPullRequest opens the ant PR from the pushed ant branch to the base
// branch, with the agent's title, body and draft choice. Anthill adds the
// closing reference and the opt-in label, and forces a draft after a
// partial or timed-out run (spec ant-tools, "Pull request tools").
func (t *AntTools) openPullRequest(ctx context.Context, c *toolCall) (any, error) {
	var args openArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	rec := c.ant
	ws, err := t.deps.Workspace(ctx, rec.Item)
	if err != nil {
		return nil, err
	}
	host, err := t.codeHost(ctx, ws.Repo)
	if err != nil {
		return nil, err
	}
	if rec.PR != nil {
		url := rec.PR.Display(ws.Repo)
		if pr, _, err := host.PullRequest(ctx, *rec.PR); err == nil {
			url = pr.URL
		}
		return nil, domain.RefuseTool("%s", MessageSecondPR(url))
	}
	page, err := host.PullRequests(ctx, ws.Repo, "")
	if err != nil {
		return nil, fmt.Errorf("listing the open pull requests: %w", err)
	}
	for _, pr := range page.PullRequests {
		if pr.SourceBranch == rec.Branch {
			return nil, domain.RefuseTool("%s", MessageSecondPR(pr.URL))
		}
	}
	if err := t.checkPushed(ctx, ws.Clone, rec.Branch); err != nil {
		return nil, err
	}
	forced, err := t.forcedDraft(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	pr, err := host.OpenPullRequest(ctx, ports.NewPullRequest{
		Repo:   ws.Repo,
		Source: rec.Branch,
		Target: rec.BaseBranch,
		Title:  t.safe(c, args.Title),
		Body:   domain.WithClosingReference(t.safe(c, args.Body), rec.Item, ws.Repo),
		Draft:  args.Draft || forced != "",
		Labels: []string{ws.Settings.OptInLabel},
		Item:   &rec.Item,
	})
	if err != nil {
		return nil, fmt.Errorf("opening the pull request: %w", err)
	}
	rec.PR = &pr.Ref
	if err := t.store.SaveAnt(context.WithoutCancel(ctx), rec); err != nil {
		return nil, err
	}
	return prResult{URL: pr.URL, Title: pr.Title, Draft: pr.Draft, ForcedDraft: forced}, nil
}

// checkPushed refuses to open a PR for a branch that is not on the
// remote, or that has commits not pushed yet.
func (t *AntTools) checkPushed(ctx context.Context, clone ports.Clone, branch string) error {
	info, err := t.deps.Git.Branch(ctx, clone, branch, "")
	if err != nil && !errors.Is(err, ports.ErrNotFound) {
		return err
	}
	if !info.Remote {
		return domain.RefuseTool("the branch %s is not on the remote: call push first", branch)
	}
	unpushed, err := t.deps.Git.Unpushed(ctx, clone, branch, "")
	if err != nil {
		return err
	}
	if len(unpushed) > 0 {
		return domain.RefuseTool("the branch has %d commits that are not pushed: call push first", len(unpushed))
	}
	return nil
}

// forcedDraft says why the ant PR must stay a draft, or "" when the agent
// may choose: the ant's last ended run was partial or timed out, and no
// later run finished complete (spec ant-execution, "Draft PRs for
// incomplete work"). Marking the PR ready after a complete finish is the
// finish handler's.
func (t *AntTools) forcedDraft(ctx context.Context, ant ports.AntID) (string, error) {
	runs, err := t.store.Runs(ctx, ant)
	if err != nil {
		return "", err
	}
	reason := ""
	for _, r := range runs {
		switch r.Outcome {
		case ports.OutcomePartial:
			reason = "an earlier run finished partial"
		case ports.OutcomeTimedOut:
			reason = "an earlier run timed out"
		case ports.OutcomeComplete:
			reason = ""
		}
	}
	if reason == "" {
		return "", nil
	}
	return "the PR stays a draft until a run finishes complete: " + reason, nil
}

// updateArgs are the arguments of update_pull_request. Nil fields stay
// as they are.
type updateArgs struct {
	Title *string `json:"title"`
	Body  *string `json:"body"`
	Draft *bool   `json:"draft"`
}

// updatePullRequest changes the ant PR's title, body or draft state. The
// body keeps its closing reference, and a forced draft cannot be marked
// ready.
func (t *AntTools) updatePullRequest(ctx context.Context, c *toolCall) (any, error) {
	var args updateArgs
	if err := decodeArgs(c.args, &args); err != nil {
		return nil, err
	}
	if args.Title == nil && args.Body == nil && args.Draft == nil {
		return nil, domain.RefuseTool("give at least one of title, body and draft")
	}
	pr, err := c.target(wherePR)
	if err != nil {
		return nil, err
	}
	rec := c.ant
	forced, err := t.forcedDraft(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if args.Draft != nil && !*args.Draft && forced != "" {
		return nil, domain.RefuseTool("%s", forced)
	}
	var u ports.PullRequestUpdate
	if args.Title != nil {
		title := t.safe(c, *args.Title)
		u.Title = &title
	}
	if args.Body != nil {
		body := domain.WithClosingReference(t.safe(c, *args.Body), rec.Item, pr.Repo)
		u.Body = &body
	}
	u.Draft = args.Draft
	host, err := t.codeHost(ctx, pr.Repo)
	if err != nil {
		return nil, err
	}
	updated, err := host.UpdatePullRequest(ctx, pr, u)
	if err != nil {
		return nil, fmt.Errorf("updating the pull request: %w", err)
	}
	return prResult{URL: updated.URL, Title: updated.Title, Draft: updated.Draft, ForcedDraft: forced}, nil
}
