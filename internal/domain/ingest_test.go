package domain_test

import (
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

var (
	ingestT0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	optIn    = []string{"anthill"}
)

// store applies what a poll found to the stored content, as the local
// store does.
func store(prev domain.StoredContent, in domain.Ingested) domain.StoredContent {
	next := domain.StoredContent{Hashes: maps.Clone(prev.Hashes), Texts: maps.Clone(prev.Texts)}
	if next.Hashes == nil {
		next.Hashes, next.Texts = map[string]string{}, map[string]string{}
	}
	for _, h := range in.Hashes {
		next.Hashes[h.Group] = h.Value
		next.Texts[h.Group] = h.Text
	}
	return next
}

func issue12() domain.WorkItem {
	return domain.WorkItem{
		Ref: ghItem(12), Author: "reporter", Title: "Fix login timeout", Body: "Steps…",
		Type: domain.TypeBug, State: domain.StateOpen, Labels: []string{"anthill", "bug"},
		Assignees: []string{"mrosani"}, Dependencies: []domain.Ref{ghItem(10)},
		Comments: []domain.Comment{{ID: "1", Author: "reporter", Body: "Also on Safari.", CreatedAt: ingestT0, UpdatedAt: ingestT0}},
		Revision: "1", UpdatedAt: ingestT0,
	}
}

// baseline ingests the item once and returns what the store keeps.
func baseline(t *testing.T, item domain.WorkItem) domain.StoredContent {
	t.Helper()
	first := domain.IngestItem(domain.StoredContent{}, item, optIn)
	if len(first.Events) != 1 || first.Events[0].Kind != domain.ChangeItemIngested {
		t.Fatalf("first poll events = %+v", first.Events)
	}
	return store(domain.StoredContent{}, first)
}

func kinds(events []domain.ChangeEvent) []domain.ChangeKind {
	out := []domain.ChangeKind{}
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// Scenario "New comment on a ticket": exactly one comment-added event,
// with the comment's author and body.
func TestIngestNewCommentOnATicket(t *testing.T) {
	item := issue12()
	prev := baseline(t, item)
	later := ingestT0.Add(time.Minute)
	item.Comments = append(item.Comments, domain.Comment{ID: "2", Author: "dev", Body: "Please also handle the empty password.", CreatedAt: later, UpdatedAt: later})
	item.Revision, item.UpdatedAt = "2", later

	got := domain.IngestItem(prev, item, optIn)
	want := []domain.ChangeEvent{{
		Ref: ghItem(12), Kind: domain.ChangeCommentAdded, Actor: "dev", At: later, Origin: domain.OriginHuman,
		CommentID: "2", Body: "Please also handle the empty password.",
	}}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("events = %+v, want %+v", got.Events, want)
	}
	if len(got.Hashes) != 1 || got.Hashes[0].Group != domain.CommentGroup("2") {
		t.Errorf("hashes = %+v, want only the new comment's", got.Hashes)
	}
}

// Scenario "No change": the same revision and content emit nothing and
// store nothing, and neither does a new revision with the same content.
func TestIngestNoChange(t *testing.T) {
	item := issue12()
	prev := baseline(t, item)
	if got := domain.IngestItem(prev, item, optIn); len(got.Events) != 0 || len(got.Hashes) != 0 {
		t.Errorf("unchanged item: %+v", got)
	}
	item.Revision, item.UpdatedAt = "7", ingestT0.Add(time.Hour)
	item.Labels = []string{"Bug", "ANTHILL"} // order and case do not matter
	item.Assignees = []string{"MRosani"}
	if got := domain.IngestItem(prev, item, optIn); len(got.Events) != 0 || len(got.Hashes) != 0 {
		t.Errorf("new revision, same content: %+v", got)
	}
}

// Scenario "Anthill's own comment": a new comment with the marker is
// flagged as Anthill-authored.
func TestIngestAnthillsOwnComment(t *testing.T) {
	item := issue12()
	prev := baseline(t, item)
	status := domain.AnthillComment{Role: domain.RoleAnt, Body: "Working on it.",
		Marker: domain.Marker{Version: 1, Instance: "mrosani@LAPTOP-42", Kind: domain.CommentStatus, Ref: "gh-12"}}
	item.Comments = append(item.Comments, domain.Comment{ID: "2", Author: "mrosani", Body: status.String()})
	got := domain.IngestItem(prev, item, optIn)
	if len(got.Events) != 1 || got.Events[0].Origin != domain.OriginAnthill {
		t.Errorf("events = %+v, want one Anthill-authored comment", got.Events)
	}
}

// Scenario "Developer comments from the same account": the developer
// whose credentials Anthill uses comments by hand on an ant PR. The
// comment has no marker, so it is human, although the same account
// wrote Anthill's comment before it.
func TestIngestDeveloperCommentsFromTheSameAccount(t *testing.T) {
	pr := domain.PullRequest{
		Ref: domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}, Title: "Fix login timeout",
		Body: "Closes #12", Author: "mrosani", HeadCommit: "abc", Item: ptr(ghItem(12)),
	}
	prev := store(domain.StoredContent{}, domain.IngestPullRequest(domain.StoredContent{}, pr))
	summary := domain.AnthillComment{Role: domain.RoleAnt, Body: "Done.",
		Marker: domain.Marker{Version: 1, Instance: "mrosani@LAPTOP-42", Kind: domain.CommentSummary, Ref: "gh-pr-34"}}
	pr.Comments = []domain.Comment{
		{ID: "5", Author: "mrosani", Body: summary.String()},
		{ID: "6", Author: "mrosani", Body: "I tried it, the timeout is still 30 s."},
	}
	got := domain.IngestPullRequest(prev, pr)
	if len(got.Events) != 2 {
		t.Fatalf("events = %+v", got.Events)
	}
	if got.Events[0].Origin != domain.OriginAnthill || got.Events[1].Origin != domain.OriginHuman {
		t.Errorf("origins = %q, %q, want anthill, human", got.Events[0].Origin, got.Events[1].Origin)
	}
	if got.Events[1].Actor != "mrosani" || got.Events[1].CommentID != "6" {
		t.Errorf("hand-typed comment = %+v", got.Events[1])
	}
}

