package app_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
	"github.com/matteorosani/anthill/internal/testkit/storetest"
)

// fakeWrapUp records the wrap-ups of exits without finishing.
type fakeWrapUp struct {
	mu    sync.Mutex
	calls []wrapUpCall
}

type wrapUpCall struct {
	ant   ports.AntID
	state domain.AntState
	run   ports.RunID
	req   app.WrapUpRequest
}

func (f *fakeWrapUp) WrapUp(_ context.Context, rec ports.AntRecord, run ports.RunID, w app.WrapUpRequest) (ports.AntRecord, app.WrapUpResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, wrapUpCall{rec.ID, rec.State(), run, w})
	return rec, app.WrapUpResult{}, nil
}

// wantInterruptedWrapUp checks one call: the ant failed, the run, and the
// restart's reason and summary, with no draft opened.
func wantInterruptedWrapUp(t *testing.T, c wrapUpCall, ant ports.AntID, run ports.RunID) {
	t.Helper()
	if c.ant != ant || c.state != domain.AntFailed || c.run != run || c.req.Reason != app.ReasonInterruptedByRestart ||
		c.req.OpenDraft || c.req.Summary != app.SummaryInterruptedByRestart {
		t.Errorf("wrap-up %+v, want ant %d failed, run %d, interrupted by restart", c, ant, run)
	}
}

func (f *fakeWrapUp) all() []wrapUpCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wrapUpCall(nil), f.calls...)
}

// fakeRetrier records the ants retried.
type fakeRetrier struct {
	mu   sync.Mutex
	ants []ports.AntID
}

func (f *fakeRetrier) Retry(_ context.Context, ant ports.AntID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ants = append(f.ants, ant)
	return nil
}

func (f *fakeRetrier) all() []ports.AntID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.AntID(nil), f.ants...)
}

// leftRunning stores what a daemon that stopped mid-run leaves for item
// n: a live task claim, and an ant in state with an active run of session
// sess. The process is gone.
func (e *supEnv) leftRunning(n int, sess string, states ...domain.AntState) (ports.AntID, ports.RunID) {
	e.t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := e.claiming.Acquire(e.ctx, ghRef(n), domain.ClaimTask)
		done <- err
	}()
	e.settle()
	if err := <-done; err != nil {
		e.t.Fatal(err)
	}
	rec := storetest.Ant(n, states...)
	rec.Item, rec.Runner, rec.SessionID = ghRef(n), string(domain.RunnerClaudeCode), sess
	id, err := e.store.CreateAnt(e.ctx, rec)
	if err != nil {
		e.t.Fatal(err)
	}
	rec.ID = id
	run, err := e.store.BeginRun(e.ctx, ports.RunStart{
		Ant: rec, Run: ports.RunRecord{Ant: id, Kind: ports.RunTask, SessionID: sess, StartedAt: e.clock.Now()}, At: e.clock.Now(),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.clock.Advance(10 * time.Minute)
	return id, run
}

func (e *supEnv) reconcile(deps app.RestartDeps) {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.sup.Reconcile(e.ctx, deps) }()
	e.settle()
	if err := <-done; err != nil {
		e.t.Fatalf("Reconcile() = %v", err)
	}
	e.settle()
}

// Task 14.8, scenario "Daemon restarted mid-run", without auto-resume:
// the ant is handled as an exit without finishing with reason
// "interrupted by restart", its run ends with unknown usage, its claim is
// kept, and no run starts.
func TestDaemonRestartedMidRun(t *testing.T) {
	// A finishing ant without a stored finish is interrupted too.
	for _, state := range []domain.AntState{domain.AntStarting, domain.AntRunning, domain.AntAwaitingExtension, domain.AntFinishing} {
		t.Run(string(state), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newSupEnv(t, 2)
				states := []domain.AntState{domain.AntStarting, domain.AntRunning, state}
				switch state {
				case domain.AntStarting:
					states = states[:1]
				case domain.AntRunning:
					states = states[:2]
				}
				id, run := e.leftRunning(12, "sess-12", states...)
				wrap := &fakeWrapUp{}
				e.reconcile(app.RestartDeps{WrapUp: wrap, Retry: &fakeRetrier{}})

				rec := e.wantState(id, domain.AntFailed)
				if lastReason(rec) != app.ReasonInterruptedByRestart {
					t.Errorf("reason %q", lastReason(rec))
				}
				ends := e.ends.all()
				if len(ends) != 1 || ends[0].run.ID != run || ends[0].run.Outcome != ports.OutcomeFailed ||
					ends[0].run.Reason != app.ReasonInterruptedByRestart || ends[0].reported != (ports.Usage{}) ||
					ends[0].resumed != "sess-12" || !ends[0].run.EndedAt.Equal(e.clock.Now()) {
					t.Errorf("run ends %+v", ends)
				}
				if calls := wrap.all(); len(calls) != 1 {
					t.Errorf("wrap-ups %+v", calls)
				} else {
					wantInterruptedWrapUp(t, calls[0], id, run)
				}
				if n := countKind(e.notifier, domain.NotifyFailed); n != 1 {
					t.Errorf("%d failure notifications", n)
				}
				if !e.claimed(12) {
					t.Error("the claim was not kept")
				}
				if x := e.exec.Executions(); len(x) != 0 {
					t.Errorf("%d runs started", len(x))
				}
				if rec.SessionID != "sess-12" {
					t.Errorf("session %q, want it kept for a retry", rec.SessionID)
				}

				// A second reconciliation changes nothing.
				e.reconcile(app.RestartDeps{WrapUp: wrap})
				if len(wrap.all()) != 1 || len(e.ends.all()) != 1 || len(e.ant(id).History) != len(rec.History) {
					t.Error("a second reconciliation handled the ant again")
				}
			})
		})
	}
}

