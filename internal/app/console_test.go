package app_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// consoleEnv is one instance with the console over its services, as in
// the daemon: the supervisor reports activity to the hub, and the
// conversation posts the queen's answers as mrosani.
type consoleEnv struct {
	*reactEnv
	hub     *app.ActivityHub
	conv    *app.Conversation
	config  *app.Configuration
	console *app.Console
}

func newConsoleEnv(t *testing.T) *consoleEnv {
	t.Helper()
	hub := app.NewActivityHub()
	ants := &lazyAnts{}
	var conv *app.Conversation
	e := &consoleEnv{hub: hub}
	e.reactEnv = newReactEnv(t, func(d *app.AntSupervisorDeps) {
		d.Activity = hub
		d.Runs = hub.RunEnds(d.Runs)
		conv = app.NewConversation(app.ConversationDeps{
			Store: d.Store.(*testkit.StateStore), Tracker: d.Tracker, CodeHost: d.CodeHost,
			Ants: ants, Clock: d.Clock, Notifier: d.Notifier,
			Queen: func(context.Context, domain.Ref) (ports.QueenCommentPort, error) { return e.gh.Client("mrosani"), nil },
		})
		d.Answers = conv.Answers()
	})
	ants.sup = e.sup
	e.conv = conv
	e.config = app.NewConfiguration(source(domain.SettingValues{}, entry("acme/shop")))
	if _, err := e.config.Load(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.console = e.newConsole(e.store)
	return e
}

// newConsole is a console over store, so that a second one can stand
// for a colleague's machine.
func (e *consoleEnv) newConsole(store *testkit.StateStore) *app.Console {
	tracker := func(context.Context, domain.Ref) (ports.TrackerPort, error) { return e.gh.Client("mrosani"), nil }
	target := func(context.Context, domain.WorkItem) (app.ItemTarget, error) {
		return app.ItemTarget{Repository: shop, Available: true, Settings: e.settings}, nil
	}
	return app.NewConsole(app.ConsoleDeps{
		Store:       store,
		Ants:        e.sup,
		Eligibility: app.NewEligibility(tracker, e.claiming, target, store),
		Candidates: app.TrackerCandidates(e.config, func(context.Context, domain.Repository) (ports.TrackerPort, error) {
			return e.gh.Client("mrosani"), nil
		}),
		Tracker:   tracker,
		Answers:   e.conv,
		Activity:  e.hub,
		Workspace: app.NewWorkspace(e.git, "/home/me/.anthill/worktrees"),
		Route: func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Clone: e.clone, Repo: shop, Settings: e.settings}, nil
		},
		Repositories: func() []domain.Repository { return []domain.Repository{shop} },
		Claims:       e.claiming,
		Clock:        e.clock,
	})
}

func (e *consoleEnv) proposed(c *app.Console) []int {
	e.t.Helper()
	var list app.ProposalList
	e.mustDo(func() (err error) { list, err = c.Proposals(e.ctx); return err })
	if len(list.Warnings) > 0 {
		e.t.Errorf("warnings = %v", list.Warnings)
	}
	var out []int
	for _, p := range list.Proposals {
		out = append(out, p.Item.Number)
	}
	return out
}

func wantNumbers(t *testing.T, what string, got []int, want ...int) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", what, got, want)
			return
		}
	}
}

