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

// conflictRetries is how many times a tag change that is not a claim is
// tried again after a revision conflict: a release must not fail because
// someone edited the item at the same moment.
const conflictRetries = 3

// AddComment posts an Anthill comment on a work item, or on a PR as a new
// thread. When the marker has an ID and a comment with the same marker ID
// exists, that comment is returned and nothing is posted (design D7).
func (a *Adapter) AddComment(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return domain.Comment{}, err
	}
	if ref.Kind == domain.KindPR {
		return a.addPRComment(ctx, ref, c)
	}
	if id := c.Marker.ID; id != "" {
		existing, _, err := a.workItemComments(ctx, ref)
		if err != nil {
			return domain.Comment{}, err
		}
		if found, ok := withMarkerID(existing, id); ok {
			return found, nil
		}
	}
	var created commentJSON
	if _, err := a.send(ctx, call{
		op:     "comment on " + ref.Full(),
		method: http.MethodPost,
		url:    a.commentsURL(ref),
		body:   map[string]string{"text": c.String()},
		out:    &created,
	}); err != nil {
		return domain.Comment{}, err
	}
	return a.comment(ref, created), nil
}

// commentsURL is the comments endpoint of a work item, or of one comment
// when id is given. Comments are written as Markdown, which keeps the
// hidden marker (design D13).
func (a *Adapter) commentsURL(ref domain.Ref, id ...string) string {
	query := url.Values{"api-version": {commentsAPIVersion}, "format": {"markdown"}}
	segments := append([]string{ref.Repo.Org, ref.Repo.Project, "_apis", "wit", "workItems", strconv.Itoa(ref.Number), "comments"}, id...)
	return a.api(query, segments...)
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

// EditComment replaces the text of a work item comment, or of a PR
// comment whose ID is <thread>.<comment>.
func (a *Adapter) EditComment(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return domain.Comment{}, err
	}
	if ref.Kind == domain.KindPR {
		return a.editPRComment(ctx, ref, commentID, c)
	}
	if _, err := strconv.Atoi(commentID); err != nil {
		return domain.Comment{}, fmt.Errorf("azure devops: comment id %q is not valid: %w", commentID, ports.ErrNotFound)
	}
	var edited commentJSON
	if _, err := a.send(ctx, call{
		op:     fmt.Sprintf("edit comment %s on %s", commentID, ref.Full()),
		method: http.MethodPatch,
		url:    a.commentsURL(ref, commentID),
		body:   map[string]string{"text": c.String()},
		out:    &edited,
	}); err != nil {
		return domain.Comment{}, err
	}
	return a.comment(ref, edited), nil
}

// AddLabel adds a tag to a work item, or a label to a PR. Adding a
// present label, in any case, does nothing.
func (a *Adapter) AddLabel(ctx context.Context, ref domain.Ref, label string) error {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return err
	}
	if ref.Kind == domain.KindPR {
		return a.addPRLabel(ctx, ref, label)
	}
	return a.changeTags(ctx, ref, fmt.Sprintf("add tag %q to %s", label, ref.Full()), func(w workItemJSON) []patchOp {
		tags := splitTags(w.Fields.Tags)
		if domain.HasLabel(tags, label) {
			return nil
		}
		return []patchOp{{Op: "add", Path: "/fields/System.Tags", Value: joinTags(append(tags, label))}}
	})
}

// RemoveLabel removes a tag from a work item, or a label from a PR.
// Removing a missing label does nothing.
func (a *Adapter) RemoveLabel(ctx context.Context, ref domain.Ref, label string) error {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return err
	}
	if ref.Kind == domain.KindPR {
		return a.removePRLabel(ctx, ref, label)
	}
	return a.changeTags(ctx, ref, fmt.Sprintf("remove tag %q from %s", label, ref.Full()), func(w workItemJSON) []patchOp {
		return removeTag(w, label)
	})
}

// removeTag is the patch that removes label from a work item's tags, or
// nil when it has no such tag.
func removeTag(w workItemJSON, label string) []patchOp {
	tags := splitTags(w.Fields.Tags)
	kept := slices.DeleteFunc(slices.Clone(tags), func(t string) bool { return strings.EqualFold(t, label) })
	if len(kept) == len(tags) {
		return nil
	}
	return []patchOp{{Op: "add", Path: "/fields/System.Tags", Value: joinTags(kept)}}
}

// changeTags reads a work item, asks change for the patch, and sends it
// with a test on the revision it read. A revision conflict, someone
// else's edit in between, starts over, up to conflictRetries times. An
// empty patch sends nothing.
func (a *Adapter) changeTags(ctx context.Context, ref domain.Ref, op string, change func(workItemJSON) []patchOp) error {
	for attempt := 0; ; attempt++ {
		w, _, err := a.readWorkItem(ctx, ref, url.Values{"fields": {"System.Tags,System.AssignedTo,System.TeamProject"}}, op)
		if err != nil {
			return err
		}
		ops := change(w)
		if len(ops) == 0 {
			return nil
		}
		_, err = a.patchWorkItem(ctx, ref, op, w.Rev, ops)
		if err == nil || !errors.Is(err, ports.ErrConflict) || attempt == conflictRetries {
			return err
		}
	}
}

// patchWorkItem sends a JSON Patch to a work item, preceded by a test that
// its revision is still rev (design D3). A failed test is a 409 or 412,
// which becomes ports.ErrConflict.
func (a *Adapter) patchWorkItem(ctx context.Context, ref domain.Ref, op string, rev int, ops []patchOp) (workItemJSON, error) {
	doc := append([]patchOp{{Op: "test", Path: "/rev", Value: rev}}, ops...)
	var w workItemJSON
	_, err := a.send(ctx, call{
		op:          op,
		method:      http.MethodPatch,
		url:         a.api(nil, ref.Repo.Org, ref.Repo.Project, "_apis", "wit", "workitems", strconv.Itoa(ref.Number)),
		body:        doc,
		contentType: contentJSONPatch,
		out:         &w,
	})
	return w, err
}
