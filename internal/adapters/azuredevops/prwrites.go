package azuredevops

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Thread statuses Anthill writes.
const (
	statusActive = "active"
	statusFixed  = "fixed"
)

// newThread is the body that creates a PR thread. Status is left out for
// general comments, so that Anthill's comments, questions included, do
// not block a branch policy that requires resolved comments. Pending
// questions are tracked by their marker (spec agent-conversation).
type newThread struct {
	Comments      []newComment   `json:"comments"`
	Status        string         `json:"status,omitempty"`
	ThreadContext *threadContext `json:"threadContext,omitempty"`
}

type newComment struct {
	ParentCommentID int    `json:"parentCommentId"`
	Content         string `json:"content"`
	CommentType     string `json:"commentType"`
}

type threadContext struct {
	FilePath       string   `json:"filePath"`
	RightFileStart position `json:"rightFileStart"`
	RightFileEnd   position `json:"rightFileEnd"`
}

// addPRComment posts a general comment as a new thread, idempotently by
// marker ID (design D7).
func (a *Adapter) addPRComment(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error) {
	if found, ok, err := a.findPRComment(ctx, ref, c.Marker.ID); err != nil || ok {
		return found, err
	}
	return a.createThread(ctx, ref, "comment on "+ref.Full(), newThread{
		Comments: []newComment{{ParentCommentID: 0, Content: c.String(), CommentType: "text"}},
	})
}

// findPRComment looks for a comment whose marker has the given ID in any
// thread of the PR. An empty ID finds nothing.
func (a *Adapter) findPRComment(ctx context.Context, ref domain.Ref, id string) (domain.Comment, bool, error) {
	if id == "" {
		return domain.Comment{}, false, nil
	}
	threads, _, err := a.threads(ctx, ref)
	if err != nil {
		return domain.Comment{}, false, err
	}
	var existing []domain.Comment
	for _, t := range threads {
		existing = append(existing, a.threadComments(ref, t)...)
	}
	found, ok := withMarkerID(existing, id)
	return found, ok, nil
}

// createThread creates a thread and returns its first comment.
func (a *Adapter) createThread(ctx context.Context, ref domain.Ref, op string, body newThread) (domain.Comment, error) {
	var created threadJSON
	if _, err := a.send(ctx, call{op: op, method: http.MethodPost, url: a.prURL(ref, nil, "threads"), body: body, out: &created}); err != nil {
		return domain.Comment{}, err
	}
	if len(created.Comments) == 0 {
		return domain.Comment{}, fmt.Errorf("azure devops: %s: the new thread has no comment", op)
	}
	return a.prComment(ref, created.ID, created.Comments[0]), nil
}

// parsePRCommentID reads a PR comment ID, <thread>.<comment>.
func parsePRCommentID(id string) (thread, comment int, err error) {
	t, c, ok := strings.Cut(id, ".")
	thread, err1 := strconv.Atoi(t)
	comment, err2 := strconv.Atoi(c)
	if !ok || err1 != nil || err2 != nil || thread <= 0 || comment <= 0 {
		return 0, 0, fmt.Errorf("azure devops: pull request comment id %q is not valid: %w", id, ports.ErrNotFound)
	}
	return thread, comment, nil
}

// editPRComment replaces the text of a PR comment.
func (a *Adapter) editPRComment(ctx context.Context, ref domain.Ref, id string, c domain.AnthillComment) (domain.Comment, error) {
	thread, comment, err := parsePRCommentID(id)
	if err != nil {
		return domain.Comment{}, err
	}
	var edited prCommentJSON
	if _, err := a.send(ctx, call{
		op:     fmt.Sprintf("edit comment %s on %s", id, ref.Full()),
		method: http.MethodPatch,
		url:    a.prURL(ref, nil, "threads", strconv.Itoa(thread), "comments", strconv.Itoa(comment)),
		body:   map[string]string{"content": c.String()},
		out:    &edited,
	}); err != nil {
		return domain.Comment{}, err
	}
	return a.prComment(ref, thread, edited), nil
}

// addPRLabel adds a label to a PR. Azure DevOps answers an existing
// label with the label, so adding it again does nothing.
func (a *Adapter) addPRLabel(ctx context.Context, ref domain.Ref, label string) error {
	_, err := a.send(ctx, call{
		op:     fmt.Sprintf("add label %q to %s", label, ref.Full()),
		method: http.MethodPost,
		url:    a.prURL(ref, nil, "labels"),
		body:   map[string]string{"name": label},
	})
	return err
}

// removePRLabel removes a label from a PR; a missing label is no error.
func (a *Adapter) removePRLabel(ctx context.Context, ref domain.Ref, label string) error {
	_, err := a.send(ctx, call{
		op:     fmt.Sprintf("remove label %q from %s", label, ref.Full()),
		method: http.MethodDelete,
		url:    a.prURL(ref, nil, "labels", label),
	})
	if notFound(err) {
		return nil
	}
	return err
}