// Spec queen-console, "Viewing proposals", and spec task-eligibility,
// "Proposal list": eligible items, oldest first, with their repository,
// type, priority, age and runner; an item with an ant is not proposed.
func TestConsoleProposals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		e.gh.EditItem(ghRef(13), func(i *domain.WorkItem) { i.CreatedAt = t0.Add(-time.Hour); i.Type = domain.TypeBug })
		e.gh.EditItem(ghRef(15), func(i *domain.WorkItem) { i.Labels = nil })
		e.mustAccept(12)

		var list app.ProposalList
		e.mustDo(func() (err error) { list, err = e.console.Proposals(e.ctx); return err })
		if len(list.Proposals) != 2 {
			t.Fatalf("proposals = %+v", list.Proposals)
		}
		p := list.Proposals[0]
		if p.Item != ghRef(13) || p.Title != "Fix bug 13" || p.Repository != shop || p.Type != domain.TypeBug ||
			p.Priority != "-" || !p.CreatedAt.Equal(t0.Add(-time.Hour)) || p.Runner != domain.RunnerClaudeCode {
			t.Errorf("first proposal = %+v", p)
		}
		if list.Proposals[1].Item != ghRef(14) || !list.Now.Equal(e.clock.Now()) {
			t.Errorf("list = %+v", list)
		}
	})
}

// Spec queen-console, "Why is an item not proposed".
func TestConsoleExplain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		e.gh.PutItem(domain.WorkItem{Ref: ghRef(10), Title: "Base", State: domain.StateOpen})
		e.gh.EditItem(ghRef(12), func(i *domain.WorkItem) { i.Dependencies = []domain.Ref{ghRef(10)} })
		var x app.Explanation
		e.mustDo(func() (err error) { x, err = e.console.Explain(e.ctx, ghRef(12)); return err })
		if len(x.Results) != len(domain.Checks) || x.Title != "Fix bug 12" || x.Hidden {
			t.Fatalf("explanation = %+v", x)
		}
		for _, r := range x.Results {
			want := r.Prerequisite != domain.PrereqDependencies
			if r.Met != want {
				t.Errorf("%s met = %v (%s)", r.Prerequisite, r.Met, r.Reason)
			}
			if !r.Met && r.Reason != "dependency #10 is open" {
				t.Errorf("reason = %q", r.Reason)
			}
		}
	})
}

// Spec queen-console, "Accept with a longer budget".
func TestConsoleAcceptWithALongerBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		var v app.AntView
		e.mustDo(func() (err error) {
			v, err = e.console.Accept(e.ctx, app.AcceptRequest{Item: ghRef(12), BudgetMinutes: 90})
			return err
		})
		if v.Total != 90*time.Minute || v.Runner != string(domain.RunnerClaudeCode) || v.Branch == "" {
			t.Errorf("ant = %+v", v)
		}
		rec := e.ant(v.ID)
		if !rec.State().HasActiveRun() {
			t.Errorf("state = %s, want an active run", rec.State())
		}
		if rec.Base != 90*time.Minute {
			t.Errorf("base budget = %v", rec.Base)
		}
		if n := len(e.exec.Executions()); n != 1 {
			t.Errorf("%d runs started, want the first one", n)
		}
	})
}

