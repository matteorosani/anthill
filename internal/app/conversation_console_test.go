package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// withQueen sets where the queen's answers are posted.
func (e *convEnv) withQueen() *convEnv {
	*e.conv = *app.NewConversation(app.ConversationDeps{
		Store: e.store, Tracker: func(_ context.Context, _ domain.Ref) (ports.TrackerPort, error) { return e.gh.Client("mrosani"), nil },
		CodeHost: func(_ context.Context, _ domain.Repository) (ports.CodeHostPort, error) {
			return e.gh.Client("mrosani"), nil
		},
		Ants: &lazyAnts{sup: e.sup}, Clock: e.clock, Notifier: e.notifier,
		Queen: func(_ context.Context, _ domain.Ref) (ports.QueenCommentPort, error) {
			return e.gh.Client("mrosani"), nil
		},
	})
	return e
}

func (e *convEnv) answerFromConsole(id ports.AntID, text string) (domain.Comment, error) {
	e.t.Helper()
	var c domain.Comment
	var err error
	e.do(func() error {
		c, err = e.conv.AnswerFromConsole(e.ctx, id, text)
		return nil
	})
	return c, err
}

// Spec queen-console, "Answer from the console": the answer is posted as
// the queen's own comment where the question was asked, and a new run
// resumes the session with it. When the next poll delivers that comment
// as a change, it starts no second run.
func TestAnswerFromTheConsole(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2).withQueen()
		id, x := e.startAnt(12)
		e.ask(x, "Does timeout mean 30 or 60 seconds?")

		posted, err := e.answerFromConsole(id, "60 seconds.")
		if err != nil {
			t.Fatal(err)
		}
		item, _ := e.gh.Item(ghRef(12))
		last := item.Comments[len(item.Comments)-1]
		if last.ID != posted.ID || last.Author != "mrosani" || last.Body != "> Does timeout mean 30 or 60 seconds?\n\n60 seconds." {
			t.Errorf("posted = %+v", last)
		}
		if _, marked := domain.FindMarker(last.Body); marked || strings.Contains(last.Body, "🐜") {
			t.Error("the queen's answer carries Anthill's marker or prefix")
		}
		q := e.question(id)
		if q.AnswerID != posted.ID || q.Answer == nil || q.Answer.Actor != "mrosani" || q.Answer.Origin != domain.OriginHuman {
			t.Errorf("question = %+v", q)
		}
		e.wantState(id, domain.AntRunning)
		resumed := e.execOf(ghRef(12))
		if resumed == x || resumed.Spec.Session != "sess-gh-12" {
			t.Fatalf("run = %+v, want a new run resuming sess-gh-12", resumed.Spec)
		}
		if b := e.briefs.last(); b.kind != "resume" || b.answer == nil || b.answer.CommentID != posted.ID {
			t.Errorf("brief = %+v", b)
		}

		// The poll that sees the comment hands it to the conversation as a
		// human change: it is the stored answer, and no second run starts.
		ev := domain.ChangeEvent{
			Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: last.Author, At: last.CreatedAt,
			Origin: last.Origin(), CommentID: last.ID, Body: last.Body,
		}
		pending := domain.PendingQuestion{On: q.On, CommentID: q.CommentID, Text: q.Text, AskedAt: q.AskedAt}
		if !domain.IsAnswer(pending, ev, domain.NewUserSet("mrosani")) {
			t.Error("answer detection does not recognise the posted answer")
		}
		if !e.answer(ev) {
			t.Error("the polled answer was not handled as the answer")
		}
		if n := len(e.exec.Executions()); n != 2 {
			t.Errorf("%d executions, want 2", n)
		}
	})
}

// On a threaded provider the answer is a reply in the question's thread,
// without a quote.
func TestAnswerFromTheConsoleInThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2).withQueen()
		id, x := e.startAnt(12)
		e.ask(x, "Which status code?")
		q := e.question(id)
		q.Thread = "7"
		if err := e.store.SaveQuestion(e.ctx, q); err != nil {
			t.Fatal(err)
		}
		if _, err := e.answerFromConsole(id, "410 Gone"); err != nil {
			t.Fatal(err)
		}
		item, _ := e.gh.Item(ghRef(12))
		last := item.Comments[len(item.Comments)-1]
		if last.Body != "410 Gone" || last.Thread != "7" {
			t.Errorf("posted = %+v", last)
		}
		if q := e.question(id); q.Answer == nil || q.Answer.ThreadID != "7" {
			t.Errorf("answer = %+v", q.Answer)
		}
		e.wantState(id, domain.AntRunning)
	})
}

func TestAnswerFromTheConsoleRefusals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		id, x := e.startAnt(12)
		if _, err := e.withQueen().answerFromConsole(id, "60 seconds."); !errors.As(err, new(*app.NoPendingQuestionError)) ||
			err.Error() != "the ant for #12 has no pending question" {
			t.Errorf("answer without question = %v", err)
		}
		e.ask(x, "Which timeout?")
		for _, text := range []string{"  ", "/anthill stop"} {
			if _, err := e.answerFromConsole(id, text); err == nil {
				t.Errorf("answer %q was accepted", text)
			}
		}
		e.wantState(id, domain.AntAwaitingInput)
		item, _ := e.gh.Item(ghRef(12))
		for _, c := range item.Comments {
			if c.Author == "mrosani" && !strings.Contains(c.Body, "<!--") {
				t.Errorf("a refused answer was posted: %q", c.Body)
			}
		}
	})
}
