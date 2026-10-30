package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// toolsEnv is the ant tools over fakes: a GitHub provider with issue #12,
// a clone of acme/shop with the ant's branch and worktree, and the ant,
// which holds the task claim and has an active run.
type toolsEnv struct {
	t        *testing.T
	clock    *testkit.Clock
	gh       *testkit.Provider
	store    *testkit.StateStore
	git      *testkit.SourceControl
	clone    ports.Clone
	tools    *app.AntTools
	ant      ports.AntID
	runs     *fakeRunEnder
	finishes *fakeFinishes
	reviews  *fakeReviews
	fixes    *fakeFixes
	notifier *testkit.Notifier
}

// The ant's worktree and branch, and the user's home.
const (
	antWorktree = "/home/ann/.anthill/worktrees/gh-acme-shop/ant/gh-12-fix-login"
	antBranch   = "ant/gh-12-fix-login"
	userHome    = "/home/ann"
)

// storeClaims lists the held claims of a local store, as Claiming.Held
// does.
type storeClaims struct{ store *testkit.StateStore }

func (c storeClaims) Held(ctx context.Context) ([]ports.HeldClaim, error) {
	return c.store.HeldClaims(ctx)
}

// fakeRunEnder records the runs it is asked to end.
type fakeRunEnder struct {
	mu    sync.Mutex
	ended []string
}

func (f *fakeRunEnder) EndRun(_ context.Context, ant ports.AntID, run ports.RunID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, fmt.Sprintf("%d/%d: %s", ant, run, reason))
	return nil
}

// fakeFinishes, fakeReviews and fakeFixes record what is handed over.
type (
	fakeFinishes struct{ got []app.FinishRequest }
	fakeReviews  struct{ got []app.ReviewReport }
	fakeFixes    struct{ got []app.FixReport }
)

func (f *fakeFinishes) Finish(_ context.Context, r app.FinishRequest) error {
	f.got = append(f.got, r)
	return nil
}

func (f *fakeReviews) SubmitReview(_ context.Context, r app.ReviewReport) error {
	f.got = append(f.got, r)
	return nil
}

func (f *fakeFixes) FinishFixes(_ context.Context, r app.FixReport) error {
	f.got = append(f.got, r)
	return nil
}

