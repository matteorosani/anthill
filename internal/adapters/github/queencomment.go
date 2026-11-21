package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

var _ ports.QueenCommentPort = (*Adapter)(nil)

// PostAsQueen posts the queen's own text, with no prefix and no marker:
// as a reply in a review thread when threadID is set, and as an issue
// comment on the item or PR otherwise.
func (a *Adapter) PostAsQueen(ctx context.Context, ref domain.Ref, threadID, body string) (domain.Comment, error) {
	ref = normalizeRef(ref)
	if err := checkRepo(ref.Repo); err != nil {
		return domain.Comment{}, err
	}
	if threadID != "" {
		op := fmt.Sprintf("reply to thread %s on %s", threadID, ref.Full())
		var data struct {
			Reply struct {
				Comment gqlComment `json:"comment"`
			} `json:"addPullRequestReviewThreadReply"`
		}
		if err := a.graphql(ctx, op, replyMutation, map[string]any{"id": threadID, "body": body}, &data); err != nil {
			return domain.Comment{}, err
		}
		return data.Reply.Comment.comment(), nil
	}
	created, resp, err := a.client.Issues.CreateComment(ctx, ref.Repo.Org, ref.Repo.Name, ref.Number, gh.IssueCommentRequest{Body: body})
	if err != nil {
		return domain.Comment{}, a.apiError("comment on "+ref.Full(), resp, err)
	}
	return issueComment(created), nil
}
