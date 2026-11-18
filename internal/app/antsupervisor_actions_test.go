package app_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// newActionsEnv is a supervisor whose queen actions say which run is due
// after a retry or a continue, and whose timekeeper reaches the ant
// tools.
func newActionsEnv(t *testing.T, capacity int) *supEnv {
	t.Helper()
	e := newSupEnv(t, capacity, func(d *app.AntSupervisorDeps) {
		d.Due = nil
		tools := d.Tokens.(*app.AntTools)
		d.Messages, d.Extensions = tools, tools
	})
	e.tools.WatchBudgets(e.sup)
	return e
}

// startRunning accepts the item and reports the run's session.
func (e *supEnv) startRunning(n int, session string) ports.AntID {
	e.t.Helper()
	id := e.mustAccept(n)
	e.execOf(ghRef(n)).Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: session})
	e.settle()
	e.wantState(id, domain.AntRunning)
	return id
}

// pass moves the clock on by d while the daemon renews the held claims
// every 5 minutes, as Claiming.Run does.
func (e *supEnv) pass(d time.Duration) {
	e.t.Helper()
	for d > 0 {
		step := min(d, 5*time.Minute)
		e.clock.Advance(step)
		d -= step
		done := make(chan error, 1)
		go func() { done <- e.claiming.Renew(e.ctx) }()
		e.settle()
		if err := <-done; err != nil {
			e.t.Fatal(err)
		}
	}
}

// do runs a queen action and lets everything it starts settle.
func (e *supEnv) do(action func() error) error {
	done := make(chan error, 1)
	go func() { done <- action() }()
	e.settle()
	err := <-done
	e.settle()
	return err
}

func (e *supEnv) mustDo(action func() error) {
	e.t.Helper()
	if err := e.do(action); err != nil {
		e.t.Fatal(err)
	}
}

// Spec ant-execution, "Stop and retry later".
func TestStopAndRetryLater(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		first := e.execOf(ghRef(12))
		worktree := e.ant(id).Worktree
		e.pass(20 * time.Minute)

		e.mustDo(func() error { return e.sup.Stop(e.ctx, id, "") })
		rec := e.wantState(id, domain.AntStopped)
		if r := lastReason(rec); r != app.ReasonStopped {
			t.Errorf("reason = %q", r)
		}
		if got := first.Stops(); !slices.Equal(got, []time.Duration{app.StopGrace}) || app.StopGrace >= 10*time.Second {
			t.Errorf("stops = %v, want one within 10 seconds", got)
		}
		ends := e.ends.all()
		if len(ends) != 1 || ends[0].run.Outcome != ports.OutcomeStopped || ends[0].run.Reason != app.ReasonStopped {
			t.Errorf("ends = %+v", ends)
		}
		e.wantParked(12)
		e.wantActive()
		if got := e.git.RemoteBranches(supClone); slices.Contains(got, rec.Branch) {
			t.Errorf("remote branches = %v: the stop pushed", got)
		}
		if got := e.git.Worktrees(supClone); !slices.Equal(got, []string{worktree}) {
			t.Errorf("worktrees = %v, want %s kept", got, worktree)
		}

		// The next day the claim is still parked, and the retry resumes
		// the same session in the same worktree with the budget left.
		e.clock.Advance(24 * time.Hour)
		e.wantParked(12)
		e.mustDo(func() error { return e.sup.Retry(e.ctx, id) })
		rec = e.wantState(id, domain.AntRunning)
		x := e.execOf(ghRef(12))
		if x == first {
			t.Fatal("no new run")
		}
		if x.Spec.Session != "sess-12" || x.Spec.Run.Worktree != worktree || x.Spec.Run.Budget != 40*time.Minute {
			t.Errorf("spec = %+v, want sess-12 in %s with 40m", x.Spec, worktree)
		}
		if want := e.clock.Now().Add(40*time.Minute + domain.DefaultGracePeriod); !x.Spec.Deadline.Equal(want) {
			t.Errorf("deadline = %v, want %v", x.Spec.Deadline, want)
		}
		if !strings.HasPrefix(x.Spec.Run.Brief, domain.RetryNote(domain.AntStopped, app.ReasonStopped)) ||
			!strings.Contains(x.Spec.Run.Brief, "resume brief") {
			t.Errorf("input = %q", x.Spec.Run.Brief)
		}
		if h, ok := e.heldClaim(12); !ok || h.Parked {
			t.Errorf("claim = %+v, want held and no longer parked", h)
		}
		n := len(rec.History)
		if q := rec.History[n-3]; q.From != domain.AntStopped || q.To != domain.AntQueued || q.Reason != app.ReasonRetry {
			t.Errorf("history = %+v", rec.History)
		}
	})
}

