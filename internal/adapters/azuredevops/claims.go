package azuredevops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Claims reads the claim comments and /anthill release commands of a
// work item or a PR in creation order, its labels, and Azure DevOps' time
// as the now for lease checks (design D3). The labels are read before the
// comments, so a label removed before a claim comment was read is never
// missed. For a work item, Revision is its System.Rev, read with the
// tags: a change between the two reads then makes Mark fail, never pass
// on a stale view. A PR has no revision.
func (a *Adapter) Claims(ctx context.Context, ref domain.Ref) (ports.ClaimPage, error) {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return ports.ClaimPage{}, err
	}
	var page ports.ClaimPage
	var comments []domain.Comment
	if ref.Kind == domain.KindPR {
		p, _, err := a.readPR(ctx, ref)
		if err != nil {
			return ports.ClaimPage{}, err
		}
		for _, l := range p.Labels {
			if l.Active == nil || *l.Active {
				page.Labels = append(page.Labels, l.Name)
			}
		}
		threads, resp, err := a.threads(ctx, ref)
		if err != nil {
			return ports.ClaimPage{}, err
		}
		for _, t := range threads {
			if t.ThreadContext == nil && !t.IsDeleted {
				comments = append(comments, a.threadComments(ref, t)...)
			}
		}
		page.ServerTime = a.serverTime(resp)
	} else {
		w, _, err := a.readWorkItem(ctx, ref, url.Values{"fields": {"System.Rev,System.Tags,System.TeamProject"}}, "read the revision of "+ref.Full())
		if err != nil {
			return ports.ClaimPage{}, err
		}
		page.Revision, page.Labels = strconv.Itoa(w.Rev), splitTags(w.Fields.Tags)
		list, resp, err := a.workItemComments(ctx, ref)
		if err != nil {
			return ports.ClaimPage{}, err
		}
		comments = list
		page.ServerTime = a.serverTime(resp)
	}
	for _, c := range comments {
		if domain.IsClaimComment(c) {
			page.Comments = append(page.Comments, c)
		}
	}
	slices.SortStableFunc(page.Comments, compareComments)
	return page, nil
}

// Post posts a claim comment, idempotently by marker ID like AddComment.
func (a *Adapter) Post(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error) {
	return a.AddComment(ctx, ref, c)
}

// Edit replaces a claim comment's text, which moves its modified date and
// so renews the lease.
func (a *Adapter) Edit(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error) {
	return a.EditComment(ctx, ref, commentID, c)
}

// Retract deletes the claim comment of a lost race.
func (a *Adapter) Retract(ctx context.Context, ref domain.Ref, commentID string) error {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return err
	}
	op := fmt.Sprintf("delete comment %s on %s", commentID, ref.Full())
	if ref.Kind == domain.KindPR {
		thread, comment, err := parsePRCommentID(commentID)
		if err != nil {
			return err
		}
		_, err = a.send(ctx, call{op: op, method: http.MethodDelete, url: a.prURL(ref, nil, "threads", strconv.Itoa(thread), "comments", strconv.Itoa(comment))})
		return err
	}
	if _, err := strconv.Atoi(commentID); err != nil {
		return fmt.Errorf("azure devops: comment id %q is not valid: %w", commentID, ports.ErrNotFound)
	}
	query := url.Values{"api-version": {commentsAPIVersion}}
	_, err := a.send(ctx, call{
		op:     op,
		method: http.MethodDelete,
		url:    a.api(query, ref.Repo.Org, ref.Repo.Project, "_apis", "wit", "workItems", strconv.Itoa(ref.Number), "comments", commentID),
	})
	return err
}

// Mark adds the claim label and, when m.Assign is set, the authenticated
// user as Assigned To.
//
// On a work item, the tag and the assignment are one JSON Patch that
// starts with a test that the revision is still m.Revision (or the one
// just read when m.Revision is empty). A failed test means another change
// came first and fails with ports.ErrConflict, a lost race (spec
// work-claiming, "Azure DevOps optimistic concurrency"); it is never
// retried. Assigned To holds one person: it is set only when it is empty,
// and assigned is true only then. A work item assigned to someone else
// keeps that assignment, because the release could not restore it.
//
// On a PR, only the label is added, with no precondition.
func (a *Adapter) Mark(ctx context.Context, ref domain.Ref, m ports.ClaimMark) (bool, error) {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return false, err
	}
	if ref.Kind == domain.KindPR {
		if m.Assign {
			return false, fmt.Errorf("azure devops: only work items get an assignee, not %s", ref.Full())
		}
		return false, a.addPRLabel(ctx, ref, m.Label)
	}
	login := ""
	if m.Assign {
		var err error
		if login, err = a.currentLogin(ctx); err != nil {
			return false, err
		}
	}
	op := "claim " + ref.Full()
	w, _, err := a.readWorkItem(ctx, ref, url.Values{"fields": {"System.Tags,System.AssignedTo,System.TeamProject"}}, op)
	if err != nil {
		return false, err
	}
	rev := w.Rev
	if m.Revision != "" {
		if rev, err = strconv.Atoi(m.Revision); err != nil {
			return false, fmt.Errorf("azure devops: %s: revision %q is not a number", op, m.Revision)
		}
	}
	tags := splitTags(w.Fields.Tags)
	if !domain.HasLabel(tags, m.Label) {
		tags = append(tags, m.Label)
	}
	ops := []patchOp{{Op: "add", Path: "/fields/System.Tags", Value: joinTags(tags)}}
	assigned := false
	if m.Assign && w.Fields.AssignedTo.login() == "" {
		ops = append(ops, patchOp{Op: "add", Path: "/fields/System.AssignedTo", Value: login})
		assigned = true
	}
	if _, err := a.patchWorkItem(ctx, ref, op, rev, ops); err != nil {
		return false, err
	}
	return assigned, nil
}

// Unmark removes the claim label and, when unassign is set and the item
// is still assigned to the authenticated user, the assignment (spec
// work-claiming, "Release"). On a work item, a revision conflict is tried
// again, like any tag change that is not a claim.
func (a *Adapter) Unmark(ctx context.Context, ref domain.Ref, label string, unassign bool) error {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return err
	}
	if ref.Kind == domain.KindPR {
		return a.removePRLabel(ctx, ref, label)
	}
	login := ""
	if unassign {
		var err error
		if login, err = a.currentLogin(ctx); err != nil {
			return err
		}
	}
	return a.changeTags(ctx, ref, "release the claim on "+ref.Full(), func(w workItemJSON) []patchOp {
		ops := removeTag(w, label)
		if unassign && strings.EqualFold(w.Fields.AssignedTo.login(), login) {
			ops = append(ops, patchOp{Op: "remove", Path: "/fields/System.AssignedTo"})
		}
		return ops
	})
}

// notFound reports whether err is a 404.
func notFound(err error) bool {
	return errors.Is(err, ports.ErrNotFound)
}
