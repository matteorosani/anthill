package testkit

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// OpPostAsQueen is a PostAsQueen request.
const OpPostAsQueen Op = "post-as-queen"

var _ ports.QueenCommentPort = (*ProviderClient)(nil)

// PostAsQueen posts the client's own text with no marker: in a review
// thread of a PR when threadID is set, else as a comment on the item or
// PR. On an item, a comment with a thread ID is a reply in that thread,
// as on a threaded provider.
func (c *ProviderClient) PostAsQueen(_ context.Context, ref domain.Ref, threadID, body string) (domain.Comment, error) {
	p := c.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.call(OpPostAsQueen, c.login, ref, ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	if threadID != "" && ref.Kind == domain.KindPR {
		e, t, err := p.thread(ref, threadID)
		if err != nil {
			return domain.Comment{}, err
		}
		comment := p.newComment(c.login, body)
		t.Comments = append(t.Comments, comment)
		p.touchPR(e)
		return comment, nil
	}
	list, touch, err := p.comments(ref)
	if err != nil {
		return domain.Comment{}, err
	}
	comment := p.newComment(c.login, body)
	comment.Thread = threadID
	*list = append(*list, comment)
	touch()
	return comment, nil
}
