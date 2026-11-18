package app_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The timekeeper tests time a run of the ant for gh-12, accepted at
// start with the default 60-minute budget, a warning at 80% and a
// 3-minute grace period.

func (e *supEnv) tick() {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.sup.Tick(e.ctx) }()
	synctest.Wait()
	if err := <-done; err != nil {
		e.t.Fatalf("Tick = %v", err)
	}
}

// wantDeadline checks the deadline the executor has for the item's run.
func (e *supEnv) wantDeadline(n int, want time.Time) {
	e.t.Helper()
	if got := e.execOf(ghRef(n)).Deadline(); !got.Equal(want) {
		e.t.Fatalf("deadline = %v, want %v", got, want)
	}
}

func (e *supEnv) decide(id ports.AntID, minutes int) domain.ExtensionRequest {
	e.t.Helper()
	// No settle: a request_extension call that waits holds a timer on the
	// clock, which must not move.
	type result struct {
		req domain.ExtensionRequest
		err error
	}
	done := make(chan result, 1)
	go func() {
		req, err := e.sup.DecideExtension(e.ctx, id, minutes)
		done <- result{req, err}
	}()
	synctest.Wait()
	r := <-done
	if r.err != nil {
		e.t.Fatalf("DecideExtension = %v", r.err)
	}
	synctest.Wait()
	return r.req
}

type extensionOut struct {
	Status           string `json:"status"`
	GrantedMinutes   int    `json:"granted_minutes"`
	RemainingMinutes int    `json:"remaining_minutes"`
	Message          string `json:"message"`
}

func receive(t *testing.T, done <-chan app.ToolResult) extensionOut {
	t.Helper()
	synctest.Wait()
	select {
	case res := <-done:
		var out extensionOut
		if res.IsError {
			t.Fatalf("request_extension = %s", res.Text)
		}
		if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
			t.Fatal(err)
		}
		return out
	default:
		t.Fatal("request_extension is still waiting")
	}
	return extensionOut{}
}

// Spec ant-timebox, "80% reached".
func TestWarningAtTheThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		x := e.execOf(ghRef(12))
		e.pass(47 * time.Minute)
		e.tick()
		if got := x.Inputs(); len(got) != 0 {
			t.Fatalf("inputs at 47 minutes = %v", got)
		}
		e.pass(time.Minute)
		e.tick()
		want := "12 minutes remaining: wrap up and open a PR, or request more time"
		if got := x.Inputs(); !slices.Equal(got, []string{want}) {
			t.Fatalf("inputs at 48 minutes = %v, want %q", got, want)
		}
		if !e.ant(id).Budget.Warned {
			t.Error("the warning is not recorded")
		}
		sent := e.notifier.Sent()
		if len(sent) == 0 || sent[len(sent)-1].Kind != ports.NotifyWarning || !strings.Contains(sent[len(sent)-1].Body, want) {
			t.Errorf("notifications = %+v, want the queen told the ant is near its limit", sent)
		}
		// The warning is given once.
		e.pass(time.Minute)
		e.tick()
		if got := x.Inputs(); len(got) != 1 {
			t.Errorf("inputs = %v", got)
		}
	})
}

// Spec ant-tools, "Runner without live input": the warning goes with the
// next ant-tool result.
func TestWarningWithoutLiveInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		e.exec.SetCapabilities(domain.RunnerClaudeCode, ports.Capabilities{Resume: true})
		e.startRunning(12, "sess-12")
		e.pass(48 * time.Minute)
		e.tick()
		var out struct{}
		res := <-e.callAsync(12, domain.ToolTimeRemaining, `{}`)
		if res.IsError || !strings.Contains(res.Text, "12 minutes remaining") {
			t.Errorf("time_remaining = %+v, want the warning attached", res)
		}
		_ = out
	})
}

