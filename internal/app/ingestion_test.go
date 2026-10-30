package app_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// t0 is 10:30, so that the rate limit scenario's 10:42 lies ahead.
var t0 = time.Date(2026, 10, 1, 10, 30, 0, 0, time.UTC)

var (
	shop    = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}
	web     = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}
	adoShop = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop"}
)

func ghRef(n int) domain.Ref {
	return domain.Ref{Repo: shop, Kind: domain.KindItem, Number: n}
}

func ghPR(n int) domain.Ref {
	return domain.Ref{Repo: shop, Kind: domain.KindPR, Number: n}
}

func adoRef(n int) domain.Ref {
	return domain.Ref{Repo: adoShop, Kind: domain.KindItem, Number: n}
}

func adoEntry(remote string) domain.RepositoryEntry {
	return domain.RepositoryEntry{Provider: "azure-devops", Remote: remote, Path: absPath("src", remote)}
}

// pollEnv is one instance polling a fake GitHub and a fake Azure DevOps
// organization.
type pollEnv struct {
	t         *testing.T
	clock     *testkit.Clock
	store     *testkit.StateStore
	config    *app.Configuration
	gh, ado   *testkit.Provider
	conns     []app.Connection
	ingestion *app.Ingestion
}

func newPollEnv(t *testing.T, global domain.SettingValues, entries ...domain.RepositoryEntry) *pollEnv {
	t.Helper()
	if entries == nil {
		entries = []domain.RepositoryEntry{entry("acme/shop"), adoEntry("contoso/shop/web")}
	}
	e := &pollEnv{t: t, clock: testkit.NewClock(t0), store: testkit.NewStateStore()}
	e.config = app.NewConfiguration(source(global, entries...))
	if _, err := e.config.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.gh, e.ado = testkit.NewProvider(e.clock), testkit.NewProvider(e.clock)
	ghClient, adoClient := e.gh.Client("mrosani"), e.ado.Client("mrosani")
	e.conns = []app.Connection{
		{Name: "github", Tracker: ghClient, CodeHost: ghClient},
		{Name: "azure-devops/contoso", Tracker: adoClient, CodeHost: adoClient},
	}
	e.ingestion = app.NewIngestion(e.store, e.config, e.clock)
	return e
}

// poll polls one connection and returns the events it added to the
// outbox.
func (e *pollEnv) poll(conn int) (app.PollReport, []domain.ChangeEvent) {
	e.t.Helper()
	before := len(e.pending())
	report, err := e.ingestion.Poll(context.Background(), e.conns[conn])
	if err != nil {
		e.t.Fatalf("Poll(%s) = %v", e.conns[conn].Name, err)
	}
	return report, e.pending()[before:]
}

