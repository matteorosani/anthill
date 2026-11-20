package azuredevops

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

var _ ports.QueenCommentPort = (*Adapter)(nil)

// PostAsQueen posts the queen's own text, with no prefix and no marker:
// as a reply in a PR thread when threadID is set, as a new PR thread on
// a PR otherwise, and as a work item comment on an item.
func (a *Adapter) PostAsQueen(ctx context.Context, ref domain.Ref, threadID, body string) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return domain.Comment{}, err
	}
	if ref.Kind != domain.KindPR {
		var created commentJSON
		if _, err := a.send(ctx, call{
			op:     "comment on " + ref.Full(),
			method: http.MethodPost,
			url:    a.commentsURL(ref),
			body:   map[string]string{"text": body},
			out:    &created,
		}); err != nil {
			return domain.Comment{}, err
		}
		return a.comment(ref, created), nil
	}
	if threadID == "" {
		return a.createThread(ctx, ref, "comment on "+ref.Full(), newThread{
			Comments: []newComment{{ParentCommentID: 0, Content: body, CommentType: "text"}},
		})
	}
	thread, err := strconv.Atoi(threadID)
	if err != nil || thread <= 0 {
		return domain.Comment{}, fmt.Errorf("azure devops: thread id %q is not valid: %w", threadID, ports.ErrNotFound)
	}
	var created prCommentJSON
	if _, err := a.send(ctx, call{
		op:     fmt.Sprintf("reply to thread %s on %s", threadID, ref.Full()),
		method: http.MethodPost,
		url:    a.prURL(ref, nil, "threads", threadID, "comments"),
		body:   newComment{ParentCommentID: 1, Content: body, CommentType: "text"},
		out:    &created,
	}); err != nil {
		return domain.Comment{}, err
	}
	return a.prComment(ref, thread, created), nil
}