// OpenPullRequest opens a PR with its labels, draft state and, when
// pr.Item is set, a native link to that work item (spec ant-tools, "Pull
// request tools"). The AB#<id> closing reference is already in the body.
// A PR that already exists for the branches fails with
// ports.ErrConflict, as Azure DevOps answers 409. The returned PR has no
// comments and no threads yet.
func (a *Adapter) OpenPullRequest(ctx context.Context, pr ports.NewPullRequest) (domain.PullRequest, error) {
	repo := Normalize(pr.Repo)
	if err := a.checkCodeRepo(repo); err != nil {
		return domain.PullRequest{}, err
	}
	body := map[string]any{
		"sourceRefName": "refs/heads/" + pr.Source,
		"targetRefName": "refs/heads/" + pr.Target,
		"title":         pr.Title,
		"description":   pr.Body,
		"isDraft":       pr.Draft,
	}
	if len(pr.Labels) > 0 {
		labels := make([]map[string]string, len(pr.Labels))
		for i, l := range pr.Labels {
			labels[i] = map[string]string{"name": l}
		}
		body["labels"] = labels
	}
	var native []int
	if pr.Item != nil {
		body["workItemRefs"] = []map[string]string{{"id": strconv.Itoa(pr.Item.Number)}}
		native = []int{pr.Item.Number}
	}
	var created prJSON
	if _, err := a.send(ctx, call{
		op:     fmt.Sprintf("open a pull request from %s on %s", pr.Source, repo),
		method: http.MethodPost,
		url:    a.api(nil, repo.Org, repo.Project, "_apis", "git", "repositories", repo.Name, "pullrequests"),
		body:   body,
		out:    &created,
	}); err != nil {
		return domain.PullRequest{}, err
	}
	mapped := a.mapPullRequest(repo, created, nil)
	mapped.Item = a.linkedItem(repo, created, native)
	mapped.Revision = fingerprint(mapped)
	return mapped, nil
}

// UpdatePullRequest changes the title, the body and the draft state in
// one call (spec ant-execution, "Draft PRs for incomplete work"). The
// returned PR has no comments and no threads.
func (a *Adapter) UpdatePullRequest(ctx context.Context, ref domain.Ref, u ports.PullRequestUpdate) (domain.PullRequest, error) {
	ref = normalizeRef(ref)
	if err := a.checkCodeRepo(ref.Repo); err != nil {
		return domain.PullRequest{}, err
	}
	body := map[string]any{}
	if u.Title != nil {
		body["title"] = *u.Title
	}
	if u.Body != nil {
		body["description"] = *u.Body
	}
	if u.Draft != nil {
		body["isDraft"] = *u.Draft
	}
	var p prJSON
	var err error
	if len(body) == 0 {
		p, _, err = a.readPR(ctx, ref)
	} else {
		_, err = a.send(ctx, call{op: "update pull request " + ref.Full(), method: http.MethodPatch, url: a.prURL(ref, nil), body: body, out: &p})
	}
	if err != nil {
		return domain.PullRequest{}, err
	}
	mapped := a.mapPullRequest(ref.Repo, p, nil)
	mapped.Item = a.linkedItem(ref.Repo, p, nil)
	mapped.Revision = fingerprint(mapped)
	return mapped, nil
}

// AddReviewComment posts an inline finding as an active thread on the
// lines of a file, idempotently by marker ID (design D9).
func (a *Adapter) AddReviewComment(ctx context.Context, ref domain.Ref, c ports.ReviewComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := a.checkCodeRepo(ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	if found, ok, err := a.findPRComment(ctx, ref, c.Comment.Marker.ID); err != nil || ok {
		return found, err
	}
	end := c.EndLine
	if end < c.StartLine {
		end = c.StartLine
	}
	return a.createThread(ctx, ref, fmt.Sprintf("add a review comment on %s:%d of %s", c.Path, c.StartLine, ref.Full()), newThread{
		Comments: []newComment{{ParentCommentID: 0, Content: c.Comment.String(), CommentType: "text"}},
		Status:   statusActive,
		ThreadContext: &threadContext{
			FilePath:       "/" + strings.TrimPrefix(c.Path, "/"),
			RightFileStart: position{Line: c.StartLine, Offset: 1},
			RightFileEnd:   position{Line: end, Offset: 1},
		},
	})
}

// ReplyToThread answers in a thread, as a reply to its first comment,
// idempotently by marker ID (spec ant-tools, "Conversation tools").
func (a *Adapter) ReplyToThread(ctx context.Context, ref domain.Ref, threadID string, c domain.AnthillComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := a.checkCodeRepo(ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	thread, err := strconv.Atoi(threadID)
	if err != nil || thread <= 0 {
		return domain.Comment{}, fmt.Errorf("azure devops: thread id %q is not valid: %w", threadID, ports.ErrNotFound)
	}
	op := fmt.Sprintf("reply to thread %s on %s", threadID, ref.Full())
	if id := c.Marker.ID; id != "" {
		var t threadJSON
		if _, err := a.send(ctx, call{op: op, method: http.MethodGet, url: a.prURL(ref, nil, "threads", threadID), out: &t}); err != nil {
			return domain.Comment{}, err
		}
		if found, ok := withMarkerID(a.threadComments(ref, t), id); ok {
			return found, nil
		}
	}
	var created prCommentJSON
	if _, err := a.send(ctx, call{
		op:     op,
		method: http.MethodPost,
		url:    a.prURL(ref, nil, "threads", threadID, "comments"),
		body:   newComment{ParentCommentID: 1, Content: c.String(), CommentType: "text"},
		out:    &created,
	}); err != nil {
		return domain.Comment{}, err
	}
	return a.prComment(ref, thread, created), nil
}

// ResolveThread marks a thread fixed (spec pr-shepherd, "Anthill
// verifies, pushes and replies").
func (a *Adapter) ResolveThread(ctx context.Context, ref domain.Ref, threadID string) error {
	ref = normalizeRef(ref)
	if err := a.checkCodeRepo(ref.Repo); err != nil {
		return err
	}
	if n, err := strconv.Atoi(threadID); err != nil || n <= 0 {
		return fmt.Errorf("azure devops: thread id %q is not valid: %w", threadID, ports.ErrNotFound)
	}
	_, err := a.send(ctx, call{
		op:     fmt.Sprintf("resolve thread %s on %s", threadID, ref.Full()),
		method: http.MethodPatch,
		url:    a.prURL(ref, nil, "threads", threadID),
		body:   map[string]string{"status": statusFixed},
	})
	return err
}