func (e *pollEnv) pending() []domain.ChangeEvent {
	e.t.Helper()
	list, err := e.store.Pending(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var out []domain.ChangeEvent
	for _, p := range list {
		out = append(out, p.Event)
	}
	return out
}

func (e *pollEnv) status(connection string) ports.PollStatus {
	e.t.Helper()
	st, err := e.store.PollStatus(context.Background(), connection)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func optedIn(ref domain.Ref, title string) domain.WorkItem {
	return domain.WorkItem{Ref: ref, Author: "reporter", Title: title, Body: "Steps", State: domain.StateOpen, Labels: []string{"anthill"}}
}

func eventKinds(events []domain.ChangeEvent) []domain.ChangeKind {
	out := []domain.ChangeKind{}
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// Scenario "New comment on a ticket", end to end: exactly one
// comment-added event, carrying the comment's author and body.
func TestIngestionNewCommentOnATicket(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.gh.PutItem(optedIn(ghRef(12), "Fix login timeout"))
	if _, events := e.poll(0); !reflect.DeepEqual(eventKinds(events), []domain.ChangeKind{domain.ChangeItemIngested}) {
		t.Fatalf("first poll events = %+v", events)
	}

	e.clock.Advance(time.Minute)
	c := e.gh.Comment(ghRef(12), "dev", "It also fails on Safari.")
	_, events := e.poll(0)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one", events)
	}
	got := events[0]
	if got.Kind != domain.ChangeCommentAdded || got.Ref != ghRef(12) || got.Actor != "dev" ||
		got.Body != "It also fails on Safari." || got.CommentID != c.ID || got.Origin != domain.OriginHuman {
		t.Errorf("event = %+v", got)
	}
}

// Scenario "No change": a poll that finds the same content emits no
// event, also when the provider returns the item again.
func TestIngestionNoChange(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.gh.PutItem(optedIn(ghRef(12), "Fix login timeout"))
	e.poll(0)
	if _, events := e.poll(0); len(events) != 0 {
		t.Errorf("unchanged poll events = %+v", events)
	}
	// The same content stored again moves the revision, so the provider
	// returns the item; its hashes have not changed.
	item, _ := e.gh.Item(ghRef(12))
	e.gh.PutItem(item)
	if _, events := e.poll(0); len(events) != 0 {
		t.Errorf("same content, new revision: events = %+v", events)
	}
}

// Scenario "Developer comments from the same account": Anthill's comment
// and a hand-typed one by the same account on an ant PR are told apart
// by the marker only.
func TestIngestionDeveloperCommentsFromTheSameAccount(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.gh.PutPullRequest(domain.PullRequest{Ref: ghPR(34), Title: "Fix login", Body: "Closes #12", Author: "mrosani",
		SourceBranch: "ant/gh-12-fix-login", HeadCommit: "abc", Item: ptr(ghRef(12))})
	e.poll(0)

	status := domain.AnthillComment{Role: domain.RoleAnt, Body: "Pushed the fix.",
		Marker: domain.Marker{Version: 1, Instance: "mrosani@LAPTOP-42", Kind: domain.CommentStatus, Ref: "gh-pr-34"}}
	if _, err := e.gh.Client("mrosani").AddComment(context.Background(), ghPR(34), status); err != nil {
		t.Fatal(err)
	}
	e.gh.Comment(ghPR(34), "mrosani", "Checked by hand: still slow on Safari.")
	_, events := e.poll(0)
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Actor != "mrosani" || events[0].Origin != domain.OriginAnthill {
		t.Errorf("Anthill's comment = %+v, want flagged as Anthill-authored", events[0])
	}
	if events[1].Actor != "mrosani" || events[1].Origin != domain.OriginHuman {
		t.Errorf("hand-typed comment = %+v, want flagged as human", events[1])
	}
}

// Scenario "Ticket without opt-in label": the issue is never ingested,
// so nothing reaches the outbox for eligibility, and nothing is stored.
func TestIngestionTicketWithoutOptInLabel(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.gh.PutItem(domain.WorkItem{Ref: ghRef(13), Title: "Refactor", State: domain.StateOpen, Labels: []string{"tech-debt"}})
	e.poll(0)
	e.gh.Comment(ghRef(13), "dev", "Any update?")
	e.poll(0)
	if events := e.pending(); len(events) != 0 {
		t.Errorf("events = %+v", events)
	}
	if hashes, _ := e.store.Hashes(context.Background(), ghRef(13)); len(hashes) != 0 {
		t.Errorf("hashes stored for an item that is not opted in: %v", hashes)
	}
}

// Scenario "Opt-in label added later": the issue is ingested at the next
// poll, with one item-ingested event for eligibility to evaluate.
func TestIngestionOptInLabelAddedLater(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.gh.PutItem(domain.WorkItem{Ref: ghRef(13), Title: "Refactor", State: domain.StateOpen, Comments: []domain.Comment{{ID: "1", Author: "dev", Body: "old"}}})
	e.poll(0)

	e.clock.Advance(time.Minute)
	e.gh.EditItem(ghRef(13), func(i *domain.WorkItem) { i.Labels = append(i.Labels, "anthill") })
	_, events := e.poll(0)
	want := []domain.ChangeEvent{{Ref: ghRef(13), Kind: domain.ChangeItemIngested, At: t0.Add(time.Minute), Origin: domain.OriginHuman}}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %+v, want %+v", events, want)
	}
}

// The opt-in label is a repository setting; a repository using another
// label ingests by that label only.
func TestIngestionConfiguredOptInLabel(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{OptInLabel: ptr("ai")}, entry("acme/shop"))
	e.gh.PutItem(optedIn(ghRef(12), "labelled anthill"))
	e.gh.PutItem(domain.WorkItem{Ref: ghRef(14), Title: "labelled ai", State: domain.StateOpen, Labels: []string{"AI"}})
	_, events := e.poll(0)
	if len(events) != 1 || events[0].Ref != ghRef(14) {
		t.Errorf("events = %+v, want only #14", events)
	}
}

