package domain_test

import (
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestIsAnswer(t *testing.T) {
	item := ghItem(12)
	q := domain.PendingQuestion{
		On: item, CommentID: "100", AskedAt: t0,
		Text: "Does timeout mean 30 or 60 seconds?\nThe ticket says both.",
	}
	threaded := q
	threaded.Thread = "7"
	answer := func(edit func(*domain.ChangeEvent)) domain.ChangeEvent {
		e := domain.ChangeEvent{
			Ref: item, Kind: domain.ChangeCommentAdded, Actor: "lbianchi", At: t0.Add(time.Hour),
			Origin: domain.OriginHuman, CommentID: "101", Body: "@anthill 60 seconds",
		}
		if edit != nil {
			edit(&e)
		}
		return e
	}
	body := func(b string) func(*domain.ChangeEvent) { return func(e *domain.ChangeEvent) { e.Body = b } }
	tests := []struct {
		name string
		q    domain.PendingQuestion
		e    domain.ChangeEvent
		want bool
	}{
		{"mentions @anthill", q, answer(nil), true},
		{"mention in any case, mid-sentence", q, answer(body("Hi @AntHill, it is 60 seconds.")), true},
		{"quotes the question", q, answer(body("> Does timeout mean 30 or 60 seconds?\n\n60 seconds")), true},
		{"quotes one line, with other spacing", q, answer(body(">   the TICKET says   both.\nUse 60.")), true},
		{"review with a mention", q, answer(func(e *domain.ChangeEvent) { e.Kind = domain.ChangeReviewSubmitted }), true},
		{"reply in the question's thread", threaded, answer(func(e *domain.ChangeEvent) { e.ThreadID, e.Body = "7", "60 seconds" }), true},

		// Spec agent-conversation, "Unrelated comment".
		{"unrelated comment", q, answer(body("CI is flaky today.")), false},
		{"reply in another thread", threaded, answer(func(e *domain.ChangeEvent) { e.ThreadID, e.Body = "8", "60 seconds" }), false},
		{"thread reply where comments are not threaded", q, answer(func(e *domain.ChangeEvent) { e.ThreadID, e.Body = "7", "60 seconds" }), false},
		{"quote of something else", q, answer(body("> We should rename the package.\n\nAgreed.")), false},
		{"quote too short to tell", q, answer(body("> or\n\nYes")), false},
		{"longer login", q, answer(body("ping @anthill-bot: 60")), false},
		{"e-mail address", q, answer(body("ask me@anthill.dev")), false},
		{"mention only inside a quote", q, answer(body("> @anthill can you check?\n\nI will look.")), false},
		{"mention only in a code block", q, answer(body("```\n@anthill\n```")), false},
		{"no write access", q, answer(func(e *domain.ChangeEvent) { e.Actor = "ada" }), false},
		{"before the question", q, answer(func(e *domain.ChangeEvent) { e.At = t0.Add(-time.Minute) }), false},
		{"the question itself", q, answer(func(e *domain.ChangeEvent) { e.CommentID = "100" }), false},
		{"on another item", q, answer(func(e *domain.ChangeEvent) { e.Ref = ghItem(13) }), false},
		{"Anthill's own comment", q, answer(func(e *domain.ChangeEvent) { e.Origin = domain.OriginAnthill }), false},
		{"an edit", q, answer(func(e *domain.ChangeEvent) { e.Kind = domain.ChangeCommentEdited }), false},
		{"holds a command", q, answer(body("@anthill 60 seconds\n/anthill stop")), false},
		{"empty review", q, answer(func(e *domain.ChangeEvent) { e.Kind, e.Body = domain.ChangeReviewSubmitted, " " }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.IsAnswer(tt.q, tt.e, writers); got != tt.want {
				t.Errorf("IsAnswer = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindCommandAck(t *testing.T) {
	ack := domain.AnthillComment{
		Role: domain.RoleAnt, Body: "`/anthill stop`: stopped.",
		Marker: domain.Marker{Version: domain.MarkerVersion, ID: domain.CommandAckID("55"), Instance: "mrosani@LAPTOP-42", Kind: domain.CommentReply, Ref: "gh-12"},
	}
	other := ack
	other.Marker.ID = domain.CommandAckID("56")
	status := ack
	status.Marker.Kind = domain.CommentStatus
	comments := []domain.Comment{
		{ID: "55", Author: "lbianchi", Body: "/anthill stop"},
		{ID: "57", Author: "ada", Body: ack.String()},       // a forged marker
		{ID: "58", Author: "mrosani", Body: other.String()}, // another command's
		{ID: "59", Author: "mrosani", Body: status.String()},
	}
	if _, ok := domain.FindCommandAck(comments, "55", writers); ok {
		t.Fatal("found an acknowledgement in a forged or unrelated comment")
	}
	comments = append(comments, domain.Comment{ID: "60", Author: "mrosani", Body: ack.String()})
	c, ok := domain.FindCommandAck(comments, "55", writers)
	if !ok || c.ID != "60" {
		t.Errorf("FindCommandAck = %+v, %v; want comment 60", c, ok)
	}
}

func TestCommentsBefore(t *testing.T) {
	comments := []domain.Comment{
		{ID: "9", CreatedAt: t0},
		{ID: "10", CreatedAt: t0.Add(time.Minute)},
		{ID: "11", CreatedAt: t0.Add(time.Minute)},
		{ID: "12", CreatedAt: t0.Add(2 * time.Minute)},
	}
	got := domain.CommentsBefore(comments, comments[2])
	if len(got) != 2 || got[0].ID != "9" || got[1].ID != "10" {
		t.Errorf("CommentsBefore = %+v, want 9 and 10", got)
	}
}

func TestRenderCommandAck(t *testing.T) {
	got := domain.RenderCommandAck([]domain.CommandReply{{Name: domain.CmdStop, Text: "stopped. "}, {Name: "deploy", Text: "unknown"}})
	want := "`/anthill stop`: stopped.\n\n`/anthill deploy`: unknown"
	if got != want {
		t.Errorf("RenderCommandAck = %q, want %q", got, want)
	}
}

func TestUnreleaseAfter(t *testing.T) {
	claim := domain.Claim{Kind: domain.ClaimTask, Instance: "mrosani@LAPTOP-42", Ref: "gh-12", Lease: 15 * time.Minute, Released: true}
	released := claim.Comment(time.Time{}).String()
	comments := []domain.Comment{
		{ID: "1", Author: "mrosani", Body: released, CreatedAt: t0, UpdatedAt: t0.Add(time.Hour)},
		{ID: "2", Author: "mrosani", Body: released, CreatedAt: t0, UpdatedAt: t0.Add(30 * time.Minute)},
		{ID: "3", Author: "lbianchi", Body: "/anthill release", CreatedAt: t0, UpdatedAt: t0.Add(time.Hour)},
	}
	got := domain.UnreleaseAfter(comments, t0.Add(time.Hour))
	if m, _ := domain.FindMarker(got[0].Body); m.ClaimFields == nil || m.Released {
		t.Errorf("claim released at the time is still released: %+v", m)
	}
	if m, _ := domain.FindMarker(got[1].Body); m.ClaimFields == nil || !m.Released {
		t.Errorf("claim released before is no longer released: %+v", m)
	}
	if got[2].Body != comments[2].Body {
		t.Errorf("human comment changed: %q", got[2].Body)
	}
	if m, _ := domain.FindMarker(comments[0].Body); !m.Released {
		t.Error("the input was changed")
	}
}
