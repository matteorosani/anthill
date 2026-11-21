package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// QueenCommentRoute finds where the queen's own comments on a ticket or
// PR are posted. The daemon builds it from the provider connections.
type QueenCommentRoute func(ctx context.Context, ref domain.Ref) (ports.QueenCommentPort, error)

// NoPendingQuestionError is an answer given in the console to an ant
// that has no pending question.
type NoPendingQuestionError struct {
	Item domain.Ref
}

func (e *NoPendingQuestionError) Error() string {
	return fmt.Sprintf("the ant for %s has no pending question", e.Item.Display(e.Item.Repo))
}

// AnswerFromConsole answers the ant's pending question with the queen's
// text (spec queen-console, "Answer from the console"). The answer is
// posted where the question was asked, as the queen's own comment with no
// marker (design D14): a reply in the question's thread on a threaded
// provider, and otherwise a comment that quotes the question, so that
// answer detection recognises it as well. It is then stored and the ant
// triggered through the same path as an answer found on the provider, so
// the run that resumes the session starts, or the ant is queued while
// capacity is full. When the next poll delivers the posted comment as a
// change, HandleAnswer finds it already stored and starts no second run.
func (c *Conversation) AnswerFromConsole(ctx context.Context, ant ports.AntID, text string) (domain.Comment, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return domain.Comment{}, errors.New("the answer is empty")
	case len(domain.ParseCommands(text)) > 0:
		return domain.Comment{}, errors.New("the answer holds an /anthill command; run the command on the provider instead")
	case c.deps.Queen == nil:
		return domain.Comment{}, errors.New("answering from the console is not available")
	}
	rec, err := c.deps.Store.Ant(ctx, ant)
	if err != nil {
		return domain.Comment{}, fmt.Errorf("reading ant %d: %w", ant, err)
	}
	qs, err := c.deps.Store.AntQuestions(ctx, ant)
	if err != nil {
		return domain.Comment{}, fmt.Errorf("reading the questions of ant %d: %w", ant, err)
	}
	if len(qs) == 0 || !qs[len(qs)-1].AnsweredAt.IsZero() || rec.State() != domain.AntAwaitingInput {
		return domain.Comment{}, &NoPendingQuestionError{Item: rec.Item}
	}
	q := qs[len(qs)-1]
	port, err := c.deps.Queen(ctx, q.On)
	if err != nil {
		return domain.Comment{}, fmt.Errorf("finding where to answer on %s: %w", q.On, err)
	}
	body := text
	if q.Thread == "" {
		body = QuoteQuestion(q.Text) + "\n\n" + text
	}
	posted, err := port.PostAsQueen(ctx, q.On, q.Thread, body)
	if err != nil {
		return domain.Comment{}, fmt.Errorf("posting the answer on %s: %w", q.On, err)
	}
	thread := q.Thread
	if posted.Thread != "" {
		thread = posted.Thread
	}
	at := posted.CreatedAt
	if at.IsZero() {
		at = c.deps.Clock.Now()
	}
	e := domain.ChangeEvent{
		Ref: q.On, Kind: domain.ChangeCommentAdded, Actor: posted.Author, At: at, Origin: domain.OriginHuman,
		CommentID: posted.ID, ThreadID: thread, Body: posted.Body,
	}
	if e.Body == "" {
		e.Body = body
	}
	return posted, c.recordAnswer(ctx, q, e)
}

// QuoteQuestion quotes the question's text in Markdown, line by line, as
// an answer that quotes it does (domain.IsAnswer).
func QuoteQuestion(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}