// newToolsEnv makes the ant for #12 with a 60-minute budget and the
// given policy, starting at t0 and running from t0.
func newToolsEnv(t *testing.T, policy domain.BudgetPolicy) *toolsEnv {
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
	ant, err := domain.NewAnt(ghRef(12), domain.AntStarting, time.Hour, policy, t0, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	if err := ant.Transition(domain.AntRunning, t0, "runner started"); err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateAnt(ctx, ports.AntRecord{
		Item: ghRef(12), Base: time.Hour, Budget: ant.Budget, History: ant.History(),
		Runner: "claude", Worktree: antWorktree, Branch: antBranch, BaseBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun(ctx, ports.RunRecord{Ant: id, Kind: ports.RunTask, StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveClaim(ctx, ports.HeldClaim{Ref: ghRef(12), Kind: domain.ClaimTask, Instance: instanceA, CommentID: "1", Lease: 15 * time.Minute, UpdatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	e := &toolsEnv{
		t: t, clock: clock, gh: gh, store: store, git: git, clone: clone, ant: id,
		runs: &fakeRunEnder{}, finishes: &fakeFinishes{}, reviews: &fakeReviews{}, fixes: &fakeFixes{}, notifier: &testkit.Notifier{},
	}
	redactor, err := domain.NewRedactor([]string{`ACME-[0-9]{8}`})
	if err != nil {
		t.Fatal(err)
	}
	e.tools = app.NewAntTools(app.AntToolsDeps{
		Store:    store,
		Claims:   storeClaims{store},
		Tracker:  func(context.Context, domain.Ref) (ports.TrackerPort, error) { return gh.Client("mrosani"), nil },
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return gh.Client("mrosani"), nil },
		Workspace: func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Clone: clone, Repo: shop, Settings: domain.Effective{OptInLabel: "anthill", ProtectedBranches: []string{"release/*"}}}, nil
		},
		Git:      git,
		Clock:    clock,
		Notifier: e.notifier,
		Runs:     e.runs,
		Finishes: e.finishes,
		Reviews:  e.reviews,
		Fixes:    e.fixes,
	}, app.AntToolsOptions{
		ExtensionWait: func() time.Duration { return domain.DefaultExtensionWait },
		Redactor:      func() *domain.Redactor { return redactor },
		Home:          userHome,
	})
	return e
}

// issue returns a token for the ant's run in the given role.
func (e *toolsEnv) issue(role domain.AgentRole) string {
	e.t.Helper()
	s := app.ToolSession{Role: role, Ant: e.ant, Run: 1}
	if !role.IsRun() {
		s = app.ToolSession{Role: role, PR: ghPR(34), Round: 1}
	}
	token, err := e.tools.Issue(s)
	if err != nil {
		e.t.Fatal(err)
	}
	return token
}

func (e *toolsEnv) call(token, tool, args string) app.ToolResult {
	return e.tools.Call(context.Background(), token, tool, json.RawMessage(args))
}

// mustCall calls a tool that must succeed and decodes its result.
func (e *toolsEnv) mustCall(token, tool, args string, out any) {
	e.t.Helper()
	res := e.call(token, tool, args)
	if res.IsError {
		e.t.Fatalf("%s: refused: %s", tool, res.Text)
	}
	if err := json.Unmarshal([]byte(res.Text), out); err != nil {
		e.t.Fatalf("%s: decoding %q: %v", tool, res.Text, err)
	}
}

// refused checks that a result is a refusal with the given reason.
func refused(t *testing.T, res app.ToolResult, reason string) {
	t.Helper()
	if !res.IsError || res.Text != reason {
		t.Errorf("result = %+v, want the refusal %q", res, reason)
	}
}

func (e *toolsEnv) antRecord() ports.AntRecord {
	e.t.Helper()
	rec, err := e.store.Ant(context.Background(), e.ant)
	if err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *toolsEnv) states() []domain.AntState {
	var out []domain.AntState
	for _, tr := range e.antRecord().History {
		out = append(out, tr.To)
	}
	return out
}

func (e *toolsEnv) setState(to domain.AntState) {
	e.t.Helper()
	rec := e.antRecord()
	rec.History = append(rec.History, domain.Transition{From: rec.State(), To: to, At: e.clock.Now(), Reason: "test"})
	if err := e.store.SaveAnt(context.Background(), rec); err != nil {
		e.t.Fatal(err)
	}
}

// Spec ant-tools, "Token reused after the run".
func TestTokenReusedAfterTheRun(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	if !strings.HasPrefix(token, "ant_") {
		t.Errorf("token = %q, want the ant_ prefix", token)
	}
	var task struct{}
	e.mustCall(token, domain.ToolGetTask, `{}`, &task)
	if _, err := e.tools.Tools(token); err != nil {
		t.Fatalf("Tools: %v", err)
	}

	e.tools.Revoke(token)
	before := len(e.gh.Requests())
	for _, tool := range []string{domain.ToolGetTask, domain.ToolPush, "no_such_tool"} {
		refused(t, e.call(token, tool, `{}`), "unknown or revoked tool token")
	}
	if got := len(e.gh.Requests()); got != before {
		t.Errorf("provider requests after the revoked calls = %d, want %d: nothing must be executed", got, before)
	}
	if _, err := e.tools.Tools(token); !errors.Is(err, app.ErrUnknownToken) {
		t.Errorf("Tools after Revoke = %v, want ErrUnknownToken", err)
	}
	refused(t, e.call("ant_never-issued", domain.ToolGetTask, `{}`), "unknown or revoked tool token")
}

func TestTokensAreScopedToOneRun(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	first, second := e.issue(domain.RoleTaskRun), e.issue(domain.RoleTaskRun)
	if first == second {
		t.Fatal("two runs got the same token")
	}
	e.tools.Revoke(first)
	var task struct{}
	e.mustCall(second, domain.ToolGetTask, `{}`, &task)

	for _, s := range []app.ToolSession{
		{Role: "queen"},
		{Role: domain.RoleTaskRun},
		{Role: domain.RoleReviewAgent},
	} {
		if _, err := e.tools.Issue(s); err == nil {
			t.Errorf("Issue(%+v) succeeded, want an error", s)
		}
	}
}

// Spec ant-tools, "Review agent tool list", and the tool list of each
// role.
func TestReviewAgentToolList(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	for _, role := range domain.AgentRoles {
		defs, err := e.tools.Tools(e.issue(role))
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range defs {
			names = append(names, d.Name)
			var s struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(d.InputSchema, &s); err != nil || s.Type != "object" {
				t.Errorf("%s: input schema %s is not an object schema", d.Name, d.InputSchema)
			}
			if d.Description == "" {
				t.Errorf("%s has no description", d.Name)
			}
		}
		if want := domain.ToolsFor(role); !slices.Equal(names, want) {
			t.Errorf("%s tools = %v, want %v", role, names, want)
		}
	}
	defs, _ := e.tools.Tools(e.issue(domain.RoleReviewAgent))
	if len(defs) != 1 || defs[0].Name != domain.ToolSubmitReview {
		t.Errorf("review agent tools = %v, want only submit_review", defs)
	}
}

// Spec ant-tools, "Fix agent cannot push".
func TestFixAgentCannotPush(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	if err := e.store.SaveClaim(context.Background(), heldReview()); err != nil {
		t.Fatal(err)
	}
	before := len(e.gh.Requests())
	refused(t, e.call(e.issue(domain.RoleFixAgent), domain.ToolPush, `{}`), "push is not available to the fix agent")
	refused(t, e.call(e.issue(domain.RoleReviewAgent), domain.ToolFinishFixes, `{"results":[]}`), "finish_fixes is not available to the review agent")
	refused(t, e.call(e.issue(domain.RoleFollowUpRun), domain.ToolOpenPullRequest, `{"title":"t","body":"b"}`), "open_pull_request is not available to the follow-up run")
	if got := len(e.gh.Requests()); got != before {
		t.Errorf("provider requests = %d, want %d: nothing must be pushed or posted", got, before)
	}
}

// Spec ant-tools, "Call while waiting for an extension": while a
// request_extension call waits for the queen, other calls of the run are
// refused at once, except get_task and time_remaining.
func TestCallWhileWaitingForAnExtension(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newToolsEnv(t, domain.DefaultBudgetPolicy())
		token := e.issue(domain.RoleTaskRun)
		done := make(chan app.ToolResult, 1)
		go func() { done <- e.call(token, domain.ToolRequestExtension, `{"minutes":30,"reason":"tests left"}`) }()
		synctest.Wait()
		if got := e.antRecord().State(); got != domain.AntAwaitingExtension {
			t.Fatalf("state while waiting = %s, want awaiting-extension", got)
		}

		refused(t, e.call(token, domain.ToolPush, `{}`), "waiting for the extension decision")
		refused(t, e.call(token, domain.ToolComment, `{"where":"item","body":"hi"}`), "waiting for the extension decision")
		var left struct {
			RemainingMinutes int `json:"remaining_minutes"`
			OpenExtension    *struct {
				Minutes int `json:"minutes"`
			} `json:"open_extension"`
		}
		e.mustCall(token, domain.ToolTimeRemaining, `{}`, &left)
		if left.OpenExtension == nil || left.OpenExtension.Minutes != 30 {
			t.Errorf("time_remaining open extension = %+v, want 30 minutes", left.OpenExtension)
		}
		var task struct{}
		e.mustCall(token, domain.ToolGetTask, `{}`, &task)

		e.clock.Advance(domain.DefaultExtensionWait)
		res := <-done
		var out struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(res.Text), &out); err != nil || out.Status != "pending" {
			t.Errorf("request_extension = %s, want pending", res.Text)
		}
	})
}

// Spec ant-tools, "Queen answers within the wait"; spec ant-timebox,
// "Queen approves a smaller extension".
func TestQueenAnswersWithinTheWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newToolsEnv(t, domain.DefaultBudgetPolicy())
		token := e.issue(domain.RoleTaskRun)
		done := make(chan app.ToolResult, 1)
		go func() { done <- e.call(token, domain.ToolRequestExtension, `{"minutes":30,"reason":"tests left"}`) }()
		synctest.Wait()

		e.clock.Advance(2 * time.Minute)
		req, effect, err := e.tools.DecideExtension(context.Background(), e.ant, 20)
		if err != nil {
			t.Fatal(err)
		}
		if req.Status != domain.ExtensionReduced || effect != domain.EffectBudgetGrown {
			t.Errorf("decision = %s, %s; want reduced, budget-grown", req.Status, effect)
		}
		res := <-done
		var out struct {
			Status           string `json:"status"`
			GrantedMinutes   int    `json:"granted_minutes"`
			RemainingMinutes int    `json:"remaining_minutes"`
		}
		if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
			t.Fatal(err)
		}
		// 60 minutes, none used: the 2 minutes of waiting do not count.
		if out.Status != "reduced" || out.GrantedMinutes != 20 || out.RemainingMinutes != 80 {
			t.Errorf("request_extension = %+v, want reduced, 20 granted, 80 remaining", out)
		}
		rec := e.antRecord()
		if rec.Budget.Total() != 80*time.Minute || rec.Budget.Open != nil {
			t.Errorf("budget total = %s, open = %v; want 80m and no open request", rec.Budget.Total(), rec.Budget.Open)
		}
		want := []domain.AntState{domain.AntStarting, domain.AntRunning, domain.AntAwaitingExtension, domain.AntRunning}
		if got := e.states(); !slices.Equal(got, want) {
			t.Errorf("states = %v, want %v", got, want)
		}
		exts, _ := e.store.Extensions(context.Background(), e.ant)
		if len(exts) != 1 || exts[0].Request.Status != domain.ExtensionReduced || exts[0].DecidedAt.IsZero() {
			t.Errorf("stored requests = %+v, want one decided as reduced", exts)
		}
		if res.Notices != nil {
			t.Errorf("notices = %v, want none: the call itself returned the decision", res.Notices)
		}
	})
}

