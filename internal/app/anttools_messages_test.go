package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// runWith issues a token for the ant's run on a fake runner with caps,
// and returns the token and the fake run.
func (e *toolsEnv) runWith(caps ports.Capabilities) (string, *testkit.FakeRun) {
	e.t.Helper()
	runner := testkit.NewAgentRunner(caps)
	run, err := runner.Start(context.Background(), ports.RunSpec{Worktree: antWorktree})
	if err != nil {
		e.t.Fatal(err)
	}
	token, err := e.tools.Issue(app.ToolSession{Role: domain.RoleTaskRun, Ant: e.ant, Run: 1, Channel: app.RunChannel{Capabilities: caps, Run: run}})
	if err != nil {
		e.t.Fatal(err)
	}
	return token, run.(*testkit.FakeRun)
}

// warning is the 80% warning after 48 of 60 minutes (spec ant-timebox,
// "80% reached").
func (e *toolsEnv) warning() string {
	e.clock.Advance(48 * time.Minute)
	rec := e.antRecord()
	return rec.Budget.WarningMessage(e.clock.Now())
}

// Spec ant-tools, "Runner without live input".
func TestRunnerWithoutLiveInput(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token, run := e.runWith(ports.Capabilities{Resume: true})
	warning := e.warning()
	if warning != "12 minutes remaining: wrap up and open a PR, or request more time" {
		t.Fatalf("warning = %q", warning)
	}
	means, err := e.tools.Deliver(context.Background(), e.ant, warning)
	if err != nil || means != domain.DeliverToolResult {
		t.Fatalf("Deliver = %s, %v; want tool-result", means, err)
	}
	if len(run.Inputs()) != 0 {
		t.Errorf("inputs = %v, want none", run.Inputs())
	}
	res := e.call(token, domain.ToolGetTask, `{}`)
	if res.IsError || !slices.Equal(res.Notices, []string{warning}) {
		t.Errorf("next result notices = %v, want the warning", res.Notices)
	}
	if again := e.call(token, domain.ToolGetTask, `{}`); again.Notices != nil {
		t.Errorf("the warning was attached twice: %v", again.Notices)
	}
	// A refused call carries the notice too.
	if _, err := e.tools.Deliver(context.Background(), e.ant, "ticket updated"); err != nil {
		t.Fatal(err)
	}
	if res := e.call(token, domain.ToolUpdatePullRequest, `{}`); !res.IsError || !slices.Equal(res.Notices, []string{"ticket updated"}) {
		t.Errorf("refused call = %+v, want the notice", res)
	}
}

// A runner with live input gets the message at once, as input.
func TestRunnerWithLiveInput(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token, run := e.runWith(ports.Capabilities{LiveInput: true, Hooks: true})
	warning := e.warning()
	means, err := e.tools.Deliver(context.Background(), e.ant, warning)
	if err != nil || means != domain.DeliverLiveInput {
		t.Fatalf("Deliver = %s, %v; want live-input", means, err)
	}
	if !slices.Equal(run.Inputs(), []string{warning}) {
		t.Errorf("inputs = %v, want the warning", run.Inputs())
	}
	if res := e.call(token, domain.ToolGetTask, `{}`); res.Notices != nil {
		t.Errorf("notices = %v, want none: the message was sent already", res.Notices)
	}
	if n, _ := e.tools.Notices(token); n != nil {
		t.Errorf("hook notices = %v, want none", n)
	}

	// When sending fails, the hook takes the message instead.
	run.End(ports.Exit{})
	means, err = e.tools.Deliver(context.Background(), e.ant, "ticket updated")
	if err != nil || means != domain.DeliverHook {
		t.Fatalf("Deliver after a failed send = %s, %v; want hook", means, err)
	}
	if n, _ := e.tools.Notices(token); !slices.Equal(n, []string{"ticket updated"}) {
		t.Errorf("hook notices = %v", n)
	}
}

// A runner with hooks but no live input: the hook takes the message, and
// the next tool result does not repeat it. Whichever comes first takes it.
func TestRunnerWithHooks(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	token, run := e.runWith(ports.Capabilities{Hooks: true})
	warning := e.warning()
	means, err := e.tools.Deliver(context.Background(), e.ant, warning)
	if err != nil || means != domain.DeliverHook {
		t.Fatalf("Deliver = %s, %v; want hook", means, err)
	}
	if n, err := e.tools.Notices(token); err != nil || !slices.Equal(n, []string{warning}) {
		t.Errorf("Notices = %v, %v; want the warning", n, err)
	}
	if res := e.call(token, domain.ToolGetTask, `{}`); res.Notices != nil {
		t.Errorf("notices = %v, want none: the hook took it", res.Notices)
	}
	if len(run.Inputs()) != 0 {
		t.Errorf("inputs = %v, want none", run.Inputs())
	}

	if _, err := e.tools.Deliver(context.Background(), e.ant, "ticket updated"); err != nil {
		t.Fatal(err)
	}
	if res := e.call(token, domain.ToolGetTask, `{}`); !slices.Equal(res.Notices, []string{"ticket updated"}) {
		t.Errorf("notices = %v, want the update before the hook ran", res.Notices)
	}
	if n, _ := e.tools.Notices(token); n != nil {
		t.Errorf("hook notices = %v, want none left", n)
	}
}

// A late extension decision goes to the run by the same means (spec
// ant-timebox, "Late extension decisions").
func TestLateDecisionByLiveInput(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	_, run := e.runWith(ports.Capabilities{LiveInput: true})
	rec := e.antRecord()
	if _, err := rec.Budget.RequestExtension(30, "tests", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveAnt(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.SaveExtension(context.Background(), ports.ExtensionRecord{Ant: e.ant, Request: *rec.Budget.Open}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.tools.DecideExtension(context.Background(), e.ant, 0); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(run.Inputs(), []string{"extension denied: wrap up within the remaining time"}) {
		t.Errorf("inputs = %q", run.Inputs())
	}
}

func TestDeliverWithoutActiveRun(t *testing.T) {
	e := newToolsEnv(t, domain.DefaultBudgetPolicy())
	if _, err := e.tools.Deliver(context.Background(), e.ant, "x"); !errors.Is(err, app.ErrNoActiveRun) {
		t.Errorf("Deliver without a token = %v, want ErrNoActiveRun", err)
	}
	token := e.issue(domain.RoleTaskRun)
	if _, err := e.tools.Deliver(context.Background(), e.ant, "x"); err != nil {
		t.Fatal(err)
	}
	// Revoking the run's token drops what it did not take.
	e.tools.Revoke(token)
	next := e.issue(domain.RoleTaskRun)
	if res := e.call(next, domain.ToolGetTask, `{}`); res.Notices != nil {
		t.Errorf("notices of the next run = %v, want none", res.Notices)
	}
	if _, err := e.tools.Notices(token); !errors.Is(err, app.ErrUnknownToken) {
		t.Errorf("Notices with a revoked token = %v", err)
	}
}
