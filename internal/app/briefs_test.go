package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// briefs is the interface ant supervision declares for the briefs it
// starts runs with. This copy checks that BriefBuilder satisfies it.
type briefs interface {
	Task(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error)
	Summary(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error)
	Resume(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error)
}

var _ briefs = (*app.BriefBuilder)(nil)

// briefEnv is the brief builder over fakes: issue #12 on GitHub, a clone
// of acme/shop with the ant's branch and worktree, and the ant.
type briefEnv struct {
	t       *testing.T
	clock   *testkit.Clock
	gh      *testkit.Provider
	store   *testkit.StateStore
	git     *testkit.SourceControl
	builder *app.BriefBuilder
	ant     ports.AntRecord
}

func newBriefEnv(t *testing.T) *briefEnv {
	t.Helper()
	ctx := context.Background()
	clock := testkit.NewClock(t0)
	gh := testkit.NewProvider(clock)
	gh.PutItem(domain.WorkItem{
		Ref: ghRef(12), URL: "https://github.com/acme/shop/issues/12",
		Title: "Fix login", Body: "Logins time out on Safari.", State: domain.StateOpen, Labels: []string{"anthill"},
	})
	git := testkit.NewSourceControl()
	clone := git.AddClone("/src/shop", shop, "main")
	if err := git.Fetch(ctx, clone); err != nil {
		t.Fatal(err)
	}
	if err := git.CreateBranch(ctx, clone, antBranch, "main"); err != nil {
		t.Fatal(err)
	}
	trailers := domain.Trailers{Item: "gh-12", Agent: "claude-code", Instance: instanceA}
	if err := git.AddWorktree(ctx, clone, antWorktree, antBranch, trailers); err != nil {
		t.Fatal(err)
	}
	store := testkit.NewStateStore()
	ant, err := domain.NewAnt(ghRef(12), domain.AntStarting, time.Hour, domain.DefaultBudgetPolicy(), t0, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	rec := ports.AntRecord{
		Item: ghRef(12), Base: time.Hour, Budget: ant.Budget, History: ant.History(),
		Runner: "claude", Worktree: antWorktree, Branch: antBranch, BaseBranch: "main",
	}
	rec.ID, err = store.CreateAnt(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	e := &briefEnv{t: t, clock: clock, gh: gh, store: store, git: git, ant: rec}
	e.builder = app.NewBriefBuilder(app.BriefDeps{
		Tracker:  func(context.Context, domain.Ref) (ports.TrackerPort, error) { return gh.Client("mrosani"), nil },
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return gh.Client("mrosani"), nil },
		Workspace: func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Clone: clone, Repo: shop, Settings: domain.Effective{AgentInstructions: "AGENTS.md"}}, nil
		},
		Git:   git,
		Runs:  store,
		Clock: clock,
	})
	return e
}

// anthillComment renders an Anthill comment of the given kind on #12.
func anthillComment(kind domain.CommentKind, body string) string {
	m := domain.Marker{Version: domain.MarkerVersion, Instance: instanceA, Kind: kind, Ref: "gh-12"}
	if kind == domain.CommentClaim {
		m.ClaimFields = &domain.ClaimFields{Claim: domain.ClaimTask, LeaseMinutes: 15}
	}
	return domain.AnthillComment{Role: domain.RoleAnt, Body: body, Marker: m}.String()
}

func contains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("brief lacks %q:\n%s", w, got)
		}
	}
}

func lacks(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(got, w) {
			t.Errorf("brief contains %q:\n%s", w, got)
		}
	}
}

// Spec ant-execution, "Task brief", scenario "Brief includes discussion".
func TestBriefIncludesDiscussion(t *testing.T) {
	e := newBriefEnv(t)
	e.gh.Comment(ghRef(12), "mrosani", anthillComment(domain.CommentClaim, "Claimed by mrosani@LAPTOP-42."))
	e.gh.Comment(ghRef(12), "ann", "Scope: only the login page.")
	e.clock.Advance(time.Minute)
	e.gh.Comment(ghRef(12), "bob", "Safari 17 and 18 only.")
	e.clock.Advance(time.Minute)
	e.gh.Comment(ghRef(12), "mrosani", anthillComment(domain.CommentStatus, "Working on it."))
	e.gh.Comment(ghRef(12), "ann", "Keep the API unchanged.")

	got, err := e.builder.Task(context.Background(), e.ant, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, got,
		"Scope: only the login page.", "Safari 17 and 18 only.", "Keep the API unchanged.",
		"Fix login", "Logins time out on Safari.", "https://github.com/acme/shop/issues/12",
		"Branch: `ant/gh-12-fix-login`", "Base branch: `main`", "Time budget: 60 minutes",
		"`AGENTS.md`", "`ask_question`", "`finish`",
	)
	lacks(t, got, "Claimed by", "Working on it.", "<!-- anthill")
	first := strings.Index(got, "Scope: only the login page.")
	last := strings.Index(got, "Keep the API unchanged.")
	if first < 0 || last < first {
		t.Errorf("comments are not in their order:\n%s", got)
	}
}