// Spec ant-tools, "No answer within the wait"; spec ant-timebox, "No
// decision within the wait".
func TestNoAnswerWithinTheWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newToolsEnv(t, domain.DefaultBudgetPolicy())
		token := e.issue(domain.RoleTaskRun)
		e.clock.Advance(56 * time.Minute) // 4 minutes left
		done := make(chan app.ToolResult, 1)
		go func() { done <- e.call(token, domain.ToolRequestExtension, `{"minutes":30,"reason":"tests left"}`) }()
		synctest.Wait()
		e.clock.Advance(domain.DefaultExtensionWait)
		res := <-done
		var out struct {
			Status           string `json:"status"`
			RemainingMinutes int    `json:"remaining_minutes"`
			Message          string `json:"message"`
		}
		if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
			t.Fatal(err)
		}
		if out.Status != "pending" || out.RemainingMinutes != 4 || out.Message != app.MessageExtensionPending {
			t.Errorf("request_extension = %+v, want pending with 4 minutes left", out)
		}
		rec := e.antRecord()
		if rec.State() != domain.AntRunning || !rec.Budget.Counting() || rec.Budget.Open == nil {
			t.Errorf("state %s, counting %v, open %v; want running, counting, and the request open", rec.State(), rec.Budget.Counting(), rec.Budget.Open)
		}
		pending, _ := e.store.PendingExtensions(context.Background())
		if len(pending) != 1 {
			t.Errorf("pending requests = %d, want 1 left for the queen", len(pending))
		}

		// The late decision is delivered with the next tool result.
		if _, _, err := e.tools.DecideExtension(context.Background(), e.ant, 30); err != nil {
			t.Fatal(err)
		}
		next := e.call(token, domain.ToolTimeRemaining, `{}`)
		if len(next.Notices) != 1 || !strings.Contains(next.Notices[0], "30 minutes added") {
			t.Errorf("notices = %v, want the approval", next.Notices)
		}
		if again := e.call(token, domain.ToolTimeRemaining, `{}`); again.Notices != nil {
			t.Errorf("notices delivered twice: %v", again.Notices)
		}
	})
}