// Scenario "Ticket without opt-in label": an item never seen without
// the label is ignored, and nothing is stored for it.
func TestIngestTicketWithoutOptInLabel(t *testing.T) {
	item := issue12()
	item.Labels = []string{"bug"}
	if got := domain.IngestItem(domain.StoredContent{}, item, optIn); len(got.Events) != 0 || len(got.Hashes) != 0 {
		t.Errorf("item without opt-in label: %+v", got)
	}
}

// Scenario "Opt-in label added later": the item is ingested at the next
// poll with one item-ingested event, and its existing comments become the
// baseline without events.
func TestIngestOptInLabelAddedLater(t *testing.T) {
	item := issue12()
	item.Labels = []string{"bug"}
	nothing := domain.IngestItem(domain.StoredContent{}, item, optIn)

	item.Labels = []string{"bug", "Anthill"}
	got := domain.IngestItem(store(domain.StoredContent{}, nothing), item, optIn)
	if !reflect.DeepEqual(kinds(got.Events), []domain.ChangeKind{domain.ChangeItemIngested}) {
		t.Fatalf("events = %+v", got.Events)
	}
	if e := got.Events[0]; e.Ref != ghItem(12) || e.Origin != domain.OriginHuman || !e.At.Equal(ingestT0) {
		t.Errorf("event = %+v", e)
	}
	prev := store(domain.StoredContent{}, got)
	if again := domain.IngestItem(prev, item, optIn); len(again.Events) != 0 {
		t.Errorf("second poll events = %+v", again.Events)
	}
}

