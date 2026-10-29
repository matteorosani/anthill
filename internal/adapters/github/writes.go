package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// AddComment posts an Anthill comment on an item or a PR (a PR is an
// issue for comments). When the marker has an ID and a comment with the
// same marker ID exists, that comment is returned and nothing is posted
// (design D7).
func (a *Adapter) AddComment(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	if id := c.Marker.ID; id != "" {
		existing, _, err := a.issueComments(ctx, ref)
		if err != nil {
			return domain.Comment{}, err
		}
		if found, ok := withMarkerID(existing, id); ok {
			return found, nil
		}
	}
	created, resp, err := a.client.Issues.CreateComment(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, gh.IssueCommentRequest{Body: c.String()})
	if err != nil {
		return domain.Comment{}, a.apiError("comment on "+ref.Full(), resp, err)
	}
	return issueComment(created), nil
}

// withMarkerID finds the comment whose marker has the given ID.
func withMarkerID(comments []domain.Comment, id string) (domain.Comment, bool) {
	for _, c := range comments {
		if m, ok := domain.FindMarker(c.Body); ok && m.ID == id {
			return c, true
		}
	}
	return domain.Comment{}, false
}

// EditComment replaces the text of a comment: an issue comment, or a
// review comment when commentID starts with r.
func (a *Adapter) EditComment(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	id, review, err := parseCommentID(commentID)
	if err != nil {
		return domain.Comment{}, err
	}
	op := fmt.Sprintf("edit comment %s on %s", commentID, ref.Full())
	if review {
		edited, resp, err := a.client.PullRequests.UpdateComment(ctx, ref.Repo.Org, ref.Repo.Name, id, gh.UpdatePullRequestCommentRequest{Body: c.String()})
		if err != nil {
			return domain.Comment{}, a.apiError(op, resp, err)
		}
		return reviewComment(edited), nil
	}
	edited, resp, err := a.client.Issues.UpdateComment(ctx, ref.Repo.Org, ref.Repo.Name, id, gh.IssueCommentRequest{Body: c.String()})
	if err != nil {
		return domain.Comment{}, a.apiError(op, resp, err)
	}
	return issueComment(edited), nil
}

// parseCommentID reads a comment ID: 123 for an issue comment, r123 for
// a review comment.
func parseCommentID(s string) (id int64, review bool, err error) {
	digits, review := strings.CutPrefix(s, reviewPrefix)
	id, err = strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 {
		return 0, false, fmt.Errorf("github: comment id %q is not valid: %w", s, ports.ErrNotFound)
	}
	return id, review, nil
}

func reviewComment(c *gh.PullRequestComment) domain.Comment {
	return domain.Comment{
		ID:        reviewCommentID(c.GetID()),
		Author:    c.GetUser().GetLogin(),
		URL:       c.GetHTMLURL(),
		Body:      c.GetBody(),
		CreatedAt: c.GetCreatedAt().Time,
		UpdatedAt: c.GetUpdatedAt().Time,
	}
}

// AddLabel adds a label to an item or PR. Adding a present label does
// nothing.
func (a *Adapter) AddLabel(ctx context.Context, ref domain.Ref, label string) error {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return err
	}
	_, resp, err := a.client.Issues.AddLabelsToIssue(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, []string{label})
	if err != nil {
		return a.apiError(fmt.Sprintf("add label %q to %s", label, ref.Full()), resp, err)
	}
	return nil
}

// RemoveLabel removes a label from an item or PR. Removing a missing
// label does nothing.
func (a *Adapter) RemoveLabel(ctx context.Context, ref domain.Ref, label string) error {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return err
	}
	resp, err := a.client.Issues.RemoveLabelForIssue(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, url.PathEscape(label))
	if err != nil {
		if er, ok := errors.AsType[*gh.ErrorResponse](err); ok && er.Response != nil &&
			er.Response.StatusCode == http.StatusNotFound && strings.Contains(er.Message, "Label does not exist") {
			return nil
		}
		return a.apiError(fmt.Sprintf("remove label %q from %s", label, ref.Full()), resp, err)
	}
	return nil
}