// A cancelled request_extension call, such as an agent that went away,
// leaves the ant running.
func TestCancelledExtensionCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newToolsEnv(t, domain.DefaultBudgetPolicy())
		token := e.issue(domain.RoleTaskRun)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan app.ToolResult, 1)
		go func() {
			done <- e.tools.Call(ctx, token, domain.ToolRequestExtension, json.RawMessage(`{"minutes":30,"reason":"tests left"}`))
		}()
		synctest.Wait()
		cancel()
		res := <-done
		if !res.IsError {
			t.Errorf("cancelled call = %+v, want an error", res)
		}
		if got := e.antRecord().State(); got != domain.AntRunning {
			t.Errorf("state = %s, want running", got)
		}
		var task struct{}
		e.mustCall(token, domain.ToolGetTask, `{}`, &task)
	})
}

// Spec ant-timebox, "Auto-approved".
func TestExtensionAutoApproved(t *testing.T) {
	policy := domain.DefaultBudgetPolicy()
	policy.AutoApprove = domain.ExtensionAllowance{MaxMinutes: 15, Times: 1}
	e := newToolsEnv(t, policy)
	var out struct {
		Status         string `json:"status"`
		GrantedMinutes int    `json:"granted_minutes"`
	}
	e.mustCall(e.issue(domain.RoleTaskRun), domain.ToolRequestExtension, `{"minutes":10,"reason":"one more test"}`, &out)
	if out.Status != "approved" || out.GrantedMinutes != 10 {
		t.Errorf("request_extension = %+v, want approved with 10", out)
	}
	if rec := e.antRecord(); rec.Budget.Total() != 70*time.Minute {
		got := rec.Budget.Total()
		t.Errorf("budget = %s, want 70m", got)
	}
	exts, _ := e.store.Extensions(context.Background(), e.ant)
	if len(exts) != 1 || exts[0].Request.Status != domain.ExtensionApproved {
		t.Errorf("logged requests = %+v, want one approved", exts)
	}
	if got := e.states(); got[len(got)-1] != domain.AntRunning || len(got) != 2 {
		t.Errorf("states = %v, want no wait", got)
	}
}