// Spec queen-console, "Decline is local", and spec task-eligibility,
// "Declined item changes"; snooze and un-hide.
func TestConsoleDeclineSnoozeAndUnhide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		colleague := e.newConsole(testkit.NewStateStore())
		wantNumbers(t, "proposals", e.proposed(e.console), 12, 13, 14, 15)

		before := len(e.gh.Requests())
		e.mustDo(func() error { return e.console.Decline(e.ctx, ghRef(12)) })
		for _, r := range e.gh.Requests()[before:] {
			if r.Op != testkit.OpItem {
				t.Errorf("the decline sent %s to the provider", r.Op)
			}
		}
		var until time.Time
		e.mustDo(func() (err error) { until, err = e.console.Snooze(e.ctx, ghRef(13), 2*time.Hour); return err })
		if !until.Equal(e.clock.Now().Add(2 * time.Hour)) {
			t.Errorf("snoozed until %v", until)
		}
		wantNumbers(t, "proposals after decline and snooze", e.proposed(e.console), 14, 15)
		wantNumbers(t, "proposals on a colleague's machine", e.proposed(colleague), 12, 13, 14, 15)

		var x app.Explanation
		e.mustDo(func() (err error) { x, err = e.console.Explain(e.ctx, ghRef(13)); return err })
		if !x.Hidden || !x.Until.Equal(until) {
			t.Errorf("explanation of the snoozed item = %+v", x)
		}

		// A label change does not bring the declined item back; a
		// description edit does.
		e.gh.EditItem(ghRef(12), func(i *domain.WorkItem) { i.Labels = append(i.Labels, "frontend") })
		wantNumbers(t, "after a label change", e.proposed(e.console), 14, 15)
		e.gh.EditItem(ghRef(12), func(i *domain.WorkItem) { i.Body = "Steps to reproduce, on Safari." })
		wantNumbers(t, "after a description edit", e.proposed(e.console), 12, 14, 15)

		// The snooze ends on its own, or the queen un-hides the item.
		var was bool
		e.mustDo(func() (err error) { was, err = e.console.Unhide(e.ctx, ghRef(13)); return err })
		if !was {
			t.Error("the snoozed item was not hidden")
		}
		wantNumbers(t, "after un-hide", e.proposed(e.console), 12, 13, 14, 15)
		e.mustDo(func() (err error) { _, err = e.console.Snooze(e.ctx, ghRef(14), time.Hour); return err })
		e.clock.Advance(time.Hour)
		wantNumbers(t, "after the snooze", e.proposed(e.console), 12, 13, 14, 15)
		e.mustDo(func() (err error) { was, err = e.console.Unhide(e.ctx, ghRef(15)); return err })
		if was {
			t.Error("un-hiding an item never hidden reported it hidden")
		}
		if _, err := e.console.Snooze(e.ctx, ghRef(15), 0); err == nil {
			t.Error("a zero snooze was accepted")
		}
	})
}

// Spec queen-console, "Follow an ant": the activity is streamed until
// the queen detaches, and detaching does not affect the run.
func TestConsoleFollowAnAnt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		id := e.mustAccept(12)
		x := e.execOf(ghRef(12))
		x.Emit(ports.ExecEvent{Kind: ports.ExecStarted})
		e.settle()

		var f *app.Follower
		e.mustDo(func() (err error) { f, _, err = e.console.Follow(e.ctx, ghRef(12)); return err })
		x.Emit(ports.ExecEvent{Kind: ports.ExecActivity, Line: "editing login.go"})
		e.settle()
		if ev := <-f.Events(); ev.Line != "editing login.go" {
			t.Errorf("event = %+v", ev)
		}
		f.Detach()
		x.Emit(ports.ExecEvent{Kind: ports.ExecActivity, Line: "running tests"})
		e.settle()
		e.wantState(id, domain.AntRunning)
		if x.Ended() || len(x.Stops()) > 0 {
			t.Error("detaching affected the run")
		}

		// A follower sees the run end.
		e.mustDo(func() (err error) { f, _, err = e.console.Follow(e.ctx, ghRef(12)); return err })
		e.mustDo(func() error { _, err := e.console.Stop(e.ctx, ghRef(12)); return err })
		var last app.FollowEvent
		for ev := range f.Events() {
			last = ev
		}
		if !last.Ended || last.Outcome != ports.OutcomeStopped {
			t.Errorf("last event = %+v", last)
		}

		var noRun *app.NoActiveRunError
		if _, _, err := e.console.Follow(e.ctx, ghRef(12)); !errors.As(err, &noRun) ||
			err.Error() != "the ant for #12 has no active run (it is stopped)" {
			t.Errorf("follow without a run = %v", err)
		}
		var noAnt *app.NoAntError
		if _, _, err := e.console.Follow(e.ctx, ghRef(13)); !errors.As(err, &noAnt) || err.Error() != "no ant for #13" {
			t.Errorf("follow without an ant = %v", err)
		}
	})
}