// Losing the opt-in label emits only its label-removed event (spec
// change-reaction, "Withdrawal of opt-in"); later changes emit nothing
// until the label returns, which ingests the item again.
func TestIngestOptInLabelRemovedAndAddedAgain(t *testing.T) {
	item := issue12()
	prev := baseline(t, item)

	item.Labels = []string{"bug", "wontfix"}
	item.Title = "Fix login timeout on Safari"
	got := domain.IngestItem(prev, item, optIn)
	want := []domain.ChangeEvent{{Ref: ghItem(12), Kind: domain.ChangeLabelRemoved, Label: "anthill", At: ingestT0, Origin: domain.OriginHuman}}
	if !reflect.DeepEqual(got.Events, want) {
		t.Fatalf("events = %+v, want %+v", got.Events, want)
	}
	prev = store(prev, got)

	item.Body = "New steps"
	item.Comments = append(item.Comments, domain.Comment{ID: "2", Author: "dev", Body: "ping"})
	got = domain.IngestItem(prev, item, optIn)
	if len(got.Events) != 0 || len(got.Hashes) == 0 {
		t.Errorf("changes while not opted in: %+v, want hashes and no events", got)
	}
	prev = store(prev, got)

	item.Labels = append(item.Labels, "anthill")
	got = domain.IngestItem(prev, item, optIn)
	if !reflect.DeepEqual(kinds(got.Events), []domain.ChangeKind{domain.ChangeItemIngested}) {
		t.Errorf("label added again: events = %+v", got.Events)
	}
}

// An Azure DevOps project can back several repository entries; any of
// their opt-in labels opts an item in.
func TestIngestAnyOptInLabel(t *testing.T) {
	item := issue12()
	item.Labels = []string{"AI"}
	got := domain.IngestItem(domain.StoredContent{}, item, []string{"anthill", "ai"})
	if !reflect.DeepEqual(kinds(got.Events), []domain.ChangeKind{domain.ChangeItemIngested}) {
		t.Errorf("events = %+v", got.Events)
	}
}

// Each field group of an opted-in item becomes its own typed event, in a
// fixed order, with its payload.
func TestIngestItemEvents(t *testing.T) {
	item := issue12()
	prev := baseline(t, item)
	later := ingestT0.Add(time.Hour)
	item.Title = "Fix login timeout on Safari"
	item.Body = "Steps…\n\n- [ ] works on Safari"
	item.AcceptanceCriteria = "Login works"
	item.State = domain.StateClosedNotPlanned
	item.Labels = []string{"anthill", "anthill:hold"}
	item.Assignees = nil
	item.Children = []domain.Ref{ghItem(20)}
	item.Comments[0].Body, item.Comments[0].UpdatedAt = "Also on Safari 17.", later
	item.UpdatedAt = later

	got := domain.IngestItem(prev, item, optIn)
	base := domain.ChangeEvent{Ref: ghItem(12), At: later, Origin: domain.OriginHuman}
	with := func(f func(*domain.ChangeEvent)) domain.ChangeEvent {
		e := base
		f(&e)
		return e
	}
	want := []domain.ChangeEvent{
		with(func(e *domain.ChangeEvent) {
			e.Kind, e.Field, e.Old, e.Body = domain.ChangeItemEdited, domain.FieldTitle, "Fix login timeout", "Fix login timeout on Safari"
		}),
		with(func(e *domain.ChangeEvent) {
			e.Kind, e.Field, e.Old, e.Body = domain.ChangeItemEdited, domain.FieldBody, "Steps…", item.Body
		}),
		with(func(e *domain.ChangeEvent) {
			e.Kind, e.Field, e.Body = domain.ChangeItemEdited, domain.FieldAcceptanceCriteria, "Login works"
		}),
		with(func(e *domain.ChangeEvent) { e.Kind, e.State = domain.ChangeStateChanged, "closed-not-planned" }),
		with(func(e *domain.ChangeEvent) { e.Kind, e.Label = domain.ChangeLabelRemoved, "bug" }),
		with(func(e *domain.ChangeEvent) { e.Kind, e.Label = domain.ChangeLabelAdded, "anthill:hold" }),
		with(func(e *domain.ChangeEvent) { e.Kind = domain.ChangeAssigneesChanged }),
		with(func(e *domain.ChangeEvent) { e.Kind = domain.ChangeLinksChanged }),
		with(func(e *domain.ChangeEvent) {
			e.Kind, e.Actor, e.CommentID, e.Body = domain.ChangeCommentEdited, "reporter", "1", "Also on Safari 17."
		}),
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got.Events, want)
	}
}