// Spec ant-timebox, "Ceiling reached".
func TestExtensionCeilingReached(t *testing.T) {
	policy := domain.DefaultBudgetPolicy()
	policy.AutoApprove = domain.ExtensionAllowance{MaxMinutes: 120, Times: 1}
	e := newToolsEnv(t, policy)
	token := e.issue(domain.RoleTaskRun)
	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	e.mustCall(token, domain.ToolRequestExtension, `{"minutes":120,"reason":"big"}`, &out)
	e.mustCall(token, domain.ToolRequestExtension, `{"minutes":10,"reason":"a bit more"}`, &out)
	if out.Status != "denied" || out.Message != "maximum budget reached" {
		t.Errorf("request_extension = %+v, want denied with maximum budget reached", out)
	}
	if rec := e.antRecord(); rec.Budget.Total() != 180*time.Minute {
		got := rec.Budget.Total()
		t.Errorf("budget = %s, want 180m", got)
	}
}

// Spec ant-tools, "Ticket changed since the brief".
func TestTicketChangedSinceTheBrief(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	e.gh.EditItem(ghRef(12), func(i *domain.WorkItem) { i.Body = "Logins time out on Safari and Firefox." })
	e.gh.Comment(ghRef(12), "ada", "Firefox too.")
	claim := domain.AnthillComment{Role: domain.RoleAnt, Marker: domain.Marker{Version: domain.MarkerVersion, Instance: instanceA, Kind: domain.CommentStatus, Ref: "gh-12"}, Body: "Working on it"}
	e.gh.Comment(ghRef(12), "mrosani", claim.String())
	e.clock.Advance(20 * time.Minute)

	var task struct {
		Item struct {
			Ref  string `json:"ref"`
			Body string `json:"body"`
		} `json:"item"`
		Comments []struct {
			Author string `json:"author"`
		} `json:"comments"`
		Branch string `json:"branch"`
		Base   string `json:"base"`
		Budget struct {
			Total     int `json:"total_minutes"`
			Remaining int `json:"remaining_minutes"`
		} `json:"budget"`
		PR *struct{} `json:"pr"`
	}
	e.mustCall(e.issue(domain.RoleTaskRun), domain.ToolGetTask, `{}`, &task)
	if task.Item.Body != "Logins time out on Safari and Firefox." {
		t.Errorf("body = %q, want the edited body", task.Item.Body)
	}
	if len(task.Comments) != 1 || task.Comments[0].Author != "ada" {
		t.Errorf("comments = %+v, want only the human one", task.Comments)
	}
	if task.Item.Ref != "gh-12" || task.Branch != "ant/gh-12-fix-login" || task.Base != "main" || task.PR != nil {
		t.Errorf("task = %+v", task)
	}
	if task.Budget.Total != 60 || task.Budget.Remaining != 40 {
		t.Errorf("budget = %+v, want 40 of 60", task.Budget)
	}

	// With a PR, get_task reads it fresh too.
	e.gh.PutPullRequest(domain.PullRequest{Ref: ghPR(34), URL: "https://github.com/acme/shop/pull/34", Draft: true, SourceBranch: "ant/gh-12-fix-login", TargetBranch: "main"})
	rec := e.antRecord()
	pr := ghPR(34)
	rec.PR = &pr
	if err := e.store.SaveAnt(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	var withPR struct {
		PR struct {
			URL   string `json:"url"`
			Draft bool   `json:"draft"`
		} `json:"pr"`
	}
	e.mustCall(e.issue(domain.RoleTaskRun), domain.ToolGetTask, `{}`, &withPR)
	if withPR.PR.URL != "https://github.com/acme/shop/pull/34" || !withPR.PR.Draft {
		t.Errorf("pr = %+v", withPR.PR)
	}
}

// Spec ant-timebox, "Ant checks its budget".
func TestAntChecksItsBudget(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)
	e.clock.Advance(20 * time.Minute)
	var out struct {
		Remaining int    `json:"remaining_minutes"`
		Total     int    `json:"total_minutes"`
		Used      int    `json:"used_minutes"`
		Warning   string `json:"warning"`
	}
	e.mustCall(token, domain.ToolTimeRemaining, `{}`, &out)
	if out.Remaining != 40 || out.Total != 60 || out.Used != 20 || out.Warning != "" {
		t.Errorf("time_remaining = %+v, want 40 of 60 left and no warning", out)
	}
	e.clock.Advance(28 * time.Minute)
	e.mustCall(token, domain.ToolTimeRemaining, `{}`, &out)
	if out.Warning != "12 minutes remaining: wrap up and open a PR, or request more time" {
		t.Errorf("warning at 48 minutes = %q", out.Warning)
	}
}