// OpenPullRequest opens a PR and adds its labels (spec ant-tools, "Pull
// request tools"). The closing reference is already in the body. A PR
// that already exists for the branch fails with ports.ErrConflict. The
// returned PR has no comments and no threads yet.
func (a *Adapter) OpenPullRequest(ctx context.Context, pr ports.NewPullRequest) (domain.PullRequest, error) {
	repo := Normalize(pr.Repo)
	if err := checkRepo(repo); err != nil {
		return domain.PullRequest{}, err
	}
	op := fmt.Sprintf("open a pull request from %s on %s", pr.Source, repo)
	created, resp, err := a.client.PullRequests.Create(ctx, repo.Org, repo.Name, gh.CreatePullRequest{
		Title: new(pr.Title),
		Head:  pr.Source,
		Base:  pr.Target,
		Body:  new(pr.Body),
		Draft: new(pr.Draft),
	})
	if err != nil {
		if er, ok := errors.AsType[*gh.ErrorResponse](err); ok && er.Response != nil &&
			er.Response.StatusCode == http.StatusUnprocessableEntity && strings.Contains(er.Error(), "already exists") {
			return domain.PullRequest{}, fmt.Errorf("github: %s: a pull request already exists: %w", op, ports.ErrConflict)
		}
		return domain.PullRequest{}, a.apiError(op, resp, err)
	}
	if len(pr.Labels) > 0 {
		labels, resp, err := a.client.Issues.AddLabelsToIssue(ctx, repo.Org, repo.Name, created.GetNumber(), pr.Labels)
		if err != nil {
			return domain.PullRequest{}, a.apiError(fmt.Sprintf("label pull request #%d of %s", created.GetNumber(), repo), resp, err)
		}
		created.Labels = labels
	}
	return a.mapPullRequest(repo, created), nil
}

const (
	draftMutation = `mutation($id: ID!) {
  convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { isDraft } }
}`
	readyMutation = `mutation($id: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } }
}`
)

// UpdatePullRequest changes the title and body over REST, and the draft
// state over GraphQL, which is the only way to turn an open PR into a
// draft (spec ant-execution, "Draft PRs for incomplete work"). The
// returned PR has no comments and no threads.
func (a *Adapter) UpdatePullRequest(ctx context.Context, ref domain.Ref, u ports.PullRequestUpdate) (domain.PullRequest, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.PullRequest{}, err
	}
	org, name := ref.Repo.Org, ref.Repo.Name
	op := "update pull request " + ref.Full()
	var pr *gh.PullRequest
	if u.Title != nil || u.Body != nil {
		edited, resp, err := a.client.PullRequests.Edit(ctx, org, name, ref.Number, &gh.PullRequest{Title: u.Title, Body: u.Body})
		if err != nil {
			return domain.PullRequest{}, a.apiError(op, resp, err)
		}
		pr = edited
	}
	if pr == nil {
		read, resp, err := a.client.PullRequests.Get(ctx, org, name, ref.Number)
		if err != nil {
			return domain.PullRequest{}, a.apiError(op, resp, err)
		}
		pr = read
	}
	if u.Draft != nil && pr.GetDraft() != *u.Draft {
		mutation := readyMutation
		if *u.Draft {
			mutation = draftMutation
		}
		var data map[string]any
		if err := a.graphql(ctx, op, mutation, map[string]any{"id": pr.GetNodeID()}, &data); err != nil {
			return domain.PullRequest{}, err
		}
		pr.Draft = u.Draft
	}
	return a.mapPullRequest(ref.Repo, pr), nil
}