// Spec ant-timebox, "Time runs out", end to end through the timekeeper:
// the notice to stop when the budget runs out, the end at the deadline,
// and the wrap-up of the timed-out ant. TestTimeRunsOut and
// TestTimeRunsOutWithNoCommits check the wrap-up in detail.
func TestTimeRunsOutEndToEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		e.commit(id, "WIP")
		start := e.clock.Now()
		x := e.execOf(ghRef(12))
		e.wantDeadline(12, start.Add(63*time.Minute))
		e.pass(60 * time.Minute)
		e.tick()
		inputs := x.Inputs()
		if want := domain.StopNotice(domain.DefaultGracePeriod); len(inputs) == 0 || inputs[len(inputs)-1] != want {
			t.Fatalf("inputs = %v, want the notice to stop %q", inputs, want)
		}
		e.tick()
		if got := x.Inputs(); len(got) != len(inputs) {
			t.Errorf("inputs = %v: the notice to stop was given twice", got)
		}

		// At the deadline the executor ends the run.
		e.pass(3 * time.Minute)
		e.end(ghRef(12), ports.ExecEndedAtDeadline)
		rec := e.wantState(id, domain.AntTimedOut)
		if r := lastReason(rec); r != app.ReasonBudgetUsedUp {
			t.Errorf("reason = %q", r)
		}
		if ends := e.ends.all(); len(ends) != 1 || ends[0].run.Outcome != ports.OutcomeTimedOut {
			t.Errorf("ends = %+v", ends)
		}
		if got := e.pushed(rec); len(got) != 1 {
			t.Errorf("pushed commits = %v", got)
		}
		pr := e.onePR(id)
		if !pr.Draft || rec.PR == nil || *rec.PR != pr.Ref {
			t.Errorf("PR = %+v, record PR %v; want a draft", pr, rec.PR)
		}
		if got := e.summaries(12); len(got) != 1 || !strings.Contains(got[0], pr.URL) {
			t.Errorf("summary comments = %q, want one linking %s", got, pr.URL)
		}
		e.wantParked(12)
		if !slices.Contains(e.notifier.Kinds(), domain.NotifyTimedOut) {
			t.Errorf("notifications = %v, want timed-out", e.notifier.Kinds())
		}
		e.wantNoErrors()
	})
}

// Spec ant-timebox, "Auto-approved".
func TestAutoApprovedExtensionMovesTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		e.settings.ExtensionAllowance = domain.ExtensionAllowance{MaxMinutes: 15, Times: 1}
		e.startRunning(12, "sess-12")
		start := e.clock.Now()
		e.pass(30 * time.Minute)
		var out extensionOut
		e.call(12, domain.ToolRequestExtension, `{"minutes":10,"reason":"one more test"}`, &out)
		if out.Status != "approved" || out.GrantedMinutes != 10 {
			t.Errorf("request_extension = %+v", out)
		}
		e.wantDeadline(12, start.Add(73*time.Minute))
	})
}

// Spec ant-timebox, "Queen approves a smaller extension": the deadline
// is cleared while the call waits, and set again from the grown budget.
func TestQueenApprovesASmallerExtension(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		e.pass(30 * time.Minute)
		done := e.callAsync(12, domain.ToolRequestExtension, `{"minutes":45,"reason":"integration tests"}`)
		e.wantState(id, domain.AntAwaitingExtension)
		e.wantDeadline(12, time.Time{})

		// Two minutes waiting do not count.
		e.clock.Advance(2 * time.Minute)
		synctest.Wait()
		e.decide(id, 20)
		out := receive(t, done)
		if out.Status != "reduced" || out.GrantedMinutes != 20 || out.RemainingMinutes != 50 {
			t.Errorf("request_extension = %+v, want reduced 20 with 50 left", out)
		}
		e.wantState(id, domain.AntRunning)
		e.wantDeadline(12, e.clock.Now().Add(50*time.Minute+domain.DefaultGracePeriod))
	})
}

// Spec ant-timebox, "Queen denies".
func TestQueenDeniesTheExtension(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		start := e.clock.Now()
		e.pass(30 * time.Minute)
		done := e.callAsync(12, domain.ToolRequestExtension, `{"minutes":45,"reason":"integration tests"}`)
		e.decide(id, 0)
		out := receive(t, done)
		if out.Status != "denied" || !strings.Contains(out.Message, "wrap up within the remaining time") {
			t.Errorf("request_extension = %+v", out)
		}
		e.wantState(id, domain.AntRunning)
		e.wantDeadline(12, start.Add(63*time.Minute))
	})
}

// Spec ant-timebox, "No decision within the wait", then a late approval
// on the active run (spec ant-timebox, "Late extension decisions").
func TestNoDecisionWithinTheWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		x := e.execOf(ghRef(12))
		e.pass(56 * time.Minute)
		done := e.callAsync(12, domain.ToolRequestExtension, `{"minutes":30,"reason":"integration tests"}`)
		e.wantDeadline(12, time.Time{})
		e.clock.Advance(domain.DefaultExtensionWait)
		out := receive(t, done)
		if out.Status != "pending" || out.RemainingMinutes != 4 {
			t.Errorf("request_extension = %+v, want pending with 4 minutes left", out)
		}
		rec := e.wantState(id, domain.AntRunning)
		if rec.Budget.Open == nil {
			t.Error("the request is no longer open")
		}
		if pending, _ := e.store.PendingExtensions(e.ctx); len(pending) != 1 {
			t.Errorf("pending extensions = %+v", pending)
		}
		e.wantDeadline(12, e.clock.Now().Add(4*time.Minute+domain.DefaultGracePeriod))

		// The queen approves later: the budget grows and the run is told.
		e.pass(time.Minute)
		e.decide(id, 30)
		e.wantDeadline(12, e.clock.Now().Add(33*time.Minute+domain.DefaultGracePeriod))
		inputs := x.Inputs()
		if len(inputs) == 0 || !strings.Contains(inputs[len(inputs)-1], "extension approved: 30 minutes added") {
			t.Errorf("inputs = %v", inputs)
		}
	})
}