// Removing the opt-in label emits its label-removed event, which
// change-reaction turns into a withdrawal; changes after it emit nothing.
func TestIngestionOptInLabelRemoved(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.gh.PutItem(optedIn(ghRef(12), "Fix login timeout"))
	e.poll(0)
	e.gh.EditItem(ghRef(12), func(i *domain.WorkItem) { i.Labels = nil })
	_, events := e.poll(0)
	if len(events) != 1 || events[0].Kind != domain.ChangeLabelRemoved || events[0].Label != "anthill" {
		t.Fatalf("events = %+v", events)
	}
	e.gh.Comment(ghRef(12), "dev", "ping")
	if _, events := e.poll(0); len(events) != 0 {
		t.Errorf("events after withdrawal = %+v", events)
	}
}

// An edit carries the old and the new text, so that change-reaction can
// deliver a diff.
func TestIngestionEditCarriesOldText(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.gh.PutItem(optedIn(ghRef(12), "Fix login timeout"))
	e.poll(0)
	e.gh.EditItem(ghRef(12), func(i *domain.WorkItem) { i.Body = "Steps\n\n- [ ] works on Safari" })
	_, events := e.poll(0)
	if len(events) != 1 || events[0].Kind != domain.ChangeItemEdited || events[0].Field != domain.FieldBody ||
		events[0].Old != "Steps" || events[0].Body != "Steps\n\n- [ ] works on Safari" {
		t.Errorf("events = %+v", events)
	}
}

// failingStore is a store whose next RecordPoll fails, like a daemon that
// crashes while writing a poll.
type failingStore struct {
	*testkit.StateStore
	fail bool
}

func (s *failingStore) RecordPoll(ctx context.Context, p ports.PollResult) error {
	if s.fail {
		s.fail = false
		return errors.New("crash")
	}
	return s.StateStore.RecordPoll(ctx, p)
}

// A crash while writing a poll keeps neither the new hashes nor the
// events (design D7). After the restart, the next poll finds the same
// comment again and emits it exactly once.
func TestIngestionRestartAfterAFailedWrite(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	store := &failingStore{StateStore: e.store}
	e.gh.PutItem(optedIn(ghRef(12), "Fix login timeout"))
	if _, err := app.NewIngestion(store, e.config, e.clock).Poll(context.Background(), e.conns[0]); err != nil {
		t.Fatal(err)
	}
	e.gh.Comment(ghRef(12), "dev", "Please also handle the empty password.")

	store.fail = true
	if _, err := app.NewIngestion(store, e.config, e.clock).Poll(context.Background(), e.conns[0]); err == nil || !strings.Contains(err.Error(), "crash") {
		t.Fatalf("Poll() during the crash = %v", err)
	}
	if events := e.pending(); len(events) != 1 {
		t.Fatalf("events after the crash = %+v, want only the first poll's", events)
	}

	restarted := app.NewIngestion(store, e.config, e.clock)
	for range 2 {
		if _, err := restarted.Poll(context.Background(), e.conns[0]); err != nil {
			t.Fatal(err)
		}
	}
	events := e.pending()
	if !reflect.DeepEqual(eventKinds(events), []domain.ChangeKind{domain.ChangeItemIngested, domain.ChangeCommentAdded}) {
		t.Errorf("events after restart = %+v, want the comment exactly once", events)
	}
}

