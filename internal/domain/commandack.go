package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// CommandAckID is the marker ID of the reply that acknowledges a command
// comment (spec agent-conversation, "Each command is executed once"). One
// reply acknowledges every command of the comment.
func CommandAckID(commentID string) string {
	return "cmd-" + commentID
}

// FindCommandAck finds the reply that acknowledges the command comment
// commentID among comments. Only a reply written by a user with write
// access counts, so a copied marker from anyone else does not stop the
// command (design, Risks: forged markers).
func FindCommandAck(comments []Comment, commentID string, writers UserSet) (Comment, bool) {
	id := CommandAckID(commentID)
	for _, c := range comments {
		m, ok := FindMarker(c.Body)
		if ok && m.Kind == CommentReply && m.ID == id && writers.Contains(c.Author) {
			return c, true
		}
	}
	return Comment{}, false
}

// CommentsBefore keeps the comments posted before c in provider order:
// by creation time, then by comment id.
func CommentsBefore(comments []Comment, c Comment) []Comment {
	var out []Comment
	for _, x := range comments {
		if compareComments(x, c.CreatedAt, c.ID) < 0 {
			out = append(out, x)
		}
	}
	return out
}

// UnreleaseAfter returns the comments with the releases written from at on
// taken back: a claim comment marked released by an edit at or after at
// shows its claim as not released, as it was at that time; an edit at the
// same time counts as later, as provider times can be that coarse. The edit
// that released it also renewed its lease, so the claim holds at at.
func UnreleaseAfter(comments []Comment, at time.Time) []Comment {
	out := slices.Clone(comments)
	for i, c := range out {
		m, ok := FindMarker(c.Body)
		if !ok || m.ClaimFields == nil || !m.Released || c.UpdatedAt.Before(at) {
			continue
		}
		fields := *m.ClaimFields
		fields.Released = false
		m.ClaimFields = &fields
		out[i].Body = markerPattern.ReplaceAllLiteralString(c.Body, m.String())
	}
	return out
}

// CommandReply is what Anthill says about one command of a comment.
type CommandReply struct {
	Name CommandName
	Text string
}

// RenderCommandAck renders the acknowledgement of a command comment: one
// paragraph per command, in the comment's order.
func RenderCommandAck(replies []CommandReply) string {
	var b strings.Builder
	for i, r := range replies {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "`%s`: %s", r.Name, strings.TrimSpace(r.Text))
	}
	return b.String()
}

// The replies to commands that ran (spec agent-conversation, "Command
// set"). The other outcomes take their reply from AuthorizeCommand.
const (
	ReplyStopped      = "stopped the ant's active run. Its claim is parked and its session saved: comment `/anthill retry` to start a new run that resumes it."
	ReplyNoActiveRun  = "the ant has no active run to stop."
	ReplyRetried      = "retrying: a new run resumes the ant's session with its remaining budget."
	ReplyCannotRetry  = "only a failed or stopped ant can be retried; this one is %s."
	ReplyReleased     = "claims released. Anthill no longer works on this item unless it is accepted again."
	ReplyNoAnt        = "this instance has no ant for this item."
	ReplyRoundStarted = "a shepherd round reviews this pull request."
	ReplyFixOptedIn   = "the shepherd will try to fix this thread on the PR branch."
	ReplyFixForwarded = "the ant that owns this pull request will handle this thread in a follow-up run."
)
