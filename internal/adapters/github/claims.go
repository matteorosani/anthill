package github

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Claims reads the claim comments and /anthill release commands of an
// item or PR in creation order, its labels, and GitHub's time as the now
// for lease checks (design D3). The labels are read first, so a label
// removed before a claim comment was read is never missed. GitHub has no
// revision, so Revision is empty.
func (a *Adapter) Claims(ctx context.Context, ref domain.Ref) (ports.ClaimPage, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return ports.ClaimPage{}, err
	}
	labels, resp, err := collect(func(o gh.ListOptions) ([]*gh.Label, *gh.Response, error) {
		return a.client.Issues.ListLabelsByIssue(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, &o)
	})
	if err != nil {
		return ports.ClaimPage{}, a.apiError("list labels of "+ref.Full(), resp, err)
	}
	comments, resp, err := a.issueComments(ctx, ref)
	if err != nil {
		return ports.ClaimPage{}, err
	}
	page := ports.ClaimPage{ServerTime: a.serverTime(resp)}
	for _, l := range labels {
		page.Labels = append(page.Labels, l.GetName())
	}
	for _, c := range comments {
		if domain.IsClaimComment(c) {
			page.Comments = append(page.Comments, c)
		}
	}
	// GitHub lists comments oldest first; sorting makes the order explicit,
	// with the comment ID as tie-breaker (spec work-claiming, "Claim
	// acquisition resolves races deterministically").
	slices.SortStableFunc(page.Comments, func(x, y domain.Comment) int {
		if c := x.CreatedAt.Compare(y.CreatedAt); c != 0 {
			return c
		}
		xi, _ := strconv.ParseInt(x.ID, 10, 64)
		yi, _ := strconv.ParseInt(y.ID, 10, 64)
		return cmp.Compare(xi, yi)
	})
	return page, nil
}

// Post posts a claim comment, idempotently by marker ID like AddComment.
func (a *Adapter) Post(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error) {
	return a.AddComment(ctx, ref, c)
}

// Edit replaces a claim comment's text, which moves its updated_at and so
// renews the lease.
func (a *Adapter) Edit(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error) {
	return a.EditComment(ctx, ref, commentID, c)
}

// Retract deletes the claim comment of a lost race.
func (a *Adapter) Retract(ctx context.Context, ref domain.Ref, commentID string) error {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return err
	}
	id, review, err := parseCommentID(commentID)
	if err != nil {
		return err
	}
	if review {
		return fmt.Errorf("github: claim comment %s is a review comment: %w", commentID, ports.ErrNotFound)
	}
	resp, err := a.client.Issues.DeleteComment(ctx, ref.Repo.Org, ref.Repo.Name, id)
	if err != nil {
		return a.apiError(fmt.Sprintf("delete comment %s on %s", commentID, ref.Full()), resp, err)
	}
	return nil
}

// Mark adds the claim label and, when m.Assign is set, the authenticated
// user as assignee of the item. assigned is false when the user was
// already assigned. GitHub has no revision precondition, so m.Revision is
// not used (design D3).
func (a *Adapter) Mark(ctx context.Context, ref domain.Ref, m ports.ClaimMark) (bool, error) {
	ref = normalizeRef(ref)
	if err := a.AddLabel(ctx, ref, m.Label); err != nil {
		return false, err
	}
	if !m.Assign {
		return false, nil
	}
	if ref.Kind != domain.KindItem {
		return false, fmt.Errorf("github: only items get an assignee, not %s", ref.Full())
	}
	login, err := a.currentLogin(ctx)
	if err != nil {
		return false, err
	}
	op := fmt.Sprintf("assign %s to %s", login, ref.Full())
	issue, resp, err := a.client.Issues.Get(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number)
	if err != nil {
		return false, a.apiError(op, resp, err)
	}
	for _, u := range issue.Assignees {
		if strings.EqualFold(u.GetLogin(), login) {
			return false, nil
		}
	}
	if _, resp, err := a.client.Issues.AddAssignees(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, []string{login}); err != nil {
		return false, a.apiError(op, resp, err)
	}
	return true, nil
}

// Unmark removes the claim label and, when unassign is set, the
// authenticated user from the item's assignees (spec work-claiming,
// "Release").
func (a *Adapter) Unmark(ctx context.Context, ref domain.Ref, label string, unassign bool) error {
	ref = normalizeRef(ref)
	if err := a.RemoveLabel(ctx, ref, label); err != nil {
		return err
	}
	if !unassign || ref.Kind != domain.KindItem {
		return nil
	}
	login, err := a.currentLogin(ctx)
	if err != nil {
		return err
	}
	if _, resp, err := a.client.Issues.RemoveAssignees(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, []string{login}); err != nil {
		return a.apiError(fmt.Sprintf("unassign %s from %s", login, ref.Full()), resp, err)
	}
	return nil
}
