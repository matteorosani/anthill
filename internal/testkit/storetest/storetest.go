// Package storetest holds the contract tests every ports.StateStore must
// pass. The in-memory store in package testkit and the SQLite store both
// run them, so that tests using the fake can trust it behaves like the
// real store.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Open returns a new, empty store for one test.
type Open func(t *testing.T) ports.StateStore

// T0 is the time the contract tests start from. Times are in UTC, as the
// stores keep them.
var T0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

var repo = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}

// Item returns the ref of item n in the test repository.
func Item(n int) domain.Ref {
	return domain.Ref{Repo: repo, Kind: domain.KindItem, Number: n}
}

// PR returns the ref of PR n in the test repository.
func PR(n int) domain.Ref {
	return domain.Ref{Repo: repo, Kind: domain.KindPR, Number: n}
}

// Run runs every contract test against stores made by open.
func Run(t *testing.T, open Open) {
	tests := []struct {
		name string
		test func(*testing.T, ports.StateStore)
	}{
		{"SessionIndex", testSessionIndex},
		{"RunLog", testRunLog},
		{"PollState", testPollState},
		{"Outbox", testOutbox},
		{"StoredChanges", testStoredChanges},
		{"Questions", testQuestions},
		{"Extensions", testExtensions},
		{"Proposals", testProposals},
		{"HeldClaims", testHeldClaims},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					f, ok := r.(failure)
					if !ok {
						panic(r)
					}
					t.Fatal(f.err)
				}
			}()
			tc.test(t, open(t))
		})
	}
}

// must returns v, or stops the contract test on an error: run recovers
// the panic and fails the test.
func must[T any](v T, err error) T {
	if err != nil {
		panic(failure{err})
	}
	return v
}

type failure struct{ err error }

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func equal(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s =\n%+v\nwant\n%+v", what, got, want)
	}
}

func notFound(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("%s: error = %v, want ErrNotFound", what, err)
	}
}

// Ant returns an ant record for item n in state, created at T0.
func Ant(n int, states ...domain.AntState) ports.AntRecord {
	policy := domain.DefaultBudgetPolicy()
	r := ports.AntRecord{
		Item:     Item(n),
		Base:     time.Hour,
		Budget:   domain.NewBudget(time.Hour, policy),
		Runner:   "claude",
		Worktree: fmt.Sprintf("/work/gh-%d", n),
		Branch:   fmt.Sprintf("ant/gh-%d-fix", n),
	}
	from := domain.AntState("")
	for i, s := range states {
		r.History = append(r.History, domain.Transition{From: from, To: s, At: T0.Add(time.Duration(i) * time.Minute), Reason: "test"})
		from = s
	}
	return r
}

