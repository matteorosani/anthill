package main

import (
	"context"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// pendingComment is the actionable comment the restart tests leave
// pending in the outbox.
const pendingComment = "Please also log the timeout."

// restartWithPendingChange posts pendingComment on the item as a writer,
// lets the next poll detect it, and restarts the daemon while the change
// reactor's classifier call for it is still in progress: "after
// detecting an actionable comment but before acting on it" (spec
// change-reaction, "Restart with a pending change"). The classifier call
// dies with the daemon (stopDaemon).
func (in *instance) restartWithPendingChange(item domain.Ref) {
	in.t.Helper()
	in.setReply(nil)
	w := in.w
	before := len(in.classifierCalls())
	w.gh.Comment(item, in.opts.login, pendingComment)
	w.advanceUntil("the classifier call", testPollInterval, func() bool { return len(in.classifierCalls()) > before }, in)
	if !in.commentPending() {
		in.t.Fatal("the comment is not pending in the outbox before the restart")
	}
	in.stopDaemon()
	in.setReply(func(ports.ExecSpec) (string, bool) { return "actionable", true })
	in.start()
	// The restarted daemon handles the change: it classifies it again.
	w.eventually("the classifier call after the restart", func() bool {
		for _, x := range in.executor().Executions() {
			if x.Spec.Run.ToolToken == "" && strings.Contains(x.Spec.Run.Brief, pendingComment) {
				return true
			}
		}
		return false
	}, in)
}

// commentPending reports whether the change event of pendingComment
// waits in the outbox.
func (in *instance) commentPending() bool {
	pending, err := in.db().Pending(context.Background())
	if err != nil {
		in.t.Fatal(err)
	}
	for _, e := range pending {
		if e.Event.Body == pendingComment {
			return true
		}
	}
	return false
}

// changeSeen counts the agent inputs that carry pendingComment: the
// briefs of every run and the messages given to active runs.
func (in *instance) changeSeen() int {
	n := 0
	for _, x := range in.agentRuns() {
		n += strings.Count(x.Spec.Run.Brief, pendingComment)
		for _, input := range x.Inputs() {
			n += strings.Count(input, pendingComment)
		}
	}
	return n
}

// outboxEmpty reports whether the instance has no change event left to
// handle.
func (in *instance) outboxEmpty() bool {
	pending, err := in.db().Pending(context.Background())
	return err == nil && len(pending) == 0
}

// claimKept checks that the instance still holds the item's task claim,
// with its claim comment not released (spec ant-execution, "Daemon
// restarted mid-run": its claim is kept).
func (in *instance) claimKept(item domain.Ref) {
	in.t.Helper()
	held, err := in.db().HeldClaims(context.Background())
	if err != nil || len(held) != 1 || held[0].Ref != item || held[0].Kind != domain.ClaimTask {
		in.t.Errorf("held claims = %+v, %v; want the task claim on %s", held, err, item)
	}
	if got, _ := in.w.gh.Item(item); !domain.HasLabel(got.Labels, domain.DefaultClaimLabel) {
		in.t.Errorf("labels = %v, want the claim label kept", got.Labels)
	}
	for _, c := range markedComments(in.w, item, domain.CommentClaim) {
		if m, _ := domain.FindMarker(c.Body); m.Released {
			in.t.Errorf("claim comment released: %s", c.Body)
		}
	}
}

// morePolls lets three more polls run, so that a change handled twice
// would show. The clock moves a poll interval at a time until each poll
// has happened.
func (in *instance) morePolls() {
	in.t.Helper()
	for range 3 {
		from := in.w.clock.Now()
		in.w.advanceUntil("a poll", testPollInterval, func() bool {
			st, err := in.db().PollStatus(context.Background(), domain.ConnectionGitHub)
			return err == nil && st.LastSuccess.After(from)
		}, in)
	}
}

// Task 19.3: the daemon restarts while an ant has an active run and a
// change for it is pending. The ant is failed, or resumed when the
// repository sets auto-resume, its claim is kept, and the change is
// handled exactly once (spec ant-execution, "Daemon restarted mid-run";
// change-reaction, "Restart with a pending change").
func TestIntegrationRestartDuringARunWithAPendingChange(t *testing.T) {
	for _, autoResume := range []bool{false, true} {
		name := "auto-resume off"
		if autoResume {
			name = "auto-resume on"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			settings := ""
			if autoResume {
				settings = "auto-resume: true\n"
			}
			in := newInstance(t, w, instanceOptions{login: "mrosani", machine: "laptop", settings: settings})
			optedIn(w, 12, "Fix login timeout")
			item := shopItem(12)
			// The first run stays active. So does the run after the
			// restart, which has no queued agent.
			in.queueAgents(testkit.AgentRun{Hold: true})
			in.start()
			in.polled()
			in.accept(item)
			w.eventually("the run", in.antIn(item, domain.AntRunning), in)
			first, _ := in.ant(item)

			in.restartWithPendingChange(item)

			if !autoResume {
				// Interrupted by restart: failed, its claim kept, and the
				// change stored for its next run, which only the queen
				// starts.
				w.eventually("the change stored", func() bool {
					stored, err := in.db().StoredChanges(context.Background(), first.ID)
					return err == nil && len(stored) == 1 && in.outboxEmpty()
				}, in)
				rec, _ := in.ant(item)
				if rec.State() != domain.AntFailed || rec.ID != first.ID {
					t.Fatalf("ant = %d %s, want ant %d failed", rec.ID, rec.State(), first.ID)
				}
				if h := rec.History[len(rec.History)-1]; h.Reason != "interrupted by restart" {
					t.Errorf("reason = %q", h.Reason)
				}
				in.claimKept(item)
				in.morePolls()
				if got := len(in.agentRuns()); got != 1 {
					t.Errorf("runs before the retry = %d, want 1", got)
				}
				in.mustRun("retry", "#12")
			}

			// The ant has a run again, which resumes the session and
			// carries the change exactly once.
			w.eventually("the resumed run with the change", func() bool {
				return len(in.agentRuns()) == 2 && in.changeSeen() == 1 && in.outboxEmpty()
			}, in)
			in.morePolls()
			runs := in.agentRuns()
			if len(runs) != 2 || in.changeSeen() != 1 {
				t.Errorf("runs = %d, change seen %d times; want 2 runs and the change once", len(runs), in.changeSeen())
			}
			if want := "session-" + string(runs[0].ID); runs[1].Spec.Session != want {
				t.Errorf("the run after the restart resumes %q, want %q", runs[1].Spec.Session, want)
			}
			if rec, _ := in.ant(item); rec.ID != first.ID || rec.State() != domain.AntRunning {
				t.Errorf("ant = %d %s, want ant %d running", rec.ID, rec.State(), first.ID)
			}
			in.claimKept(item)
		})
	}
}