// Spec ant-timebox, "Budget runs out while the request is open" and
// "Late approval on a timed-out ant".
func TestLateApprovalOnATimedOutAnt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		worktree := e.ant(id).Worktree
		e.pass(56 * time.Minute)
		done := e.callAsync(12, domain.ToolRequestExtension, `{"minutes":30,"reason":"integration tests"}`)
		e.clock.Advance(domain.DefaultExtensionWait)
		receive(t, done)

		e.pass(4 * time.Minute)
		e.tick()
		e.pass(domain.DefaultGracePeriod)
		e.end(ghRef(12), ports.ExecEndedAtDeadline)
		rec := e.wantState(id, domain.AntTimedOut)
		if rec.Budget.Open == nil || rec.Budget.Open.Minutes != 30 {
			t.Fatalf("open request = %+v, want the request still open", rec.Budget.Open)
		}

		e.pass(time.Hour)
		req := e.decide(id, 30)
		if req.Status != domain.ExtensionApproved {
			t.Errorf("decision = %+v", req)
		}
		e.wantState(id, domain.AntRunning)
		x := e.execOf(ghRef(12))
		if x.Spec.Session != "sess-12" || x.Spec.Run.Worktree != worktree || x.Spec.Run.Budget != 30*time.Minute {
			t.Errorf("spec = %+v, want sess-12 in %s with 30m", x.Spec, worktree)
		}
		e.wantDeadline(12, e.clock.Now().Add(33*time.Minute))
		if !strings.HasPrefix(x.Spec.Run.Brief, domain.ContinueNote(30*time.Minute)) {
			t.Errorf("input = %q", x.Spec.Run.Brief)
		}
		if pending, _ := e.store.PendingExtensions(e.ctx); len(pending) != 0 {
			t.Errorf("pending extensions = %+v", pending)
		}
	})
}

// Spec ant-timebox, "Queen continues a timed-out ant".
func TestQueenContinuesATimedOutAnt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		worktree := e.ant(id).Worktree
		e.pass(63 * time.Minute)
		e.end(ghRef(12), ports.ExecEndedAtDeadline)
		e.wantState(id, domain.AntTimedOut)

		e.mustDo(func() error { return e.sup.Continue(e.ctx, id, 30*time.Minute) })
		rec := e.wantState(id, domain.AntRunning)
		x := e.execOf(ghRef(12))
		if x.Spec.Session != "sess-12" || x.Spec.Run.Worktree != worktree || x.Spec.Run.Budget != 30*time.Minute {
			t.Errorf("spec = %+v, want sess-12 in %s with 30m", x.Spec, worktree)
		}
		if rec.Budget.Initial != 30*time.Minute || rec.Budget.Warned {
			t.Errorf("budget = %+v, want a new 30-minute budget", rec.Budget)
		}
		e.wantDeadline(12, e.clock.Now().Add(33*time.Minute))
	})
}

// Spec ant-timebox, "Ceiling reached".
func TestCeilingReached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		for range 2 {
			done := e.callAsync(12, domain.ToolRequestExtension, `{"minutes":60,"reason":"more"}`)
			e.decide(id, 60)
			receive(t, done)
		}
		var out extensionOut
		e.call(12, domain.ToolRequestExtension, `{"minutes":10,"reason":"more"}`, &out)
		if out.Status != "denied" || out.Message != domain.MessageMaxBudget {
			t.Errorf("request_extension = %+v, want maximum budget reached", out)
		}
		e.wantDeadline(12, e.clock.Now().Add(180*time.Minute+domain.DefaultGracePeriod))
	})
}

// A finishing ant gets no warning or notice to stop, and its deadline
// stays: the finish handling ends its run.
func TestFinishingAntIsNotTimed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		start := e.clock.Now()
		e.pass(47 * time.Minute)
		e.move(id, domain.AntFinishing, "finish called")
		e.pass(14 * time.Minute)
		e.tick()
		if got := e.execOf(ghRef(12)).Inputs(); len(got) != 0 {
			t.Errorf("inputs = %v", got)
		}
		if err := e.sup.BudgetChanged(e.ctx, id); err != nil {
			t.Fatal(err)
		}
		e.wantDeadline(12, start.Add(63*time.Minute))
	})
}