func TestIngestPullRequestEvents(t *testing.T) {
	ref := domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}
	pr := domain.PullRequest{
		Ref: ref, Title: "Fix login", Body: "Closes #12", Author: "mrosani", HeadCommit: "abc", Draft: true,
		Threads: []domain.ReviewThread{{ID: "T1", Path: "login.go", Line: 3, Comments: []domain.Comment{{ID: "r1", Author: "dev", Body: "Why 30?"}}}},
	}
	first := domain.IngestPullRequest(domain.StoredContent{}, pr)
	if !reflect.DeepEqual(kinds(first.Events), []domain.ChangeKind{domain.ChangePRIngested}) {
		t.Fatalf("first poll events = %+v", first.Events)
	}
	prev := store(domain.StoredContent{}, first)
	if again := domain.IngestPullRequest(prev, pr); len(again.Events) != 0 || len(again.Hashes) != 0 {
		t.Errorf("unchanged PR: %+v", again)
	}

	later := ingestT0.Add(time.Hour)
	pr.Body, pr.Draft, pr.HeadCommit, pr.Labels = "Closes #12\n\nAlso fixes Safari.", false, "def", []string{"anthill:review"}
	pr.Threads[0].Comments = append(pr.Threads[0].Comments, domain.Comment{ID: "r2", Author: "dev", Body: "And here?", CreatedAt: later})
	pr.UpdatedAt = later
	got := domain.IngestPullRequest(prev, pr)
	base := domain.ChangeEvent{Ref: ref, At: later, Origin: domain.OriginHuman}
	want := []domain.ChangeEvent{
		{Ref: ref, Kind: domain.ChangeItemEdited, Field: domain.FieldBody, Old: "Closes #12", Body: pr.Body, At: later, Origin: domain.OriginHuman},
		{Ref: ref, Kind: domain.ChangeDraftChanged, State: domain.PRReady, At: later, Origin: domain.OriginHuman},
		{Ref: ref, Kind: domain.ChangeLabelAdded, Label: "anthill:review", At: later, Origin: domain.OriginHuman},
		{Ref: ref, Kind: domain.ChangeCommitsPushed, Head: "def", At: later, Origin: domain.OriginHuman},
		{Ref: ref, Kind: domain.ChangeCommentAdded, Actor: "dev", CommentID: "r2", ThreadID: "T1", Body: "And here?", At: later, Origin: base.Origin},
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got.Events, want)
	}
}

// A body Anthill wrote carries a marker, so its edit is Anthill's.
func TestIngestEditOriginFromMarker(t *testing.T) {
	ref := domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}
	pr := domain.PullRequest{Ref: ref, Body: "Closes #12"}
	prev := store(domain.StoredContent{}, domain.IngestPullRequest(domain.StoredContent{}, pr))
	marker := domain.Marker{Version: 1, Instance: "mrosani@LAPTOP-42", Kind: domain.CommentStatus, Ref: "gh-pr-34"}
	pr.Body = "Closes #12\n\n" + marker.String()
	got := domain.IngestPullRequest(prev, pr)
	if len(got.Events) != 1 || got.Events[0].Origin != domain.OriginAnthill {
		t.Errorf("events = %+v", got.Events)
	}
}

func TestConnectionName(t *testing.T) {
	ado := domain.Repository{Provider: domain.AzureDevOps, Org: "Contoso", Project: "Shop", Name: "web"}
	if got := domain.ConnectionName(ghRepo); got != "github" {
		t.Errorf("GitHub connection = %q", got)
	}
	if got := domain.ConnectionName(ado); got != "azure-devops/contoso" {
		t.Errorf("Azure DevOps connection = %q", got)
	}
	if got := domain.TrackerRepository(ado); got != (domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop"}) {
		t.Errorf("Azure DevOps tracker repository = %+v", got)
	}
	if got := domain.TrackerRepository(ghRepo); got != ghRepo {
		t.Errorf("GitHub tracker repository = %+v", got)
	}
}