// Spec ant-execution, "Retry after crash"; spec ant-timebox, "Retry
// after a failure".
func TestRetryAfterCrash(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		worktree := e.ant(id).Worktree
		e.pass(40 * time.Minute)
		e.execOf(ghRef(12)).End(ports.ExecExited, ports.Exit{Code: 1})
		e.settle()
		e.wantState(id, domain.AntFailed)

		e.mustDo(func() error { return e.sup.Retry(e.ctx, id) })
		e.wantState(id, domain.AntRunning)
		x := e.execOf(ghRef(12))
		if x.Spec.Session != "sess-12" || x.Spec.Run.Worktree != worktree || x.Spec.Run.Budget != 20*time.Minute {
			t.Errorf("spec = %+v, want sess-12 in %s with 20m", x.Spec, worktree)
		}
		if want := domain.RetryNote(domain.AntFailed, app.ReasonExitedWithoutFinish); !strings.HasPrefix(x.Spec.Run.Brief, want) {
			t.Errorf("input = %q, want the note %q first", x.Spec.Run.Brief, want)
		}
		if runs := e.runs(id); len(runs) != 2 || runs[1].Kind != ports.RunTask || runs[1].SessionID != "sess-12" {
			t.Errorf("runs = %+v", runs)
		}
		if got := e.git.Worktrees(supClone); !slices.Equal(got, []string{worktree}) {
			t.Errorf("worktrees = %v", got)
		}

		// A run that cannot resume gets a summary brief.
		e.execOf(ghRef(12)).End(ports.ExecExited, ports.Exit{Code: 1})
		e.settle()
		rec := e.wantState(id, domain.AntFailed)
		rec.SessionID = ""
		if err := e.store.SaveAnt(e.ctx, rec); err != nil {
			t.Fatal(err)
		}
		e.mustDo(func() error { return e.sup.Retry(e.ctx, id) })
		if b := e.briefs.last(); b.kind != "summary" {
			t.Errorf("brief = %s, want summary", b.kind)
		}
	})
}

// Spec ant-execution, "Capacity limits active runs", for a retry.
func TestRetryWhileCapacityIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 1)
		id := e.startRunning(12, "sess-12")
		e.end(ghRef(12), ports.ExecExited)
		e.wantState(id, domain.AntFailed)
		e.mustAccept(13)

		e.mustDo(func() error { return e.sup.Retry(e.ctx, id) })
		e.wantState(id, domain.AntQueued)
		e.wantActive(13)
		// A second retry of the queued ant does nothing.
		e.mustDo(func() error { return e.sup.Retry(e.ctx, id) })
		e.wantState(id, domain.AntQueued)

		e.end(ghRef(13), ports.ExecExited)
		e.wantState(id, domain.AntRunning)
		if x := e.execOf(ghRef(12)); x.Spec.Session != "sess-12" {
			t.Errorf("session = %q", x.Spec.Session)
		}
	})
}