// The queen actions by item, with refusals named by the item.
func TestConsoleAntActions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		id := e.mustAccept(12)
		e.execOf(ghRef(12)).Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "sess-12"})
		e.settle()

		var v app.AntView
		e.mustDo(func() (err error) { v, err = e.console.Stop(e.ctx, ghRef(12)); return err })
		if v.State != domain.AntStopped || v.Reason != app.ReasonStopped {
			t.Errorf("after stop = %+v", v)
		}
		if _, err := e.console.Continue(e.ctx, ghRef(12), 0); err == nil || err.Error() != "cannot continue the ant for #12: it is stopped" {
			t.Errorf("continue = %v", err)
		}
		e.mustDo(func() (err error) { v, err = e.console.Retry(e.ctx, ghRef(12)); return err })
		if !v.State.HasActiveRun() {
			t.Errorf("after retry = %+v", v)
		}
		var ants []app.AntView
		e.mustDo(func() (err error) { ants, err = e.console.Ants(e.ctx, false); return err })
		if len(ants) != 1 || ants[0].ID != id || ants[0].Remaining <= 0 {
			t.Errorf("ants = %+v", ants)
		}

		var refused *app.CleanupRefusedError
		if _, err := e.console.Cleanup(e.ctx, ghRef(12), false); !errors.As(err, &refused) {
			t.Errorf("cleanup of a running ant = %v", err)
		}
		e.mustDo(func() (err error) { v, err = e.console.Abandon(e.ctx, ghRef(12)); return err })
		if v.State != domain.AntAbandoned {
			t.Errorf("after abandon = %+v", v)
		}
		e.mustDo(func() (err error) { ants, err = e.console.Ants(e.ctx, false); return err })
		if len(ants) != 0 {
			t.Errorf("ants without final ones = %+v", ants)
		}
		e.mustDo(func() (err error) { ants, err = e.console.Ants(e.ctx, true); return err })
		if len(ants) != 1 {
			t.Errorf("all ants = %+v", ants)
		}

		// The abandoned ant's worktree has nothing unpushed: it goes.
		var plan domain.CleanupPlan
		e.mustDo(func() (err error) { plan, err = e.console.Cleanup(e.ctx, ghRef(12), false); return err })
		if !plan.RemoveWorktree || !plan.DeleteLocalBranch || plan.DeleteRemoteBranch {
			t.Errorf("plan = %+v", plan)
		}

		var detail app.AntDetail
		e.mustDo(func() (err error) { detail, err = e.console.Ant(e.ctx, ghRef(12)); return err })
		if len(detail.History) < 4 || len(detail.Runs) != 2 {
			t.Errorf("detail = %+v", detail)
		}
	})
}

// Extension decisions from the console (spec ant-timebox, "Queen
// approves a smaller extension").
func TestConsoleExtensions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		e.tools.WatchBudgets(e.sup)
		id := e.startRunning(12, "sess-12")
		if _, err := e.console.DecideExtension(e.ctx, ghRef(12), app.ExtensionApprove, 0); !errors.As(err, new(*app.NoOpenExtensionError)) {
			t.Errorf("decision without a request = %v", err)
		}
		e.pass(30 * time.Minute)
		done := e.callAsync(12, domain.ToolRequestExtension, `{"minutes":45,"reason":"integration tests"}`)
		e.wantState(id, domain.AntAwaitingExtension)

		// No settle from here: the waiting call holds a timer on the clock,
		// which must not move.
		list, err := e.console.Extensions(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Item != ghRef(12) || list[0].Request.Minutes != 45 || list[0].Request.Reason != "integration tests" {
			t.Fatalf("extensions = %+v", list)
		}
		for _, m := range []int{0, 45, 60} {
			if _, err := e.console.DecideExtension(e.ctx, ghRef(12), app.ExtensionReduce, m); err == nil {
				t.Errorf("a reduction to %d minutes was accepted", m)
			}
		}
		req, err := e.console.DecideExtension(e.ctx, ghRef(12), app.ExtensionReduce, 20)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if req.Status != domain.ExtensionReduced || req.Granted != 20 {
			t.Errorf("decision = %+v", req)
		}
		if out := receive(t, done); out.Status != "reduced" || out.GrantedMinutes != 20 {
			t.Errorf("request_extension = %+v", out)
		}
		e.wantState(id, domain.AntRunning)
	})
}