// Task 19.3, between runs: the daemon restarts while a change is pending
// for an ant whose PR is open. After the restart the change starts
// exactly one follow-up run.
func TestIntegrationRestartWithAPendingChangeBetweenRuns(t *testing.T) {
	w := newWorld(t)
	// No shepherd rounds: only the change starts a run.
	in := newInstance(t, w, instanceOptions{login: "mrosani", machine: "laptop", settings: "reviewing: false\n"})
	optedIn(w, 12, "Fix login timeout")
	item := shopItem(12)
	in.queueAgents(
		committing(in.git, []string{"fix: apply a 60 s login timeout"},
			tool(domain.ToolPush, map[string]any{}),
			tool(domain.ToolOpenPullRequest, map[string]any{"title": "Fix login timeout", "body": "Applies a 60 s timeout."}),
			tool(domain.ToolFinish, map[string]any{"status": "complete", "summary": "Login times out after 60 s."}),
		),
	)
	in.start()
	in.polled()
	in.accept(item)
	w.eventually("the ant PR", in.antIn(item, domain.AntSucceeded), in)
	first, _ := in.ant(item)

	// The follow-up run has no queued agent, so it stays active.
	in.restartWithPendingChange(item)

	w.advanceUntil("the follow-up run", testPollInterval, func() bool {
		return len(in.agentRuns()) == 2 && in.outboxEmpty()
	}, in)
	in.morePolls()
	runs := in.agentRuns()
	if len(runs) != 2 || in.changeSeen() != 1 {
		t.Fatalf("runs = %d, change seen %d times; want 2 runs and the change once", len(runs), in.changeSeen())
	}
	rec, _ := in.ant(item)
	if rec.ID != first.ID || rec.State() != domain.AntRunning {
		t.Errorf("ant = %d %s, want ant %d running", rec.ID, rec.State(), first.ID)
	}
	stored, err := in.db().Runs(context.Background(), rec.ID)
	if err != nil || len(stored) != 2 || stored[1].Kind != ports.RunFollowUp {
		t.Errorf("runs = %+v, %v; want the task run and one follow-up run", stored, err)
	}
	if left, _ := in.db().StoredChanges(context.Background(), rec.ID); len(left) != 0 {
		t.Errorf("stored changes left = %+v", left)
	}
	in.claimKept(item)
}