// Spec ant-execution, "Abandon an ant with an active run".
func TestAbandonAnAntWithAnActiveRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 1)
		id := e.startRunning(12, "sess-12")
		worktree := e.ant(id).Worktree
		x := e.execOf(ghRef(12))
		other := e.mustAccept(13)
		e.wantState(other, domain.AntQueued)

		e.mustDo(func() error { return e.sup.Abandon(e.ctx, id, "") })
		rec := e.wantState(id, domain.AntAbandoned)
		if r := lastReason(rec); r != app.ReasonAbandoned {
			t.Errorf("reason = %q", r)
		}
		if got := x.Stops(); !slices.Equal(got, []time.Duration{app.StopGrace}) {
			t.Errorf("stops = %v", got)
		}
		if e.claimed(12) {
			t.Error("the claim is still held")
		}
		if item, _ := e.gh.Item(ghRef(12)); domain.HasLabel(item.Labels, domain.DefaultClaimLabel) {
			t.Errorf("labels = %v, want no claim label", item.Labels)
		}
		if got := e.git.Worktrees(supClone); !slices.Contains(got, worktree) {
			t.Errorf("worktrees = %v, want %s kept", got, worktree)
		}
		ends := e.ends.all()
		if len(ends) != 1 || ends[0].run.Outcome != ports.OutcomeStopped || ends[0].run.Reason != app.ReasonAbandoned {
			t.Errorf("ends = %+v", ends)
		}
		// The freed slot goes to the queued ant.
		e.wantState(other, domain.AntRunning)
	})
}

// Abandon works between runs too, and closes an open extension request.
func TestAbandonBetweenRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 1)
		e.startRunning(12, "sess-12")
		queued := e.mustAccept(13)
		e.wantState(queued, domain.AntQueued)
		e.mustDo(func() error { return e.sup.Abandon(e.ctx, queued, "") })
		e.wantState(queued, domain.AntAbandoned)

		id, _ := e.store.CurrentAnt(e.ctx, ghRef(12))
		e.callAsync(12, domain.ToolRequestExtension, `{"minutes":30,"reason":"tests left"}`)
		e.end(ghRef(12), ports.ExecExited)
		e.wantState(id.ID, domain.AntFailed)
		e.mustDo(func() error { return e.sup.Abandon(e.ctx, id.ID, "") })
		e.wantState(id.ID, domain.AntAbandoned)
		if e.claimed(12) {
			t.Error("the claim is still held")
		}
		if pending, _ := e.store.PendingExtensions(e.ctx); len(pending) != 0 {
			t.Errorf("pending extensions = %+v", pending)
		}
	})
}

func TestQueenActionsRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		id := e.startRunning(12, "sess-12")
		var refused *app.ActionRefusedError
		for name, action := range map[string]func() error{
			"retry":    func() error { return e.sup.Retry(e.ctx, id) },
			"continue": func() error { return e.sup.Continue(e.ctx, id, time.Hour) },
		} {
			if err := e.do(action); !errors.As(err, &refused) {
				t.Errorf("%s of a running ant = %v, want refused", name, err)
			}
		}
		e.move(id, domain.AntFinishing, "finish called")
		if err := e.do(func() error { return e.sup.Stop(e.ctx, id, "") }); !errors.As(err, &refused) || err.Error() != "cannot stop ant 1: it is finishing" {
			t.Errorf("stop of a finishing ant = %v, want refused", err)
		}
		e.end(ghRef(12), ports.ExecExited)
		if err := e.do(func() error { return e.sup.Stop(e.ctx, id, "") }); !errors.As(err, &refused) {
			t.Errorf("stop of a failed ant = %v, want refused", err)
		}
		e.mustDo(func() error { return e.sup.Abandon(e.ctx, id, "") })
		if err := e.do(func() error { return e.sup.Abandon(e.ctx, id, "") }); !errors.As(err, &refused) || err.Error() != "cannot abandon ant 1: it is abandoned" {
			t.Errorf("abandon of an abandoned ant = %v, want refused", err)
		}
	})
}

// callAsync calls an ant tool of the item's run in the background and
// returns its result channel, once the call waits or has returned.
func (e *supEnv) callAsync(n int, tool, args string) <-chan app.ToolResult {
	token := e.execOf(ghRef(n)).Spec.Run.ToolToken
	done := make(chan app.ToolResult, 1)
	go func() { done <- e.tools.Call(e.ctx, token, tool, json.RawMessage(args)) }()
	synctest.Wait()
	return done
}

func (e *supEnv) call(n int, tool, args string, out any) {
	e.t.Helper()
	res := <-e.callAsync(n, tool, args)
	if res.IsError {
		e.t.Fatalf("%s = %s", tool, res.Text)
	}
	if err := json.Unmarshal([]byte(res.Text), out); err != nil {
		e.t.Fatalf("%s result %q: %v", tool, res.Text, err)
	}
}
