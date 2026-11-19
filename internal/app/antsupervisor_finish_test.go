package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// callTool makes the agent of the item's last run call an ant tool, and
// lets everything it starts run: a finish ends the run and is carried
// out once its process has ended.
func (e *supEnv) callTool(item domain.Ref, tool, args string) app.ToolResult {
	e.t.Helper()
	token := e.execOf(item).Spec.Run.ToolToken
	done := make(chan app.ToolResult, 1)
	go func() { done <- e.tools.Call(e.ctx, token, tool, json.RawMessage(args)) }()
	e.settle()
	res := <-done
	e.settle()
	return res
}

func (e *supEnv) mustCallTool(item domain.Ref, tool, args string) {
	e.t.Helper()
	if res := e.callTool(item, tool, args); res.IsError {
		e.t.Fatalf("%s refused: %s", tool, res.Text)
	}
}

// commit makes a commit in the ant's worktree, as its agent would.
func (e *supEnv) commit(id ports.AntID, subject string) {
	e.git.Commit(e.ant(id).Worktree, subject)
}

// antPRs lists the open PRs from the ant's branch.
func (e *supEnv) antPRs(id ports.AntID) []domain.PullRequest {
	e.t.Helper()
	page, err := e.gh.Client("mrosani").PullRequests(e.ctx, shop, "")
	if err != nil {
		e.t.Fatal(err)
	}
	branch := e.ant(id).Branch
	var prs []domain.PullRequest
	for _, pr := range page.PullRequests {
		if pr.SourceBranch == branch {
			prs = append(prs, pr)
		}
	}
	return prs
}

func (e *supEnv) onePR(id ports.AntID) domain.PullRequest {
	e.t.Helper()
	prs := e.antPRs(id)
	if len(prs) != 1 {
		e.t.Fatalf("ant %d has %d PRs, want 1", id, len(prs))
	}
	return prs[0]
}