// Spec ant-execution, "Sessions are persisted for resumption", scenario
// "Runner without resume support", and "Session index recovery".
func TestSummaryBriefSummarizesPriorWork(t *testing.T) {
	ctx := context.Background()
	e := newBriefEnv(t)
	e.gh.Comment(ghRef(12), "ann", "Scope: only the login page.")
	e.gh.Comment(ghRef(12), "mrosani", anthillComment(domain.CommentQuestion, "30 or 60 seconds?"))

	run, err := e.store.StartRun(ctx, ports.RunRecord{Ant: e.ant.ID, Kind: ports.RunTask, StartedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.EndRun(ctx, ports.RunRecord{ID: run, Ant: e.ant.ID, Kind: ports.RunTask, StartedAt: t0, EndedAt: t0.Add(20 * time.Minute), Outcome: ports.OutcomeComplete, Reason: "finished"}); err != nil {
		t.Fatal(err)
	}
	e.git.Commit(antWorktree, "fix: retry login on Safari")
	e.git.Commit(antWorktree, "test: cover the Safari retry")

	e.gh.PutPullRequest(domain.PullRequest{
		Ref: ghPR(34), URL: "https://github.com/acme/shop/pull/34", Title: "Fix login on Safari", Body: "Closes #12",
		SourceBranch: antBranch, TargetBranch: "main", Author: "mrosani",
		Comments: []domain.Comment{
			{ID: "c1", Author: "carol", Body: "Please use a 60 second timeout.", CreatedAt: t0.Add(2 * time.Hour)},
			{ID: "c2", Author: "mrosani", Body: anthillComment(domain.CommentSummary, "Done: retry added."), CreatedAt: t0.Add(time.Hour)},
		},
		Threads: []domain.ReviewThread{{ID: "t1", Path: "login.go", Line: 7, Comments: []domain.Comment{
			{ID: "c3", Author: "dave", Body: "Rename this variable.", CreatedAt: t0.Add(90 * time.Minute)},
		}}},
	})
	pr := ghPR(34)
	e.ant.PR = &pr
	e.ant.SessionID = ""
	// The run used 20 minutes, then the ant is between runs, so its
	// budget no longer counts.
	ant, err := domain.RestoreAnt(e.ant.Item, e.ant.Base, e.ant.Budget, e.ant.History)
	if err != nil {
		t.Fatal(err)
	}
	if err := ant.Transition(domain.AntRunning, t0, "runner started"); err != nil {
		t.Fatal(err)
	}
	if err := ant.Transition(domain.AntFinishing, t0.Add(20*time.Minute), "finish called"); err != nil {
		t.Fatal(err)
	}
	if err := ant.Transition(domain.AntSucceeded, t0.Add(20*time.Minute), "finished"); err != nil {
		t.Fatal(err)
	}
	e.ant.Budget, e.ant.History = ant.Budget, ant.History()
	e.clock.Advance(3 * time.Hour)

	answer := &domain.ChangeEvent{
		Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: "ann", At: t0.Add(150 * time.Minute),
		Origin: domain.OriginHuman, CommentID: "a1", Body: "60 seconds.",
	}
	got, err := e.builder.Summary(ctx, e.ant, answer, nil)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, got,
		"# Summary brief for gh-12",
		"Logins time out on Safari.", "Scope: only the login page.",
		"Task run started 2026-10-01T10:30:00Z, ended complete: finished",
		"fix: retry login on Safari", "test: cover the Safari retry",
		"https://github.com/acme/shop/pull/34 (open)", "Fix login on Safari", "Closes #12",
		"Please use a 60 second timeout.",
		"Comment by dave at 2026-10-01T12:00:00Z on `login.go` line 7", "Rename this variable.",
		"Time left: 40 minutes",
		"### Answer to your question", "ann answered at 2026-10-01T13:00:00Z", "60 seconds.",
	)
	lacks(t, got, "30 or 60 seconds?", "Done: retry added.", "<!-- anthill")
	if strings.Index(got, "Rename this variable.") > strings.Index(got, "Please use a 60 second timeout.") {
		t.Errorf("PR comments are not in time order:\n%s", got)
	}
}

func TestSummaryBriefBeforeThePR(t *testing.T) {
	e := newBriefEnv(t)
	got, err := e.builder.Summary(context.Background(), e.ant, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, got, "No earlier run is recorded.", "No commits yet.", "No pull request is open yet.")
}

func TestBriefReportsProviderErrors(t *testing.T) {
	e := newBriefEnv(t)
	boom := errors.New("provider down")
	e.gh.Fail(testkit.OpItem, boom)
	if _, err := e.builder.Task(context.Background(), e.ant, nil, nil); !errors.Is(err, boom) {
		t.Fatalf("Task: want %v, got %v", boom, err)
	}
	if _, err := e.builder.Summary(context.Background(), e.ant, nil, nil); !errors.Is(err, boom) {
		t.Fatalf("Summary: want %v, got %v", boom, err)
	}
}

// An ant queued at acceptance starts its task run with the changes stored
// meanwhile (spec change-reaction, "Changes while the ant is between
// runs").
func TestTaskBriefCarriesStoredChanges(t *testing.T) {
	e := newBriefEnv(t)
	changes := []domain.AntChange{{ID: 1, Event: domain.ChangeEvent{
		Ref: ghRef(12), Kind: domain.ChangeItemEdited, Actor: "ann", At: t0, Origin: domain.OriginHuman,
		Field: domain.FieldBody, Old: "Logins time out.", Body: "Logins time out on Safari.",
	}}}
	got, err := e.builder.Task(context.Background(), e.ant, nil, changes)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, got, "## Input for this run", "ann edited the description of gh-12", "Logins time out.")
	if strings.Index(got, "## Input for this run") > strings.Index(got, "## Conventions") {
		t.Errorf("the conventions must end the brief:\n%s", got)
	}
}

func TestResumeInputReadsNothing(t *testing.T) {
	e := newBriefEnv(t)
	e.gh.Fail(testkit.OpItem, errors.New("provider down"))
	answer := &domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: "ann", At: t0, Body: "60 seconds."}
	got, err := e.builder.Resume(context.Background(), e.ant, answer, nil)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, got, "60 seconds.")
	lacks(t, got, "Logins time out on Safari.", "## Conventions")
}
