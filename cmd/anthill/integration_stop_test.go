package main

import (
	"context"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// runWithAgentAtWork starts the daemon, accepts the item and waits until
// its ant's first run is active, with an agent that stays at work. It
// returns the ant and the run's execution.
func (in *instance) runWithAgentAtWork(item domain.Ref) (ports.AntRecord, *testkit.FakeExecution) {
	in.t.Helper()
	in.queueAgents(testkit.AgentRun{Hold: true})
	in.start()
	in.polled()
	in.accept(item)
	in.w.eventually("the run", in.antIn(item, domain.AntRunning), in)
	rec, _ := in.ant(item)
	runs := in.agentRuns()
	return rec, runs[len(runs)-1]
}

// wantRuns checks the ant's stored runs: each ended, failed, with the
// given reasons in order.
func (in *instance) wantRuns(id ports.AntID, reasons ...string) {
	in.t.Helper()
	runs, err := in.db().Runs(context.Background(), id)
	if err != nil || len(runs) < len(reasons) {
		in.t.Fatalf("runs = %+v, %v; want at least %d", runs, err, len(reasons))
	}
	for i, reason := range reasons {
		r := runs[i]
		if r.EndedAt.IsZero() || r.Outcome != ports.OutcomeFailed || r.Reason != reason {
			in.t.Errorf("run %d: ended %v, outcome %q, reason %q; want ended failed with %q", r.ID, r.EndedAt, r.Outcome, r.Reason, reason)
		}
	}
}

// Task 11.6: the agent of an active run exits during the daemon's stop,
// after the daemon has let go of its runs and before it closes its store.
// The stop logs no error (the fixture's cleanup fails on any ERROR line)
// and records nothing: the next start finds the run interrupted by the
// restart, and the ant is failed or, with auto-resume, resumed (spec
// ant-execution, "Daemon restarted mid-run").
func TestIntegrationRunEndingAfterTheStopLetGo(t *testing.T) {
	for _, autoResume := range []bool{false, true} {
		name := "auto-resume off"
		settings := "reviewing: false\n"
		if autoResume {
			name = "auto-resume on"
			settings += "auto-resume: true\n"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			in := newInstance(t, w, instanceOptions{login: "mrosani", machine: "laptop", settings: settings})
			optedIn(w, 12, "Fix login timeout")
			item := shopItem(12)
			first, active := in.runWithAgentAtWork(item)

			in.mu.Lock()
			in.beforeClose = func() { active.End(ports.ExecExited, ports.Exit{}) }
			in.mu.Unlock()
			in.stopDaemon()

			// The run after the restart, with auto-resume, stays at work.
			in.queueAgents(testkit.AgentRun{Hold: true})
			in.start()
			if autoResume {
				w.eventually("the resumed run", func() bool {
					rec, _ := in.ant(item)
					return rec.State() == domain.AntRunning && len(in.agentRuns()) == 2
				}, in)
				runs := in.agentRuns()
				if want := "session-" + string(runs[0].ID); runs[1].Spec.Session != want {
					t.Errorf("the run after the restart resumes %q, want %q", runs[1].Spec.Session, want)
				}
			} else {
				w.eventually("the ant failed", in.antIn(item, domain.AntFailed), in)
				rec, _ := in.ant(item)
				if reason := lastReason(rec); reason != app.ReasonInterruptedByRestart {
					t.Errorf("reason = %q, want %q", reason, app.ReasonInterruptedByRestart)
				}
				if got := len(in.agentRuns()); got != 1 {
					t.Errorf("runs = %d, want 1", got)
				}
			}
			in.wantRuns(first.ID, app.ReasonInterruptedByRestart)
			in.claimKept(item)
		})
	}
}

// Task 11.6: the agent of an active run exits before the daemon stops.
// The daemon records the exit without finishing, as always, and the stop
// and the next start leave it so: the ant stays failed with that reason,
// its run ended once.
func TestIntegrationRunEndingBeforeTheStop(t *testing.T) {
	w := newWorld(t)
	in := newInstance(t, w, instanceOptions{login: "mrosani", machine: "laptop", settings: "reviewing: false\nauto-resume: true\n"})
	optedIn(w, 12, "Fix login timeout")
	item := shopItem(12)
	first, active := in.runWithAgentAtWork(item)

	active.End(ports.ExecExited, ports.Exit{})
	w.eventually("the exit recorded", in.antIn(item, domain.AntFailed), in)
	in.wantRuns(first.ID, app.ReasonExitedWithoutFinish)
	in.stopDaemon()

	in.start()
	in.morePolls()
	rec, _ := in.ant(item)
	if rec.State() != domain.AntFailed || lastReason(rec) != app.ReasonExitedWithoutFinish {
		t.Errorf("ant = %s (%s), want failed with %q", rec.State(), lastReason(rec), app.ReasonExitedWithoutFinish)
	}
	in.wantRuns(first.ID, app.ReasonExitedWithoutFinish)
	if got := len(in.agentRuns()); got != 1 {
		t.Errorf("runs = %d, want 1", got)
	}
	in.claimKept(item)
}

// lastReason is the reason of the ant's last transition.
func lastReason(rec ports.AntRecord) string {
	if len(rec.History) == 0 {
		return ""
	}
	return rec.History[len(rec.History)-1].Reason
}
