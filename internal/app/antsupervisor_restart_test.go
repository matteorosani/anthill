package app_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
	"github.com/matteorosani/anthill/internal/testkit/storetest"
)

// restartWrapUps returns the wrap-up comments of a restart on item n.
func (e *supEnv) restartWrapUps(n int) []domain.Comment {
	e.t.Helper()
	item, _ := e.gh.Item(ghRef(n))
	var out []domain.Comment
	for _, c := range item.Comments {
		if strings.Contains(c.Body, app.ReasonInterruptedByRestart) && strings.Contains(c.Body, app.SummaryInterruptedByRestart) {
			out = append(out, c)
		}
	}
	return out
}

// parked reports whether this instance holds item n's task claim parked.
func (e *supEnv) parked(n int) bool {
	e.t.Helper()
	held, err := e.store.HeldClaims(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return slices.ContainsFunc(held, func(h ports.HeldClaim) bool { return h.Ref == ghRef(n) && h.Parked })
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
	rec.Worktree = fmt.Sprintf("/home/me/.anthill/worktrees/gh-%d-fix", n)
	if err := e.git.Fetch(e.ctx, e.clone); err != nil {
		e.t.Fatal(err)
	}
	if err := e.git.CreateBranch(e.ctx, e.clone, rec.Branch, "main"); err != nil {
		e.t.Fatal(err)
	}
	if err := e.git.AddWorktree(e.ctx, e.clone, rec.Worktree, rec.Branch, domain.NewTrailers(ghRef(n), domain.RunnerClaudeCode, instanceA)); err != nil {
		e.t.Fatal(err)
	}
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

func (e *supEnv) reconcile() {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.sup.Reconcile(e.ctx) }()
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
				e.git.Commit(fmt.Sprintf("/home/me/.anthill/worktrees/gh-%d-fix", 12), "half done")
				e.reconcile()

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
				// The wrap-up of an exit without finishing: the new commit
				// is pushed, the summary is on the ticket, and the claim
				// is parked.
				if c := e.restartWrapUps(12); len(c) != 1 {
					t.Errorf("restart wrap-up comments %+v", c)
				}
				if got := e.git.RemoteCommits(supClone, rec.Branch); len(got) == 0 || got[len(got)-1].Subject != "half done" {
					t.Errorf("remote %s = %+v, want the new commit pushed", rec.Branch, got)
				}
				if !e.parked(12) {
					t.Error("the claim was not parked")
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
				e.reconcile()
				if len(e.restartWrapUps(12)) != 1 || len(e.ends.all()) != 1 || len(e.ant(id).History) != len(rec.History) {
					t.Error("a second reconciliation handled the ant again")
				}
			})
		})
	}
}

// Task 14.8, scenario "Daemon restarted mid-run", with auto-resume: the
// ant is resumed automatically through the queen's retry, which resumes
// its session in the same worktree. It gets no wrap-up, and its claim is
// kept and renewed.
func TestDaemonRestartedMidRunAutoResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 2)
		e.settings.AutoResume = true
		id, _ := e.leftRunning(12, "sess-12", domain.AntStarting, domain.AntRunning)
		e.reconcile()

		rec := e.wantState(id, domain.AntRunning)
		if !slices.ContainsFunc(rec.History, func(tr domain.Transition) bool {
			return tr.To == domain.AntFailed && tr.Reason == app.ReasonInterruptedByRestart
		}) {
			t.Errorf("history %+v, want failed with %q before the retry", rec.History, app.ReasonInterruptedByRestart)
		}
		if ends := e.ends.all(); len(ends) != 1 || ends[0].run.Reason != app.ReasonInterruptedByRestart || ends[0].reported != (ports.Usage{}) {
			t.Errorf("run ends %+v", ends)
		}
		x := e.execOf(ghRef(12))
		if x.Spec.Session != "sess-12" || x.Spec.Run.Worktree != rec.Worktree {
			t.Errorf("resumed run spec %+v, want session sess-12 in %s", x.Spec, rec.Worktree)
		}
		if runs := e.runs(id); len(runs) != 2 {
			t.Errorf("runs %+v, want the interrupted one and the resumed one", runs)
		}
		if c := e.restartWrapUps(12); len(c) != 0 || e.parked(12) || !e.claimed(12) {
			t.Errorf("wrap-up comments %+v, parked %v, claimed %v", c, e.parked(12), e.claimed(12))
		}

		// A second reconciliation leaves the resumed run alone.
		e.reconcile()
		e.wantState(id, domain.AntRunning)
		if n := len(e.exec.Executions()); n != 1 {
			t.Errorf("%d runs started", n)
		}
	})
}

// Task 14.8: a finishing ant whose finish is stored is not interrupted:
// the playbook carries out the finish, here a task run's no-change
// finish, which makes the ant done.
func TestRestartCarriesOutAStoredFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id, _ := e.leftRunning(12, "sess-12", domain.AntStarting, domain.AntRunning, domain.AntFinishing)
		rec := e.ant(id)
		rec.Finish = &domain.AntFinish{Status: domain.FinishNoChange, Summary: "Already fixed by #11."}
		if err := e.store.SaveAnt(e.ctx, rec); err != nil {
			t.Fatal(err)
		}
		e.reconcile()
		rec = e.wantState(id, domain.AntDone)
		if lastReason(rec) == app.ReasonInterruptedByRestart || len(e.restartWrapUps(12)) != 0 {
			t.Errorf("the finishing ant was interrupted: %+v", rec.History)
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

		e.reconcile()
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
		e.reconcile()

		e.wantState(mine, domain.AntRunning)
		e.wantState(interrupted, domain.AntFailed)
		e.wantActive(13)
		if x := e.exec.Executions(); len(x) != 1 {
			t.Errorf("%d runs started, want only gh-13's", len(x))
		}
		if len(e.restartWrapUps(12)) != 1 || len(e.restartWrapUps(13)) != 0 {
			t.Errorf("restart wrap-ups: gh-12 %d, gh-13 %d", len(e.restartWrapUps(12)), len(e.restartWrapUps(13)))
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