// The checks run in order: token, role, arguments, state, claim. Each
// refusal has no effect.
func TestToolCallChecks(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token := e.issue(domain.RoleTaskRun)

	refused(t, e.call(token, "deploy", `{}`), "unknown tool deploy")
	refused(t, e.call(token, domain.ToolRequestExtension, `{"minutes":0,"reason":"x"}`), "minutes must be at least 1, not 0")
	refused(t, e.call(token, domain.ToolRequestExtension, `{"reason":"x"}`), "the arguments: minutes is required")
	refused(t, e.call(token, domain.ToolGetTask, `{"verbose":true}`), `the arguments: unknown argument "verbose"`)
	refused(t, e.call(token, domain.ToolComment, `{"where":"wiki","body":"x"}`), "where must be one of item, pr, not \"wiki\"")
	refused(t, e.call(token, domain.ToolUpdatePullRequest, `{}`), "give at least one of title, body and draft")

	// The role check comes before the arguments.
	refused(t, e.call(e.issue(domain.RoleFixAgent), domain.ToolPush, `{"force":true}`), "push is not available to the fix agent")

	// The claim check: a lost claim refuses every call, reads included.
	if err := e.store.DeleteClaim(context.Background(), ghRef(12), domain.ClaimTask); err != nil {
		t.Fatal(err)
	}
	reason := "the claim on gh-12 is no longer held by this instance: stop working"
	refused(t, e.call(token, domain.ToolGetTask, `{}`), reason)
	refused(t, e.call(token, domain.ToolTimeRemaining, `{}`), reason)
	refused(t, e.call(e.issue(domain.RoleReviewAgent), domain.ToolSubmitReview, `{"summary":"ok","findings":[]}`), "the claim on gh-pr-34 is no longer held by this instance: stop working")

	// The state check comes before the claim.
	e.setState(domain.AntFinishing)
	refused(t, e.call(token, domain.ToolGetTask, `{}`), "the run is finishing: no more tool calls")
	e.setState(domain.AntSucceeded)
	refused(t, e.call(token, domain.ToolTimeRemaining, `{}`), "the run has ended")
}

func TestToolCallFailure(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	e.gh.Fail(testkit.OpItem, errors.New("GitHub is down"))
	res := e.call(e.issue(domain.RoleTaskRun), domain.ToolGetTask, `{}`)
	if !res.IsError || !strings.Contains(res.Text, "GitHub is down") {
		t.Errorf("result = %+v, want the failure", res)
	}
}

// heldReview is this instance's review claim on PR 34.
func heldReview() ports.HeldClaim {
	return ports.HeldClaim{Ref: ghPR(34), Kind: domain.ClaimReview, Instance: instanceA, CommentID: "2", Lease: 15 * time.Minute, UpdatedAt: t0}
}