// Task 14.8, scenario "Daemon restarted mid-run", with auto-resume: the
// ant is resumed automatically through the retry, gets no wrap-up, and
// keeps its claim.
func TestDaemonRestartedMidRunAutoResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		e.settings.AutoResume = true
		id, _ := e.leftRunning(12, "sess-12", domain.AntStarting, domain.AntRunning)
		wrap, retry := &fakeWrapUp{}, &fakeRetrier{}
		e.reconcile(app.RestartDeps{WrapUp: wrap, Retry: retry})

		rec := e.wantState(id, domain.AntFailed)
		if lastReason(rec) != app.ReasonInterruptedByRestart {
			t.Errorf("reason %q", lastReason(rec))
		}
		if got := retry.all(); len(got) != 1 || got[0] != id {
			t.Errorf("retried %v", got)
		}
		if calls := wrap.all(); len(calls) != 0 {
			t.Errorf("wrap-ups %+v", calls)
		}
		if ends := e.ends.all(); len(ends) != 1 || ends[0].run.Reason != app.ReasonInterruptedByRestart {
			t.Errorf("run ends %+v", ends)
		}
		if !e.claimed(12) {
			t.Error("the claim was not kept")
		}
	})
}

// Task 14.8: without a retry to resume with, an auto-resume repository's
// ant is handled as an exit without finishing.
func TestRestartAutoResumeWithoutRetrier(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		e.settings.AutoResume = true
		id, _ := e.leftRunning(12, "sess-12", domain.AntStarting, domain.AntRunning)
		wrap := &fakeWrapUp{}
		e.reconcile(app.RestartDeps{WrapUp: wrap})
		e.wantState(id, domain.AntFailed)
		if len(wrap.all()) != 1 {
			t.Errorf("wrap-ups %+v", wrap.all())
		}
	})
}

// Task 14.8: a finishing ant whose finish is stored is not interrupted:
// the playbook carries out the finish.
func TestRestartTriggersAFinishingAnt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id, _ := e.leftRunning(12, "sess-12", domain.AntStarting, domain.AntRunning, domain.AntFinishing)
		wrap := &fakeWrapUp{}
		var asked []ports.AntID
		e.reconcile(app.RestartDeps{WrapUp: wrap, PendingFinish: func(rec ports.AntRecord) bool {
			asked = append(asked, rec.ID)
			return true
		}})
		e.wantState(id, domain.AntFinishing)
		if len(asked) != 1 || asked[0] != id {
			t.Errorf("pending finish asked for %v", asked)
		}
		if len(wrap.all()) != 0 || len(e.ends.all()) != 0 {
			t.Errorf("wrap-ups %+v, run ends %+v", wrap.all(), e.ends.all())
		}
	})
}

// Task 14.8: queued ants are rescheduled at restart, in queue order,
// within capacity.
func TestRestartStartsQueuedAnts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 1)
		first := e.queuedAnt(12)
		e.clock.Advance(time.Minute)
		second := e.queuedAnt(13)

		e.reconcile(app.RestartDeps{WrapUp: &fakeWrapUp{}})
		e.wantState(first, domain.AntRunning)
		e.wantState(second, domain.AntQueued)
		e.wantActive(12)
		if b := e.briefs.last(); b.kind != "task" || b.item != ghRef(12) {
			t.Errorf("brief %+v", b)
		}
	})
}

// Task 14.8: no duplicate work. An ant whose run this daemon has started
// is not touched by reconciliation, and an ant restarted mid-run does not
// start again.
func TestRestartLeavesRunsOfThisDaemon(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		interrupted, _ := e.leftRunning(12, "sess-12", domain.AntStarting, domain.AntRunning)
		mine := e.mustAccept(13)
		wrap := &fakeWrapUp{}
		e.reconcile(app.RestartDeps{WrapUp: wrap})

		e.wantState(mine, domain.AntRunning)
		e.wantState(interrupted, domain.AntFailed)
		e.wantActive(13)
		if x := e.exec.Executions(); len(x) != 1 {
			t.Errorf("%d runs started, want only gh-13's", len(x))
		}
		if calls := wrap.all(); len(calls) != 1 || calls[0].ant != interrupted {
			t.Errorf("wrap-ups %+v", calls)
		}
	})
}

// queuedAnt stores an ant queued at acceptance for item n, as Accept does
// when capacity is full.
func (e *supEnv) queuedAnt(n int) ports.AntID {
	e.t.Helper()
	ant, err := domain.NewAnt(ghRef(n), domain.AntQueued, time.Hour, domain.DefaultBudgetPolicy(), e.clock.Now(), app.ReasonCapacityFull)
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.store.CreateAnt(e.ctx, ports.AntRecord{Item: ghRef(n), Base: time.Hour, Budget: ant.Budget, History: ant.History(), Runner: string(domain.RunnerClaudeCode)})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// countKind counts the notifications of one kind.
func countKind(n *testkit.Notifier, kind ports.NotificationKind) int {
	c := 0
	for _, k := range n.Kinds() {
		if k == kind {
			c++
		}
	}
	return c
}