func testSessionIndex(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	first := Ant(1, domain.AntStarting, domain.AntRunning)
	first.Budget.Used = 5 * time.Minute
	first.Budget.CountingSince = T0.Add(time.Minute)
	first.Budget.Open = &domain.ExtensionRequest{Minutes: 10, Reason: "tests", At: T0, Status: domain.ExtensionPending}
	id1 := must(s.CreateAnt(ctx, first))
	first.ID = id1

	got := must(s.Ant(ctx, id1))
	equal(t, "Ant", got, first)
	if got.State() != domain.AntRunning {
		t.Errorf("State() = %s", got.State())
	}

	// Saving appends transitions and changes fields.
	pr := PR(7)
	first.History = append(first.History, domain.Transition{From: domain.AntRunning, To: domain.AntSucceeded, At: T0.Add(time.Hour), Reason: "finished"})
	first.SessionID, first.PR, first.Budget.Open = "session-1", &pr, nil
	check(t, s.SaveAnt(ctx, first))
	equal(t, "saved Ant", must(s.Ant(ctx, id1)), first)

	shorter := first
	shorter.History = first.History[:1]
	if err := s.SaveAnt(ctx, shorter); err == nil {
		t.Error("SaveAnt accepted a shorter history")
	}
	notFound(t, "SaveAnt of a missing ant", s.SaveAnt(ctx, ports.AntRecord{ID: 99, History: first.History}))
	_, err := s.Ant(ctx, 99)
	notFound(t, "Ant(99)", err)

	// A later ant of the same item becomes the current one; the earlier
	// one is kept.
	check(t, s.SaveAnt(ctx, withState(first, domain.AntAbandoned)))
	second := Ant(1, domain.AntQueued)
	id2 := must(s.CreateAnt(ctx, second))
	if id2 <= id1 {
		t.Errorf("second ID %d is not after %d", id2, id1)
	}
	if cur := must(s.CurrentAnt(ctx, Item(1))); cur.ID != id2 {
		t.Errorf("CurrentAnt = %d, want %d", cur.ID, id2)
	}
	if old := must(s.Ant(ctx, id1)); old.State() != domain.AntAbandoned {
		t.Errorf("earlier ant state = %s", old.State())
	}
	if b := must(s.AntByBranch(ctx, second.Branch)); b.ID != id2 {
		t.Errorf("AntByBranch = %d, want %d", b.ID, id2)
	}
	_, err = s.CurrentAnt(ctx, Item(9))
	notFound(t, "CurrentAnt(9)", err)
	_, err = s.AntByBranch(ctx, "ant/none")
	notFound(t, "AntByBranch", err)

	id3 := must(s.CreateAnt(ctx, Ant(2, domain.AntStarting, domain.AntRunning)))
	ids := func(rs []ports.AntRecord) []ports.AntID {
		var out []ports.AntID
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	equal(t, "Ants()", ids(must(s.Ants(ctx))), []ports.AntID{id2, id3})
	equal(t, "Ants(running)", ids(must(s.Ants(ctx, domain.AntRunning))), []ports.AntID{id3})
	equal(t, "Ants(abandoned)", ids(must(s.Ants(ctx, domain.AntAbandoned))), []ports.AntID(nil))
}

func withState(r ports.AntRecord, to domain.AntState) ports.AntRecord {
	last := r.History[len(r.History)-1]
	r.History = append(append([]domain.Transition(nil), r.History...), domain.Transition{From: last.To, To: to, At: last.At.Add(time.Minute), Reason: "test"})
	return r
}

func testRunLog(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	ant := must(s.CreateAnt(ctx, Ant(1, domain.AntStarting)))
	run := ports.RunRecord{Ant: ant, Kind: ports.RunTask, StartedAt: T0}
	run.ID = must(s.StartRun(ctx, run))
	equal(t, "active run", must(s.Runs(ctx, ant)), []ports.RunRecord{run})
	if d := run.Duration(); d != 0 {
		t.Errorf("active Duration() = %v", d)
	}

	tokens, cost := int64(1_200_000), 3.40
	run.EndedAt, run.Outcome, run.Reason, run.SessionID = T0.Add(40*time.Minute), ports.OutcomeComplete, "finish", "session-1"
	run.Usage = ports.Usage{Tokens: &tokens, CostUSD: &cost}
	check(t, s.EndRun(ctx, run))
	runs := must(s.Runs(ctx, ant))
	equal(t, "ended run", runs, []ports.RunRecord{run})
	if runs[0].Duration() != 40*time.Minute {
		t.Errorf("Duration() = %v", runs[0].Duration())
	}

	// Unknown usage stays unknown.
	unknown := ports.RunRecord{Ant: ant, Kind: ports.RunFollowUp, StartedAt: T0.Add(24 * time.Hour)}
	unknown.ID = must(s.StartRun(ctx, unknown))
	unknown.EndedAt, unknown.Outcome = unknown.StartedAt.Add(time.Minute), ports.OutcomeFailed
	check(t, s.EndRun(ctx, unknown))
	got := must(s.Runs(ctx, ant))
	if len(got) != 2 || got[1].Usage.Tokens != nil || got[1].Usage.CostUSD != nil {
		t.Errorf("run without usage = %+v", got)
	}
	notFound(t, "EndRun of a missing run", s.EndRun(ctx, ports.RunRecord{ID: 99}))

	round := ports.RoundRecord{PR: PR(7), Head: "abc", Requested: true, StartedAt: T0.Add(time.Hour)}
	round.ID = must(s.StartRound(ctx, round))
	round.EndedAt, round.Usage = T0.Add(70*time.Minute), ports.Usage{CostUSD: &cost}
	check(t, s.EndRound(ctx, round))
	equal(t, "Rounds", must(s.Rounds(ctx, PR(7))), []ports.RoundRecord{round})
	equal(t, "Rounds of another PR", must(s.Rounds(ctx, PR(8))), []ports.RoundRecord(nil))
	notFound(t, "EndRound of a missing round", s.EndRound(ctx, ports.RoundRecord{ID: 99}))

	runs, rounds, err := s.Usage(ctx, T0, T0.Add(24*time.Hour))
	check(t, err)
	equal(t, "Usage runs", runs, []ports.RunRecord{run})
	equal(t, "Usage rounds", rounds, []ports.RoundRecord{round})
}

func testPollState(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	equal(t, "never polled", must(s.PollStatus(ctx, "github")), ports.PollStatus{Connection: "github"})
	equal(t, "no hashes", len(must(s.Hashes(ctx, Item(1)))), 0)

	event := domain.ChangeEvent{Ref: Item(1), Kind: domain.ChangeCommentAdded, Actor: "dev", At: T0, Origin: domain.OriginHuman, CommentID: "5", Body: "hi"}
	check(t, s.RecordPoll(ctx, ports.PollResult{
		Connection: "github", Cursor: "c1", At: T0,
		Hashes: []ports.Hash{{Ref: Item(1), Group: "body", Value: "h1"}, {Ref: Item(1), Group: "comments", Value: "h2"}},
		Events: []domain.ChangeEvent{event},
	}))
	equal(t, "hashes", must(s.Hashes(ctx, Item(1))), map[string]string{"body": "h1", "comments": "h2"})
	equal(t, "status", must(s.PollStatus(ctx, "github")), ports.PollStatus{Connection: "github", Cursor: "c1", LastSuccess: T0})
	pending := must(s.Pending(ctx))
	if len(pending) != 1 || !reflect.DeepEqual(pending[0].Event, event) || !pending[0].DetectedAt.Equal(T0) {
		t.Errorf("Pending() = %+v", pending)
	}

	// A failure keeps the cursor; a success clears the error.
	check(t, s.RecordPollFailure(ctx, "github", T0.Add(time.Minute), "rate limited", T0.Add(time.Hour)))
	check(t, s.RecordPollFailure(ctx, "ado", T0.Add(time.Minute), "503", time.Time{}))
	equal(t, "statuses", must(s.PollStatuses(ctx)), []ports.PollStatus{
		{Connection: "ado", LastError: "503", LastErrorAt: T0.Add(time.Minute)},
		{Connection: "github", Cursor: "c1", LastSuccess: T0, LastError: "rate limited", LastErrorAt: T0.Add(time.Minute), RateLimitedUntil: T0.Add(time.Hour)},
	})
	check(t, s.RecordPoll(ctx, ports.PollResult{Connection: "github", Cursor: "c2", At: T0.Add(2 * time.Hour), Hashes: []ports.Hash{{Ref: Item(1), Group: "body", Value: "h3"}}}))
	equal(t, "status after success", must(s.PollStatus(ctx, "github")), ports.PollStatus{Connection: "github", Cursor: "c2", LastSuccess: T0.Add(2 * time.Hour)})
	equal(t, "updated hashes", must(s.Hashes(ctx, Item(1))), map[string]string{"body": "h3", "comments": "h2"})

	// Texts are kept next to their hash, replaced with it, and dropped
	// when the new hash has none.
	equal(t, "no texts", len(must(s.Texts(ctx, Item(2)))), 0)
	check(t, s.RecordPoll(ctx, ports.PollResult{Connection: "github", Cursor: "c3", At: T0.Add(3 * time.Hour), Hashes: []ports.Hash{
		{Ref: Item(2), Group: "title", Value: "t1", Text: "Fix login"},
		{Ref: Item(2), Group: "body", Value: "b1", Text: "Steps\nmore"},
		{Ref: Item(2), Group: "state", Value: "s1"},
	}}))
	equal(t, "texts", must(s.Texts(ctx, Item(2))), map[string]string{"title": "Fix login", "body": "Steps\nmore"})
	check(t, s.RecordPoll(ctx, ports.PollResult{Connection: "github", Cursor: "c4", At: T0.Add(4 * time.Hour), Hashes: []ports.Hash{
		{Ref: Item(2), Group: "title", Value: "t2", Text: "Fix login on Safari"},
		{Ref: Item(2), Group: "body", Value: "b2"},
	}}))
	equal(t, "updated texts", must(s.Texts(ctx, Item(2))), map[string]string{"title": "Fix login on Safari"})
	equal(t, "texts of another ref", len(must(s.Texts(ctx, Item(1)))), 0)
}

func testOutbox(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	events := []domain.ChangeEvent{
		{Ref: Item(1), Kind: domain.ChangeLabelAdded, Label: "anthill", At: T0},
		{Ref: PR(2), Kind: domain.ChangeReviewSubmitted, Review: domain.ReviewChangesRequested, At: T0},
	}
	check(t, s.RecordPoll(ctx, ports.PollResult{Connection: "github", At: T0, Events: events[:1]}))
	check(t, s.RecordPoll(ctx, ports.PollResult{Connection: "github", At: T0.Add(time.Minute), Events: events[1:]}))
	pending := must(s.Pending(ctx))
	if len(pending) != 2 || pending[0].ID >= pending[1].ID {
		t.Fatalf("Pending() = %+v", pending)
	}
	equal(t, "first event", pending[0].Event, events[0])
	equal(t, "second event", pending[1].Event, events[1])

	check(t, s.Done(ctx, pending[0].ID, T0.Add(time.Hour)))
	check(t, s.Done(ctx, pending[0].ID, T0.Add(time.Hour)))
	left := must(s.Pending(ctx))
	if len(left) != 1 || left[0].ID != pending[1].ID {
		t.Errorf("Pending() after Done = %+v", left)
	}
	notFound(t, "Done of a missing event", s.Done(ctx, 99, T0))
}

func testStoredChanges(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	ant := must(s.CreateAnt(ctx, Ant(1, domain.AntStarting, domain.AntRunning, domain.AntAwaitingInput)))
	other := must(s.CreateAnt(ctx, Ant(2, domain.AntQueued)))
	edit := domain.ChangeEvent{Ref: Item(1), Kind: domain.ChangeItemEdited, Field: domain.FieldBody, At: T0}
	check(t, s.StoreChange(ctx, ports.StoredChange{Ant: ant, Source: 3, Event: edit, StoredAt: T0}))
	// The same source again is a no-op: the outbox handler is idempotent.
	check(t, s.StoreChange(ctx, ports.StoredChange{Ant: ant, Source: 3, Event: edit, StoredAt: T0.Add(time.Minute)}))
	check(t, s.StoreChange(ctx, ports.StoredChange{Ant: other, Event: edit, StoredAt: T0}))
	changes := must(s.StoredChanges(ctx, ant))
	if len(changes) != 1 || changes[0].Source != 3 || !reflect.DeepEqual(changes[0].Event, edit) || !changes[0].StoredAt.Equal(T0) {
		t.Fatalf("StoredChanges() = %+v", changes)
	}
	check(t, s.ConsumeChanges(ctx, []int64{changes[0].ID}, T0.Add(time.Hour)))
	equal(t, "after consume", must(s.StoredChanges(ctx, ant)), []ports.StoredChange(nil))
	if n := len(must(s.StoredChanges(ctx, other))); n != 1 {
		t.Errorf("other ant has %d changes", n)
	}
	notFound(t, "ConsumeChanges of a missing change", s.ConsumeChanges(ctx, []int64{99}, T0))
}

func testQuestions(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	ant := must(s.CreateAnt(ctx, Ant(1, domain.AntAwaitingInput)))
	q1 := ports.QuestionRecord{CommentID: "10", Ant: ant, On: Item(1), AskedAt: T0.Add(time.Hour)}
	q2 := ports.QuestionRecord{CommentID: "11", Ant: ant, On: PR(2), AskedAt: T0}
	check(t, s.SaveQuestion(ctx, q1))
	check(t, s.SaveQuestion(ctx, q2))
	equal(t, "pending", must(s.PendingQuestions(ctx)), []ports.QuestionRecord{q2, q1})

	q2.StaleAt = T0.Add(72 * time.Hour)
	check(t, s.SaveQuestion(ctx, q2))
	q1.AnsweredAt, q1.AnswerID = T0.Add(2*time.Hour), "12"
	check(t, s.SaveQuestion(ctx, q1))
	equal(t, "pending after answer", must(s.PendingQuestions(ctx)), []ports.QuestionRecord{q2})
}

func testExtensions(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	ant := must(s.CreateAnt(ctx, Ant(1, domain.AntAwaitingExtension)))
	r := ports.ExtensionRecord{Ant: ant, Request: domain.ExtensionRequest{Minutes: 20, Reason: "tests are slow", At: T0, Status: domain.ExtensionPending}}
	r.ID = must(s.SaveExtension(ctx, r))
	equal(t, "pending", must(s.PendingExtensions(ctx)), []ports.ExtensionRecord{r})

	r.Request.Status, r.Request.Granted, r.Request.Message, r.DecidedAt = domain.ExtensionReduced, 10, "10 minutes", T0.Add(time.Minute)
	if id := must(s.SaveExtension(ctx, r)); id != r.ID {
		t.Errorf("update returned ID %d, want %d", id, r.ID)
	}
	equal(t, "pending after decision", must(s.PendingExtensions(ctx)), []ports.ExtensionRecord(nil))
	equal(t, "Extensions", must(s.Extensions(ctx, ant)), []ports.ExtensionRecord{r})
	_, err := s.SaveExtension(ctx, ports.ExtensionRecord{ID: 99, Ant: ant})
	notFound(t, "update of a missing request", err)
}

func testProposals(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	declined := ports.HiddenProposal{Item: Item(1), HiddenAt: T0, ItemHash: "h1"}
	snoozed := ports.HiddenProposal{Item: Item(2), HiddenAt: T0.Add(time.Minute), Until: T0.Add(24 * time.Hour), ItemHash: "h2"}
	check(t, s.Hide(ctx, snoozed))
	check(t, s.Hide(ctx, declined))
	equal(t, "Hidden", must(s.Hidden(ctx)), []ports.HiddenProposal{declined, snoozed})

	declined.ItemHash, declined.HiddenAt = "h3", T0.Add(time.Hour)
	check(t, s.Hide(ctx, declined))
	check(t, s.Unhide(ctx, Item(2)))
	check(t, s.Unhide(ctx, Item(9)))
	equal(t, "Hidden after changes", must(s.Hidden(ctx)), []ports.HiddenProposal{declined})
}

func testHeldClaims(t *testing.T, s ports.StateStore) {
	ctx := context.Background()
	task := ports.HeldClaim{Ref: Item(2), Kind: domain.ClaimTask, Instance: "mrosani@LAPTOP-42", CommentID: "100",
		Label: domain.DefaultClaimLabel, Assigned: true, Lease: domain.DefaultLease, UpdatedAt: T0}
	review := ports.HeldClaim{Ref: PR(3), Kind: domain.ClaimReview, Instance: "mrosani@LAPTOP-42", CommentID: "200",
		Label: domain.DefaultClaimLabel, Lease: domain.DefaultLease, UpdatedAt: T0.Add(time.Minute)}
	other := ports.HeldClaim{Ref: Item(1), Kind: domain.ClaimTask, Instance: "mrosani@LAPTOP-42", CommentID: "50",
		Label: "anthill:mine", Lease: time.Hour, UpdatedAt: T0}
	check(t, s.SaveClaim(ctx, review))
	check(t, s.SaveClaim(ctx, task))
	check(t, s.SaveClaim(ctx, other))
	equal(t, "HeldClaims", must(s.HeldClaims(ctx)), []ports.HeldClaim{other, task, review})

	task.Parked, task.UpdatedAt = true, T0.Add(5*time.Minute)
	check(t, s.SaveClaim(ctx, task))
	check(t, s.DeleteClaim(ctx, Item(1), domain.ClaimTask))
	check(t, s.DeleteClaim(ctx, Item(9), domain.ClaimTask))
	check(t, s.DeleteClaim(ctx, PR(3), domain.ClaimTask))
	equal(t, "after changes", must(s.HeldClaims(ctx)), []ports.HeldClaim{task, review})
}