// AddReviewComment posts an inline finding on the PR's head commit,
// idempotently by marker ID like AddComment (design D9).
func (a *Adapter) AddReviewComment(ctx context.Context, ref domain.Ref, c ports.ReviewComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	org, name := ref.Repo.Org, ref.Repo.Name
	op := fmt.Sprintf("add a review comment on %s:%d of %s", c.Path, c.StartLine, ref.Full())
	if id := c.Comment.Marker.ID; id != "" {
		list, resp, err := collect(func(o gh.ListOptions) ([]*gh.PullRequestComment, *gh.Response, error) {
			return a.client.PullRequests.ListComments(ctx, org, name, ref.Number, &gh.PullRequestListCommentsOptions{ListOptions: o})
		})
		if err != nil {
			return domain.Comment{}, a.apiError(op, resp, err)
		}
		existing := make([]domain.Comment, 0, len(list))
		for _, rc := range list {
			existing = append(existing, reviewComment(rc))
		}
		if found, ok := withMarkerID(existing, id); ok {
			return found, nil
		}
	}
	pr, resp, err := a.client.PullRequests.Get(ctx, org, name, ref.Number)
	if err != nil {
		return domain.Comment{}, a.apiError(op, resp, err)
	}
	req := gh.CreatePullRequestCommentRequest{
		Body:     c.Comment.String(),
		CommitID: pr.GetHead().GetSHA(),
		Path:     c.Path,
		Line:     new(c.StartLine),
		Side:     new("RIGHT"),
	}
	if c.EndLine > c.StartLine {
		req.StartLine, req.StartSide = new(c.StartLine), new("RIGHT")
		req.Line = new(c.EndLine)
	}
	created, resp, err := a.client.PullRequests.CreateComment(ctx, org, name, ref.Number, req)
	if err != nil {
		return domain.Comment{}, a.apiError(op, resp, err)
	}
	return reviewComment(created), nil
}

const (
	threadCommentsQuery = `query($id: ID!) {
  node(id: $id) {
    ... on PullRequestReviewThread {
      comments(first: 100) { nodes { databaseId url body createdAt updatedAt author { login } } }
    }
  }
}`
	replyMutation = `mutation($id: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $id, body: $body}) {
    comment { databaseId url body createdAt updatedAt author { login } }
  }
}`
	resolveMutation = `mutation($id: ID!) {
  resolveReviewThread(input: {threadId: $id}) { thread { id isResolved } }
}`
)

// ReplyToThread answers in a review thread, idempotently by marker ID
// like AddComment (spec ant-tools, "Conversation tools").
func (a *Adapter) ReplyToThread(ctx context.Context, ref domain.Ref, threadID string, c domain.AnthillComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	op := fmt.Sprintf("reply to thread %s on %s", threadID, ref.Full())
	if id := c.Marker.ID; id != "" {
		var data struct {
			Node *struct {
				Comments struct {
					Nodes []gqlComment `json:"nodes"`
				} `json:"comments"`
			} `json:"node"`
		}
		if err := a.graphql(ctx, op, threadCommentsQuery, map[string]any{"id": threadID}, &data); err != nil {
			return domain.Comment{}, err
		}
		if data.Node == nil {
			return domain.Comment{}, fmt.Errorf("github: %s: %w", op, ports.ErrNotFound)
		}
		existing := make([]domain.Comment, 0, len(data.Node.Comments.Nodes))
		for _, gc := range data.Node.Comments.Nodes {
			existing = append(existing, gc.comment())
		}
		if found, ok := withMarkerID(existing, id); ok {
			return found, nil
		}
	}
	var data struct {
		Reply struct {
			Comment gqlComment `json:"comment"`
		} `json:"addPullRequestReviewThreadReply"`
	}
	if err := a.graphql(ctx, op, replyMutation, map[string]any{"id": threadID, "body": c.String()}, &data); err != nil {
		return domain.Comment{}, err
	}
	return data.Reply.Comment.comment(), nil
}

// ResolveThread resolves a review thread (spec pr-shepherd, "Anthill
// verifies, pushes and replies").
func (a *Adapter) ResolveThread(ctx context.Context, ref domain.Ref, threadID string) error {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return err
	}
	var data map[string]any
	return a.graphql(ctx, fmt.Sprintf("resolve thread %s on %s", threadID, ref.Full()), resolveMutation, map[string]any{"id": threadID}, &data)
}