// One failing repository of a connection keeps its cursor and is named
// in the failure, while the other repositories are stored.
func TestIngestionFailingSourceKeepsTheOthers(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{}, entry("acme/shop"), entry("acme/web"))
	e.gh.PutItem(optedIn(ghRef(12), "Shop item"))
	e.gh.PutItem(optedIn(domain.Ref{Repo: web, Kind: domain.KindItem, Number: 3}, "Web item"))
	e.gh.FailNext(testkit.OpItems, errors.New("404 Not Found"))

	report, events := e.poll(0)
	if report.Polled != 3 || len(report.Failed) != 1 || len(events) != 1 || events[0].Ref.Repo != web {
		t.Fatalf("report = %+v, events = %+v", report, events)
	}
	st := e.status("github")
	if !st.LastSuccess.Equal(t0) || st.LastError != "items of acme/shop: 404 Not Found" || !st.LastErrorAt.Equal(t0) {
		t.Errorf("status = %+v", st)
	}

	// The next poll reads the failed source again and catches up.
	e.clock.Advance(2 * time.Minute)
	report, events = e.poll(0)
	if report.Polled != 4 || len(events) != 1 || events[0].Ref != ghRef(12) {
		t.Errorf("second poll: report = %+v, events = %+v", report, events)
	}
	if st := e.status("github"); st.LastError != "" || !st.LastSuccess.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("status after recovery = %+v", st)
	}
}

// When every source fails, nothing is stored and the last successful poll
// time stays.
func TestIngestionConnectionUnavailable(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	e.ado.PutItem(optedIn(adoRef(7), "ADO item"))
	e.poll(1)
	e.clock.Advance(2 * time.Minute)
	e.ado.Fail(testkit.OpItems, errors.New("503 Service Unavailable"))
	e.ado.Fail(testkit.OpPullRequests, errors.New("503 Service Unavailable"))
	report, _ := e.poll(1)
	if report.Polled != 0 || len(report.Failed) != 2 {
		t.Errorf("report = %+v", report)
	}
	st := e.status("azure-devops/contoso")
	if !st.LastSuccess.Equal(t0) || !st.LastErrorAt.Equal(t0.Add(2*time.Minute)) ||
		st.LastError != "items of contoso/shop: 503 Service Unavailable; pull requests of contoso/shop/web: 503 Service Unavailable" {
		t.Errorf("status = %+v", st)
	}
}

// A rate-limited poll sends no further request and records the reset;
// the sources already read are stored.
func TestIngestionRateLimited(t *testing.T) {
	e := newPollEnv(t, domain.SettingValues{})
	reset := t0.Add(12 * time.Minute)
	e.gh.RateLimit(reset)
	report, _ := e.poll(0)
	if !report.RateLimitedUntil.Equal(reset) || report.Polled != 0 || len(report.Failed) != 0 {
		t.Errorf("report = %+v", report)
	}
	if n := len(e.gh.Requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	st := e.status("github")
	if !st.RateLimitedUntil.Equal(reset) || st.LastError != "rate limited until 2026-10-01T10:42:00Z" {
		t.Errorf("status = %+v", st)
	}
}

// An Azure DevOps project behind two repository entries is polled once,
// and either entry's opt-in label opts an item in; each repository's PRs
// are polled.
func TestIngestionAzureDevOpsProjectWithTwoRepositories(t *testing.T) {
	api := adoEntry("Contoso/Shop/api")
	api.Settings.OptInLabel = ptr("ai")
	e := newPollEnv(t, domain.SettingValues{}, adoEntry("contoso/shop/web"), api)
	e.ado.PutItem(optedIn(adoRef(7), "anthill"))
	e.ado.PutItem(domain.WorkItem{Ref: adoRef(8), Title: "ai", State: domain.StateOpen, Labels: []string{"ai"}})
	e.ado.PutItem(domain.WorkItem{Ref: adoRef(9), Title: "none", State: domain.StateOpen})

	report, events := e.poll(1)
	if report.Polled != 3 || len(events) != 2 || events[0].Ref != adoRef(7) || events[1].Ref != adoRef(8) {
		t.Errorf("report = %+v, events = %+v", report, events)
	}
	var ops []testkit.Op
	for _, r := range e.ado.Requests() {
		ops = append(ops, r.Op)
	}
	if !reflect.DeepEqual(ops, []testkit.Op{testkit.OpItems, testkit.OpPullRequests, testkit.OpPullRequests}) {
		t.Errorf("requests = %v", ops)
	}
}