// Follow-up proposals from the console (spec change-reaction,
// "Auto-follow-up disabled").
func TestConsoleFollowUps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		id := e.succeed(12)
		if _, err := e.console.AcceptFollowUp(e.ctx, ghRef(12)); err == nil || err.Error() != "the ant for #12 has no follow-up proposal" {
			t.Errorf("accept without a proposal = %v", err)
		}
		e.settings.AutoFollowUp = false
		e.poll(e.prComment(id, "lbianchi", "rename the flag"))

		var list []app.FollowUpView
		e.mustDo(func() (err error) { list, err = e.console.FollowUps(e.ctx); return err })
		if len(list) != 1 || list[0].Item != ghRef(12) || len(list[0].Changes) != 1 || list[0].Changes[0].Body != "rename the flag" {
			t.Fatalf("follow-ups = %+v", list)
		}
		var v app.AntView
		e.mustDo(func() (err error) { v, err = e.console.DeclineFollowUp(e.ctx, ghRef(12)); return err })
		if v.FollowUp != domain.ProposalDeclined {
			t.Errorf("after decline = %+v", v)
		}
		e.poll(e.prComment(id, "lbianchi", "and add a test"))
		e.mustDo(func() (err error) { v, err = e.console.AcceptFollowUp(e.ctx, ghRef(12)); return err })
		if !v.State.HasActiveRun() {
			t.Errorf("after accept = %+v", v)
		}
		e.wantNoErrors()
	})
}

// Spec queen-console, "Answer from the console", through the console:
// the question is listed, and answered by item.
func TestConsoleQuestions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		id := e.startRunning(12, "sess-12")
		e.mustCallTool(ghRef(12), domain.ToolAskQuestion, `{"text":"Does timeout mean 30 or 60 seconds?"}`)
		e.wantState(id, domain.AntAwaitingInput)

		var qs []app.QuestionView
		e.mustDo(func() (err error) { qs, err = e.console.Questions(e.ctx); return err })
		if len(qs) != 1 || qs[0].Item != ghRef(12) || qs[0].On != ghRef(12) || qs[0].Text != "Does timeout mean 30 or 60 seconds?" || qs[0].Stale {
			t.Fatalf("questions = %+v", qs)
		}
		e.mustDo(func() error { _, err := e.console.Answer(e.ctx, ghRef(12), "60 seconds."); return err })
		e.wantState(id, domain.AntRunning)
		e.mustDo(func() (err error) { qs, err = e.console.Questions(e.ctx); return err })
		if len(qs) != 0 {
			t.Errorf("questions after the answer = %+v", qs)
		}
	})
}

// A tracker that cannot be read leaves its items out with a warning; the
// others are still listed, and closed or not opted-in items never are.
func TestTrackerCandidates(t *testing.T) {
	clock := testkit.NewClock(t0)
	gh := testkit.NewProvider(clock)
	gh.PutItem(domain.WorkItem{Ref: ghRef(1), State: domain.StateOpen, Labels: []string{"anthill"}})
	gh.PutItem(domain.WorkItem{Ref: ghRef(2), State: domain.StateClosedCompleted, Labels: []string{"anthill"}})
	gh.PutItem(domain.WorkItem{Ref: ghRef(3), State: domain.StateOpen})
	config := app.NewConfiguration(source(domain.SettingValues{}, entry("acme/shop"), entry("acme/other")))
	if _, err := config.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	broken := errors.New("tracker down")
	list := app.TrackerCandidates(config, func(_ context.Context, repo domain.Repository) (ports.TrackerPort, error) {
		if repo.Name == "other" {
			return nil, broken
		}
		return gh.Client("mrosani"), nil
	})
	got, err := list(context.Background())
	if !errors.Is(err, broken) || len(got) != 1 || got[0].Item.Ref != ghRef(1) {
		t.Errorf("candidates = %+v, %v", got, err)
	}
}