// heldClaim is this instance's task claim on item n.
func (e *supEnv) heldClaim(n int) (ports.HeldClaim, bool) {
	e.t.Helper()
	held, err := e.store.HeldClaims(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, h := range held {
		if h.Ref == ghRef(n) && h.Kind == domain.ClaimTask {
			return h, true
		}
	}
	return ports.HeldClaim{}, false
}

func (e *supEnv) wantParked(n int) {
	e.t.Helper()
	if h, ok := e.heldClaim(n); !ok || !h.Parked {
		e.t.Errorf("claim on gh-%d = %+v (held %v), want parked", n, h, ok)
	}
}

// summaries are the Anthill summary comments on item n.
func (e *supEnv) summaries(n int) []string {
	e.t.Helper()
	item, ok := e.gh.Item(ghRef(n))
	if !ok {
		e.t.Fatalf("no item %d", n)
	}
	var out []string
	for _, c := range item.Comments {
		if m, ok := domain.FindMarker(c.Body); ok && m.Kind == domain.CommentSummary {
			out = append(out, c.Body)
		}
	}
	return out
}

// pushed are the commits of the ant branch on the remote after the base.
func (e *supEnv) pushed(rec ports.AntRecord) []ports.Commit {
	return slices.DeleteFunc(e.git.RemoteCommits(supClone, rec.Branch), func(c ports.Commit) bool {
		return slices.Contains(e.git.RemoteCommits(supClone, rec.BaseBranch), c)
	})
}

func (e *supEnv) lastRun(id ports.AntID) ports.RunRecord {
	e.t.Helper()
	runs := e.runs(id)
	if len(runs) == 0 {
		e.t.Fatalf("ant %d has no run", id)
	}
	return runs[len(runs)-1]
}

func (e *supEnv) wantNoErrors() {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, err := range e.errs {
		e.t.Errorf("background error: %v", err)
	}
	e.errs = nil
}

func finishArgs(status domain.FinishStatus, summary string, points ...string) string {
	b, _ := json.Marshal(map[string]any{"status": status, "summary": summary, "open_points": points})
	return string(b)
}

// followUp gives a succeeded ant a stored change, so that its follow-up
// run starts (auto-follow-up is on by default).
func (e *supEnv) followUp(id ports.AntID, body string) {
	e.t.Helper()
	if err := e.store.StoreChange(e.ctx, ports.StoredChange{
		Ant: id, Source: ports.OutboxID(len(e.runs(id)) + 100),
		Event: domain.ChangeEvent{Kind: domain.ChangeCommentAdded, Ref: *e.ant(id).PR, Body: body, Actor: "lbianchi"},
	}); err != nil {
		e.t.Fatal(err)
	}
	e.trigger(id)
	e.wantState(id, domain.AntRunning)
	if k := e.lastRun(id).Kind; k != ports.RunFollowUp {
		e.t.Fatalf("run kind = %s, want follow-up", k)
	}
}

func TestSuccessfulFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Fix the login timeout")
		e.mustCallTool(ghRef(12), "push", `{}`)
		e.mustCallTool(ghRef(12), "open_pull_request", `{"title":"Fix the login timeout","body":"Raises the timeout.","draft":false}`)
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Done."))

		rec := e.wantState(id, domain.AntSucceeded)
		pr := e.onePR(id)
		if pr.Title != "Fix the login timeout" || !strings.Contains(pr.Body, "Raises the timeout.") || !strings.Contains(pr.Body, "Closes #12") {
			t.Errorf("PR = %+v", pr)
		}
		if pr.SourceBranch != rec.Branch || pr.TargetBranch != "main" || !strings.HasPrefix(pr.SourceBranch, "ant/gh-12-") {
			t.Errorf("PR branches = %s -> %s", pr.SourceBranch, pr.TargetBranch)
		}
		// Complete without local verification: Anthill runs nothing, and
		// the PR stays ready for review.
		if pr.Draft {
			t.Error("the ready PR became a draft")
		}
		if rec.PR == nil || *rec.PR != pr.Ref || rec.Finish != nil {
			t.Errorf("record PR = %v, finish = %+v", rec.PR, rec.Finish)
		}
		e.wantParked(12)
		if run := e.lastRun(id); run.Outcome != ports.OutcomeComplete || run.Reason != "finish: complete" || run.EndedAt.IsZero() {
			t.Errorf("run = %+v", run)
		}
		if r := lastReason(rec); r != "finish: complete" {
			t.Errorf("reason = %q", r)
		}
		if got := e.notifier.Kinds(); !slices.Equal(got, []ports.NotificationKind{domain.NotifySucceeded}) {
			t.Errorf("notifications = %v", got)
		}
		e.wantActive()
		e.wantNoErrors()
	})
}

func TestFinishWithoutOpeningAPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Fix the login timeout")
		e.commit(id, "Add a test")
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Raised the timeout to 60 s."))

		rec := e.wantState(id, domain.AntSucceeded)
		if got := e.pushed(rec); len(got) != 2 {
			t.Errorf("pushed commits = %v, want 2", got)
		}
		pr := e.onePR(id)
		if pr.Title != "Fix bug 12" {
			t.Errorf("title = %q, want the ticket's", pr.Title)
		}
		if pr.Body != "Raised the timeout to 60 s.\n\nCloses #12" {
			t.Errorf("body = %q", pr.Body)
		}
		if pr.Draft || !slices.Contains(pr.Labels, e.settings.OptInLabel) {
			t.Errorf("PR = %+v", pr)
		}
		if rec.PR == nil || *rec.PR != pr.Ref {
			t.Errorf("record PR = %v", rec.PR)
		}
		e.wantParked(12)

		// Triggering again finds nothing to do: no second PR.
		e.trigger(id)
		e.onePR(id)
		e.wantNoErrors()
	})
}

func TestPartialCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Half of the fix")
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishPartial, "Fixed Safari.", "Firefox still times out", "No test yet"))

		rec := e.wantState(id, domain.AntSucceeded)
		pr := e.onePR(id)
		if !pr.Draft {
			t.Error("the PR is not a draft")
		}
		for _, p := range []string{"Fixed Safari.", "- Firefox still times out", "- No test yet", "Closes #12"} {
			if !strings.Contains(pr.Body, p) {
				t.Errorf("body %q lacks %q", pr.Body, p)
			}
		}
		if run := e.lastRun(id); run.Outcome != ports.OutcomePartial {
			t.Errorf("outcome = %s", run.Outcome)
		}
		if r := lastReason(rec); r != "finish: partial" {
			t.Errorf("reason = %q", r)
		}
		e.wantParked(12)
		e.wantNoErrors()
	})
}

func TestPartialCompletionForcesTheAgentsReadyPRToADraft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Half of the fix")
		e.mustCallTool(ghRef(12), "push", `{}`)
		e.mustCallTool(ghRef(12), "open_pull_request", `{"title":"Fix login","body":"Work in progress.","draft":false}`)
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishPartial, "Half done.", "Firefox", "Tests"))

		e.wantState(id, domain.AntSucceeded)
		pr := e.onePR(id)
		if !pr.Draft || !strings.HasPrefix(pr.Body, "Work in progress.") || !strings.Contains(pr.Body, "- Firefox\n- Tests") {
			t.Errorf("PR = draft %v, body %q", pr.Draft, pr.Body)
		}
		e.wantNoErrors()
	})
}

func TestDraftCompletedByALaterRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Half of the fix")
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishPartial, "Half done.", "Firefox"))
		e.wantState(id, domain.AntSucceeded)
		if !e.onePR(id).Draft {
			t.Fatal("the partial PR is not a draft")
		}

		e.followUp(id, "Please finish Firefox too.")
		e.commit(id, "Fix Firefox")
		e.mustCallTool(ghRef(12), "push", `{}`)
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Firefox fixed."))

		rec := e.wantState(id, domain.AntSucceeded)
		pr := e.onePR(id)
		if pr.Draft {
			t.Error("the forced draft was not marked ready")
		}
		if got := e.pushed(rec); len(got) != 2 {
			t.Errorf("pushed commits = %v, want both on the same PR", got)
		}
		if *rec.PR != pr.Ref {
			t.Errorf("record PR = %v, want %v", rec.PR, pr.Ref)
		}
		e.wantParked(12)
		e.wantNoErrors()
	})
}

func TestReadyAfterCompleteLeavesTheAgentsOwnDraft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Fix")
		e.mustCallTool(ghRef(12), "push", `{}`)
		e.mustCallTool(ghRef(12), "open_pull_request", `{"title":"Fix","body":"Body.","draft":true}`)
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Done."))

		// The agent chose a draft, and nothing forced it: it is honoured.
		e.wantState(id, domain.AntSucceeded)
		if !e.onePR(id).Draft {
			t.Error("the agent's draft was marked ready")
		}
		e.wantNoErrors()
	})
}

func TestBugAlreadyFixed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishNoChange, "The timeout was raised in abc123 on main."))

		rec := e.wantState(id, domain.AntDone)
		if prs := e.antPRs(id); len(prs) != 0 {
			t.Errorf("PRs = %+v, want none", prs)
		}
		got := e.summaries(12)
		if len(got) != 1 || !strings.Contains(got[0], "The timeout was raised in abc123 on main.") {
			t.Errorf("summary comments = %q", got)
		}
		item, _ := e.gh.Item(ghRef(12))
		if !domain.HasLabel(item.Labels, e.settings.HoldLabel) {
			t.Errorf("labels = %v, want %s", item.Labels, e.settings.HoldLabel)
		}
		if e.claimed(12) || domain.HasLabel(item.Labels, domain.DefaultClaimLabel) {
			t.Errorf("the claim was not released: labels %v", item.Labels)
		}
		if run := e.lastRun(id); run.Outcome != ports.OutcomeNoChange {
			t.Errorf("outcome = %s", run.Outcome)
		}
		if r := lastReason(rec); r != "finish: no-change" || rec.Finish != nil {
			t.Errorf("reason = %q, finish = %+v", r, rec.Finish)
		}
		if got := e.notifier.Kinds(); !slices.Equal(got, []ports.NotificationKind{domain.NotifyNoChange}) {
			t.Errorf("notifications = %v", got)
		}
		e.wantNoErrors()
	})
}

func TestFollowUpNeedingNoChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Fix")
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Done."))
		before := e.onePR(id)
		pushed := e.git.RemoteCommits(supClone, e.ant(id).Branch)

		e.followUp(id, "Why 60 seconds?")
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishNoChange, "Replied on the thread."))

		rec := e.wantState(id, domain.AntSucceeded)
		if got := e.git.RemoteCommits(supClone, rec.Branch); !slices.Equal(got, pushed) {
			t.Errorf("remote commits = %v, want %v", got, pushed)
		}
		if after := e.onePR(id); after.Draft != before.Draft || after.Body != before.Body || after.Title != before.Title {
			t.Errorf("PR changed: %+v -> %+v", before, after)
		}
		item, _ := e.gh.Item(ghRef(12))
		if domain.HasLabel(item.Labels, e.settings.HoldLabel) {
			t.Error("the hold label was added")
		}
		e.wantParked(12)
		if run := e.lastRun(id); run.Outcome != ports.OutcomeNoChange || run.Kind != ports.RunFollowUp {
			t.Errorf("run = %+v", run)
		}
		e.wantNoErrors()
	})
}

func TestBackgroundCommandAfterFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		x := e.execOf(ghRef(12))
		x.Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "session-12"})
		e.settle()
		e.commit(id, "Fix")

		// The agent's background command keeps the process alive: only
		// Anthill's stop ends it.
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Done."))

		if stops := x.Stops(); !slices.Equal(stops, []time.Duration{app.StopGrace}) {
			t.Errorf("stops = %v, want one with the stop grace", stops)
		}
		if !x.Ended() {
			t.Error("the process tree is still running")
		}
		run := e.lastRun(id)
		if run.EndedAt.IsZero() || run.SessionID != "session-12" || run.Outcome != ports.OutcomeComplete {
			t.Errorf("run = %+v", run)
		}
		if rec := e.wantState(id, domain.AntSucceeded); rec.SessionID != "session-12" {
			t.Errorf("session = %q", rec.SessionID)
		}
		e.wantActive()
		e.wantNoErrors()
	})
}

// A finish stored with the move to finishing is carried out however the
// process ends: by itself before the supervisor heard of the finish, or
// at the deadline during the stop, which must not make the ant timed-out.
func TestFinishCarriedOutHoweverTheProcessEnds(t *testing.T) {
	for _, kind := range []ports.ExecEventKind{ports.ExecExited, ports.ExecEndedAtDeadline} {
		t.Run(string(kind), func(t *testing.T) { testFinishWhenTheProcessEnds(t, kind) })
	}
}

func testFinishWhenTheProcessEnds(t *testing.T, kind ports.ExecEventKind) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Fix")
		rec := e.ant(id)
		// The finish tool stored the finish, then the agent exited before
		// the supervisor heard of it, as after a crash of the daemon's
		// tool call: the finish is still carried out.
		ant, err := domain.RestoreAnt(rec.Item, rec.Base, rec.Budget, rec.History)
		if err == nil {
			err = ant.Transition(domain.AntFinishing, e.clock.Now(), "finish: complete")
		}
		if err != nil {
			t.Fatal(err)
		}
		rec.History, rec.Budget = ant.History(), ant.Budget
		rec.Finish = &domain.AntFinish{Status: domain.FinishComplete, Summary: "Done.", NewCommits: 1}
		if err := e.store.SaveAnt(e.ctx, rec); err != nil {
			t.Fatal(err)
		}
		if kind == ports.ExecEndedAtDeadline {
			// The finish was handed over, and the deadline came during
			// the stop.
			e.sup.EndingRun(ghRef(12), ports.OutcomeComplete, "finish: complete")
		}
		e.end(ghRef(12), kind)

		e.wantState(id, domain.AntSucceeded)
		e.onePR(id)
		if run := e.lastRun(id); run.Outcome != ports.OutcomeComplete || run.Reason != "finish: complete" {
			t.Errorf("run = %+v", run)
		}
		e.wantNoErrors()
	})
}

func TestCommitsButNoFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "First step")
		e.mustCallTool(ghRef(12), "report_progress", `{"text":"Running the tests."}`)
		e.commit(id, "Second step")
		// Task 14.12: the agent's last message, as the executor reports
		// it, goes in the failure summary in place of the progress report.
		e.execOf(ghRef(12)).EmitLastMessage("Both steps are committed; the Safari test still fails.")
		e.end(ghRef(12), ports.ExecExited)

		rec := e.wantState(id, domain.AntFailed)
		if r := lastReason(rec); r != app.ReasonExitedWithoutFinish {
			t.Errorf("reason = %q", r)
		}
		if got := e.pushed(rec); len(got) != 2 {
			t.Errorf("pushed commits = %v, want 2", got)
		}
		if prs := e.antPRs(id); len(prs) != 0 {
			t.Errorf("PRs = %+v, want none", prs)
		}
		got := e.summaries(12)
		if len(got) != 1 || !strings.Contains(got[0], app.ReasonExitedWithoutFinish) || !strings.Contains(got[0], "pushed") ||
			!strings.Contains(got[0], "Both steps are committed; the Safari test still fails.") || strings.Contains(got[0], "Running the tests.") {
			t.Errorf("summary comments = %q, want the agent's last message", got)
		}
		if got := e.git.Worktrees(supClone); !slices.Equal(got, []string{rec.Worktree}) {
			t.Errorf("worktrees = %v, want it kept", got)
		}
		e.wantParked(12)
		if run := e.lastRun(id); run.Outcome != ports.OutcomeFailed || run.Reason != app.ReasonExitedWithoutFinish {
			t.Errorf("run = %+v", run)
		}
		if got := e.notifier.Kinds(); !slices.Equal(got, []ports.NotificationKind{domain.NotifyFailed}) {
			t.Errorf("notifications = %v", got)
		}
		e.wantNoErrors()
	})
}

// Task 14.12: when the agent gave no message, the failure summary has
// the run's last progress report.
func TestExitWithoutFinishFallsBackToProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "First step")
		e.mustCallTool(ghRef(12), "report_progress", `{"text":"Running the tests."}`)
		e.end(ghRef(12), ports.ExecExited)

		e.wantState(id, domain.AntFailed)
		if got := e.summaries(12); len(got) != 1 || !strings.Contains(got[0], "Running the tests.") {
			t.Errorf("summary comments = %q, want the last progress report", got)
		}
		e.wantNoErrors()
	})
}

// Task 14.12 (spec agent-conversation, "Comment content safety"): the
// agent's last message is posted with its secrets redacted, its paths in
// the worktree made relative, and cut to the summary limit with a note.
func TestLastMessageIsMadeSafe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		rec, err := e.store.Ant(e.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		secret := "ghp_" + strings.Repeat("a", 36)
		long := "Stuck: " + secret + " fails in " + filepath.Join(rec.Worktree, "auth", "login.go") + ". " +
			strings.Repeat("x", domain.DefaultSummaryLimit)
		x := e.execOf(ghRef(12))
		x.EmitLastMessage("An earlier message.")
		x.EmitLastMessage(long)
		e.end(ghRef(12), ports.ExecExited)

		e.wantState(id, domain.AntFailed)
		got := e.summaries(12)
		if len(got) != 1 {
			t.Fatalf("summary comments = %q", got)
		}
		body := got[0]
		if strings.Contains(body, secret) || !strings.Contains(body, domain.Redacted) {
			t.Errorf("the summary does not redact the secret: %q", body)
		}
		if strings.Contains(body, rec.Worktree) {
			t.Errorf("the summary has the worktree's absolute path: %q", body)
		}
		if strings.Contains(body, "An earlier message.") {
			t.Error("the summary has an earlier message, not the last")
		}
		if !strings.Contains(body, "(truncated, ") || strings.Contains(body, strings.Repeat("x", domain.DefaultSummaryLimit)) {
			t.Errorf("the summary is not cut to the limit with a note")
		}
		e.wantNoErrors()
	})
}

// The claim of a failed ant is parked even when its wrap-up cannot reach
// the code host (spec work-claiming, "parked").
func TestFailedAntParkedWithoutCodeHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var broken atomic.Bool
		e := newSupEnv(t, 2, func(d *app.AntSupervisorDeps) {
			route := d.CodeHost
			d.CodeHost = func(ctx context.Context, repo domain.Repository) (ports.CodeHostPort, error) {
				if broken.Load() {
					return nil, errors.New("code host unreachable")
				}
				return route(ctx, repo)
			}
		})
		id := e.mustAccept(12)
		broken.Store(true)
		e.execOf(ghRef(12)).End(ports.ExecExited, ports.Exit{Code: 1, Crashed: true})
		e.settle()

		e.wantState(id, domain.AntFailed)
		e.wantParked(12)
		e.mu.Lock()
		errs := e.errs
		e.errs = nil
		e.mu.Unlock()
		if len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), "code host unreachable") {
			t.Errorf("errors = %v, want the code host's", errs)
		}
	})
}

func TestCrashBeforeAnyCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.execOf(ghRef(12)).End(ports.ExecExited, ports.Exit{Code: 137, Crashed: true})
		e.settle()

		rec := e.wantState(id, domain.AntFailed)
		if got := e.git.RemoteBranches(supClone); slices.Contains(got, rec.Branch) {
			t.Errorf("remote branches = %v: the branch was pushed", got)
		}
		got := e.summaries(12)
		if len(got) != 1 || !strings.Contains(got[0], "Nothing was pushed") {
			t.Errorf("summary comments = %q", got)
		}
		e.wantParked(12)
		e.wantNoErrors()
	})
}

func TestFollowUpRunExitsWithoutFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "Fix")
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Done."))
		if e.onePR(id).Draft {
			t.Fatal("the PR is a draft before the follow-up")
		}

		e.followUp(id, "Please rename the flag.")
		e.commit(id, "Rename the flag")
		e.execOf(ghRef(12)).End(ports.ExecExited, ports.Exit{Code: 1, Crashed: true})
		e.settle()

		rec := e.wantState(id, domain.AntFailed)
		if got := e.pushed(rec); len(got) != 2 {
			t.Errorf("pushed commits = %v, want the follow-up's too", got)
		}
		pr := e.onePR(id)
		if !pr.Draft {
			t.Error("the PR was not made a draft")
		}
		got := e.summaries(12)
		if len(got) != 1 || !strings.Contains(got[0], pr.URL) {
			t.Errorf("summary comments = %q, want one linking %s", got, pr.URL)
		}
		e.wantNoErrors()
	})
}

// Every finish decision's effect is recorded before the next is asked:
// a trigger after the finish carries nothing out again.
func TestFinishDecisionsAreNotRepeated(t *testing.T) {
	for _, status := range []domain.FinishStatus{domain.FinishComplete, domain.FinishPartial, domain.FinishNoChange} {
		t.Run(string(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newSupEnv(t, 2)
				id := e.mustAccept(12)
				if status != domain.FinishNoChange {
					e.commit(id, "Fix")
				}
				e.mustCallTool(ghRef(12), "finish", finishArgs(status, "Summary.", "Point"))
				before := len(e.gh.Requests())
				e.trigger(id)
				e.trigger(id)
				if after := e.gh.Requests(); len(after) != before {
					t.Errorf("triggers after the finish made provider calls: %v", after[before:])
				}
				e.wantNoErrors()
			})
		})
	}
}

// The end of the budget is wrapped up as an ending without finish too,
// with a draft PR holding the progress summary when the branch has new
// commits (spec ant-timebox, "Time runs out").
func TestTimeRunsOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "WIP")
		e.mustCallTool(ghRef(12), "report_progress", `{"text":"Safari done, Firefox next."}`)
		e.end(ghRef(12), ports.ExecEndedAtDeadline)

		rec := e.wantState(id, domain.AntTimedOut)
		if got := e.pushed(rec); len(got) != 1 {
			t.Errorf("pushed commits = %v", got)
		}
		pr := e.onePR(id)
		if !pr.Draft || !strings.Contains(pr.Body, "Safari done, Firefox next.") || !strings.Contains(pr.Body, "Closes #12") || pr.Title != "Fix bug 12" {
			t.Errorf("PR = %+v", pr)
		}
		if rec.PR == nil || *rec.PR != pr.Ref {
			t.Errorf("record PR = %v", rec.PR)
		}
		got := e.summaries(12)
		if len(got) != 1 || !strings.Contains(got[0], pr.URL) || !strings.Contains(got[0], app.ReasonBudgetUsedUp) {
			t.Errorf("summary comments = %q", got)
		}
		e.wantParked(12)
		if got := e.notifier.Kinds(); !slices.Equal(got, []ports.NotificationKind{domain.NotifyTimedOut}) {
			t.Errorf("notifications = %v", got)
		}
		e.wantNoErrors()
	})
}

// Task 14.12: a timed-out ant's draft PR and comment carry the progress
// summary, not the agent's last message (spec ant-timebox, "Budget
// exhaustion").
func TestTimeRunsOutKeepsTheProgressSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.commit(id, "WIP")
		e.mustCallTool(ghRef(12), "report_progress", `{"text":"Safari done, Firefox next."}`)
		e.execOf(ghRef(12)).EmitLastMessage("Starting on Firefox.")
		e.end(ghRef(12), ports.ExecEndedAtDeadline)

		e.wantState(id, domain.AntTimedOut)
		pr := e.onePR(id)
		if !pr.Draft || !strings.Contains(pr.Body, "Safari done, Firefox next.") || strings.Contains(pr.Body, "Starting on Firefox.") {
			t.Errorf("PR = %+v, want a draft with the progress summary only", pr)
		}
		if got := e.summaries(12); len(got) != 1 || strings.Contains(got[0], "Starting on Firefox.") {
			t.Errorf("summary comments = %q, want no last message", got)
		}
		e.wantNoErrors()
	})
}

func TestTimeRunsOutWithNoCommits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		e.end(ghRef(12), ports.ExecEndedAtDeadline)

		e.wantState(id, domain.AntTimedOut)
		if prs := e.antPRs(id); len(prs) != 0 {
			t.Errorf("PRs = %+v, want none", prs)
		}
		if got := e.summaries(12); len(got) != 1 {
			t.Errorf("summary comments = %q, want only the progress comment", got)
		}
		e.wantParked(12)
		e.wantNoErrors()
	})
}

// After ask_question the supervisor ends the run itself, as after
// finish, and records it as stopped with the session saved.
func TestQuestionEndsTheRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		x := e.execOf(ghRef(12))
		x.Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "session-12"})
		e.settle()
		e.mustCallTool(ghRef(12), "ask_question", `{"text":"30 or 60 seconds?"}`)

		e.wantState(id, domain.AntAwaitingInput)
		if stops := x.Stops(); !slices.Equal(stops, []time.Duration{app.StopGrace}) {
			t.Errorf("stops = %v", stops)
		}
		run := e.lastRun(id)
		if run.Outcome != ports.OutcomeStopped || run.Reason != "asked a question" || run.SessionID != "session-12" {
			t.Errorf("run = %+v", run)
		}
		if got := e.summaries(12); len(got) != 0 {
			t.Errorf("summary comments = %q, want none", got)
		}
		// The claim is parked while the ant awaits input.
		e.wantParked(12)
		e.wantActive()
		e.wantNoErrors()
	})
}
