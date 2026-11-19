package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// reactEnv is one instance with a supervisor and a change reactor over
// fakes. Its classifier says what verdicts says, actionable by default.
type reactEnv struct {
	*supEnv
	reactor    *app.ChangeReactor
	classifier *fakeClassifier
	talk       *fakeConversation
	findings   *fakeIntake
	next       ports.OutboxID
}

func newReactEnv(t *testing.T, opts ...func(*app.AntSupervisorDeps)) *reactEnv {
	t.Helper()
	all := append([]func(*app.AntSupervisorDeps){func(d *app.AntSupervisorDeps) {
		d.Due = nil
		tools := d.Tokens.(*app.AntTools)
		d.Messages, d.Extensions = tools, tools
		d.Holds = app.NewItemHolds(d.Tracker, d.Route)
	}}, opts...)
	e := &reactEnv{supEnv: newSupEnv(t, 2, all...)}
	e.classifier = &fakeClassifier{verdict: domain.Actionable}
	e.talk, e.findings = &fakeConversation{}, &fakeIntake{}
	e.reactor = e.newReactor(e.store, e.claiming, e.sup, e.classifier)
	return e
}

func (e *reactEnv) newReactor(store app.ChangeReactorStore, claims app.HeldClaims, ants app.ChangeRouter, c app.RelevanceClassifier) *app.ChangeReactor {
	return app.NewChangeReactor(app.ChangeReactorDeps{
		Store: store, Claims: claims, Ants: ants,
		Tracker: func(context.Context, domain.Ref) (ports.TrackerPort, error) { return e.gh.Client("mrosani"), nil },
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) {
			return e.gh.Client("mrosani"), nil
		},
		Route: func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Clone: e.clone, Repo: shop, Settings: e.settings}, nil
		},
		Classifier:   c,
		Conversation: e.talk,
		Findings:     e.findings,
	})
}

// poll hands the events of one poll to the reactor, then flushes it.
func (e *reactEnv) poll(events ...domain.ChangeEvent) {
	e.t.Helper()
	e.mustDo(func() error {
		for _, ev := range events {
			e.next++
			if err := e.reactor.Handle(e.ctx, ports.OutboxEvent{ID: e.next, Event: ev, DetectedAt: e.clock.Now()}); err != nil {
				return err
			}
		}
		return e.reactor.Flush(e.ctx)
	})
}

// succeed accepts item n and finishes its first run complete with a PR.
func (e *reactEnv) succeed(n int) ports.AntID {
	e.t.Helper()
	id := e.mustAccept(n)
	e.execOf(ghRef(n)).Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: fmt.Sprintf("session-%d", n)})
	e.settle()
	e.commit(id, "Fix the login timeout")
	e.mustCallTool(ghRef(n), "push", `{}`)
	e.mustCallTool(ghRef(n), "open_pull_request", `{"title":"Fix the login timeout","body":"Raises the timeout.","draft":false}`)
	e.mustCallTool(ghRef(n), "finish", finishArgs(domain.FinishComplete, "Done."))
	e.wantState(id, domain.AntSucceeded)
	return id
}

func (e *reactEnv) prComment(id ports.AntID, actor, body string) domain.ChangeEvent {
	return domain.ChangeEvent{Ref: *e.ant(id).PR, Kind: domain.ChangeCommentAdded, Actor: actor, Body: body, At: e.clock.Now(), Origin: domain.OriginHuman, CommentID: "c" + body}
}

func itemComment(n int, actor, body string) domain.ChangeEvent {
	return domain.ChangeEvent{Ref: ghRef(n), Kind: domain.ChangeCommentAdded, Actor: actor, Body: body, Origin: domain.OriginHuman, CommentID: "c" + body}
}

func bodyEdit(n int, actor, old, body string) domain.ChangeEvent {
	return domain.ChangeEvent{Ref: ghRef(n), Kind: domain.ChangeItemEdited, Actor: actor, Field: domain.FieldBody, Old: old, Body: body, Origin: domain.OriginHuman}
}

func label(n int, kind domain.ChangeKind, name string) domain.ChangeEvent {
	return domain.ChangeEvent{Ref: ghRef(n), Kind: kind, Actor: "mrosani", Label: name, Origin: domain.OriginHuman}
}

func (e *reactEnv) stored(id ports.AntID) []ports.StoredChange {
	e.t.Helper()
	c, err := e.store.StoredChanges(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *reactEnv) wantRuns(id ports.AntID, n int) []ports.RunRecord {
	e.t.Helper()
	runs := e.runs(id)
	if len(runs) != n {
		e.t.Fatalf("ant %d has %d runs, want %d", id, len(runs), n)
	}
	return runs
}

func (e *reactEnv) wantNotified(kind ports.NotificationKind) {
	e.t.Helper()
	if !slices.Contains(e.notifier.Kinds(), kind) {
		e.t.Fatalf("notifications = %v, want %s among them", e.notifier.Kinds(), kind)
	}
}

func (e *reactEnv) setLabels(n int, add, remove string) {
	e.gh.EditItem(ghRef(n), func(w *domain.WorkItem) {
		if add != "" {
			w.Labels = append(w.Labels, add)
		}
		w.Labels = slices.DeleteFunc(w.Labels, func(l string) bool { return l == remove })
	})
}

// fakeClassifier answers with verdict, or fails with err, and records
// the changes it was asked about.
type fakeClassifier struct {
	mu      sync.Mutex
	verdict domain.Relevance
	err     error
	asked   []string
}

func (c *fakeClassifier) Classify(_ context.Context, _ ports.AntRecord, _ domain.WorkItem, e domain.ChangeEvent) (domain.Relevance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, e.Body)
	return c.verdict, c.err
}

func (c *fakeClassifier) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

// fakeConversation stands for the conversation service: HandleCommands takes
// the comments holding /anthill lines and asks to route those command
// says to, and HandleAnswer takes those answer says are answers.
type fakeConversation struct {
	mu       sync.Mutex
	route    func(app.ChangeInput) bool
	answer   func(app.ChangeInput) bool
	commands []app.ChangeInput
	answers  []app.ChangeInput
}

func (f *fakeConversation) HandleCommands(_ context.Context, in app.ChangeInput) (app.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(domain.ParseCommands(in.Event.Body)) == 0 {
		return app.CommandResult{}, nil
	}
	f.commands = append(f.commands, in)
	return app.CommandResult{Handled: true, Route: f.route != nil && f.route(in)}, nil
}

func (f *fakeConversation) HandleAnswer(_ context.Context, in app.ChangeInput) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, in)
	return f.answer != nil && f.answer(in), nil
}

func (f *fakeConversation) got() (commands, answers []app.ChangeInput) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]app.ChangeInput(nil), f.commands...), append([]app.ChangeInput(nil), f.answers...)
}

// fakeIntake stands for the findings handler: it records what it got.
type fakeIntake struct {
	mu  sync.Mutex
	got []app.ChangeInput
}

func (f *fakeIntake) HandleFindings(_ context.Context, in app.ChangeInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, in)
	return nil
}

func (f *fakeIntake) inputs() []app.ChangeInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]app.ChangeInput(nil), f.got...)
}

// Spec change-reaction, "Thank-you comment" with the classifier's
// verdict, and "Comment from a user without write access".
func TestInformationalCommentsStartNoRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.classifier.verdict = domain.Informational
		e.poll(e.prComment(id, "lbianchi", "thanks, looks good"))
		e.poll(e.prComment(id, "stranger", "please also add dark mode"))

		e.wantState(id, domain.AntSucceeded)
		e.wantRuns(id, 1)
		if got := e.stored(id); len(got) != 0 {
			t.Errorf("stored = %+v", got)
		}
		// The rules decide the outsider's comment: no agent call.
		if got := e.classifier.calls(); !slices.Equal(got, []string{"thanks, looks good"}) {
			t.Errorf("classifier asked about %q", got)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Reviewer asks for a change", with a classifier
// that fails: the comment is actionable (design D8).
func TestClassifierFailureMakesTheCommentActionable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.classifier.err = errors.New("runner crashed")
		e.poll(e.prComment(id, "lbianchi", "please also handle the empty-password case"))

		e.wantState(id, domain.AntRunning)
		runs := e.wantRuns(id, 2)
		if runs[1].Kind != ports.RunFollowUp || runs[1].NewBudget != e.ant(id).Base/2 {
			t.Errorf("follow-up run = %+v", runs[1])
		}
		if call := e.briefs.last(); call.kind != "resume" || len(call.changes) != 1 || call.changes[0].Event.Body != "please also handle the empty-password case" {
			t.Errorf("input = %+v", call)
		}
		if s := e.execOf(ghRef(12)).Spec.Session; s != "session-12" {
			t.Errorf("the follow-up run resumed session %q", s)
		}
		e.wantNotified(domain.NotifyFollowUpStarted)
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Ticket author edits without write access" and
// "Requirement changed".
func TestTicketAuthorsEditIsActionable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.poll(bodyEdit(12, "ada", "Steps to reproduce.", "Steps to reproduce.\n\n- [ ] works offline"))

		e.wantRuns(id, 2)
		if got := e.classifier.calls(); len(got) != 0 {
			t.Errorf("classifier asked about an edit: %q", got)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Ticket edited during a run".
func TestTicketEditedDuringARun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.mustAccept(12)
		e.poll(bodyEdit(12, "mrosani", "Steps to reproduce.\nTimeout 30 s.", "Steps to reproduce.\nTimeout 60 s."))

		inputs := e.execOf(ghRef(12)).Inputs()
		if len(inputs) != 1 || !strings.Contains(inputs[0], "- Timeout 30 s.") || !strings.Contains(inputs[0], "+ Timeout 60 s.") || !strings.Contains(inputs[0], "  Steps to reproduce.") {
			t.Fatalf("live input = %q", inputs)
		}
		if got := e.stored(id); len(got) != 0 {
			t.Errorf("stored = %+v", got)
		}
		e.wantRuns(id, 1)
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Runner without live input or hooks".
func TestTicketEditedDuringARunWithoutLiveInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		e.exec.SetCapabilities(domain.RunnerClaudeCode, ports.Capabilities{Resume: true})
		e.mustAccept(12)
		e.poll(bodyEdit(12, "mrosani", "Timeout 30 s.", "Timeout 60 s."))

		if inputs := e.execOf(ghRef(12)).Inputs(); len(inputs) != 0 {
			t.Fatalf("live input = %q", inputs)
		}
		res := e.callTool(ghRef(12), domain.ToolTimeRemaining, `{}`)
		if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "+ Timeout 60 s.") {
			t.Fatalf("notices = %q", res.Notices)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Comment on a timed-out ant's draft PR".
func TestCommentOnATimedOutAntsDraftPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.move(id, domain.AntQueued, "test")
		e.move(id, domain.AntStarting, "test")
		e.move(id, domain.AntRunning, "test")
		e.move(id, domain.AntTimedOut, "budget used up")
		e.poll(e.prComment(id, "lbianchi", "the draft misses the docs"))

		e.wantState(id, domain.AntTimedOut)
		e.wantRuns(id, 1)
		if got := e.stored(id); len(got) != 1 {
			t.Fatalf("stored = %+v", got)
		}
		e.wantNotified(domain.NotifyChangeStored)

		e.mustDo(func() error { return e.sup.Continue(e.ctx, id, 0) })
		e.wantRuns(id, 2)
		if call := e.briefs.last(); len(call.changes) != 1 || call.changes[0].Event.Body != "the draft misses the docs" {
			t.Errorf("continue input = %+v", call)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Ticket edited while waiting for an answer".
func TestTicketEditedWhileWaitingForAnAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.mustAccept(12)
		e.mustCallTool(ghRef(12), "ask_question", `{"text":"30 or 60 seconds?"}`)
		e.wantState(id, domain.AntAwaitingInput)
		e.poll(bodyEdit(12, "mrosani", "Timeout 30 s.", "Timeout 60 s."))
		e.wantState(id, domain.AntAwaitingInput)

		e.answers.set(id, &domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Body: "60 seconds", Actor: "mrosani"})
		e.trigger(id)
		call := e.briefs.last()
		if call.answer == nil || call.answer.Body != "60 seconds" || len(call.changes) != 1 || call.changes[0].Event.Kind != domain.ChangeItemEdited {
			t.Errorf("input = %+v", call)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Two comments in one poll".
func TestTwoCommentsInOnePoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.poll(e.prComment(id, "lbianchi", "rename the flag"), e.prComment(id, "mrosani", "and add a test"))

		e.wantRuns(id, 2)
		call := e.briefs.last()
		if len(call.changes) != 2 || call.changes[0].Event.Body != "rename the flag" || call.changes[1].Event.Body != "and add a test" {
			t.Errorf("input = %+v", call)
		}
		if inputs := e.execOf(ghRef(12)).Inputs(); len(inputs) != 0 {
			t.Errorf("live input = %q", inputs)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Auto-follow-up disabled".
func TestAutoFollowUpDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.settings.AutoFollowUp = false
		e.poll(e.prComment(id, "lbianchi", "rename the flag"))

		rec := e.wantState(id, domain.AntSucceeded)
		if rec.FollowUpProposal != domain.ProposalPending {
			t.Fatalf("proposal = %q", rec.FollowUpProposal)
		}
		e.wantRuns(id, 1)
		e.wantNotified(domain.NotifyFollowUpProposed)

		// Declined, the change is kept, and the next one brings a new
		// proposal for both.
		e.mustDo(func() error { return e.sup.DeclineFollowUp(e.ctx, id) })
		if rec := e.ant(id); rec.FollowUpProposal != domain.ProposalDeclined {
			t.Fatalf("proposal after decline = %q", rec.FollowUpProposal)
		}
		e.poll(e.prComment(id, "lbianchi", "and add a test"))
		if rec := e.ant(id); rec.FollowUpProposal != domain.ProposalPending {
			t.Fatalf("proposal after a new change = %q", rec.FollowUpProposal)
		}

		e.mustDo(func() error { return e.sup.AcceptFollowUp(e.ctx, id) })
		rec = e.wantState(id, domain.AntRunning)
		if rec.FollowUpProposal != domain.ProposalNone {
			t.Errorf("proposal after the start = %q", rec.FollowUpProposal)
		}
		if call := e.briefs.last(); len(call.changes) != 2 {
			t.Errorf("input = %+v", call)
		}
		var refused *app.ActionRefusedError
		if err := e.do(func() error { return e.sup.AcceptFollowUp(e.ctx, id) }); !errors.As(err, &refused) {
			t.Errorf("second accept = %v", err)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Limit reached".
func TestFollowUpLimitReached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		e.settings.FollowUpLimit = 1
		id := e.succeed(12)
		e.poll(e.prComment(id, "lbianchi", "rename the flag"))
		e.wantRuns(id, 2)
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Renamed."))
		e.wantState(id, domain.AntSucceeded)

		e.poll(e.prComment(id, "lbianchi", "and add a test"))
		e.wantState(id, domain.AntSucceeded)
		e.wantRuns(id, 2)
		e.wantNotified(domain.NotifyFollowUpLimit)
		if rec := e.ant(id); rec.FollowUpProposal != domain.ProposalNone {
			t.Errorf("proposal = %q", rec.FollowUpProposal)
		}
		e.wantNoErrors()
	})
}

// A run that resumes a follow-up after a question keeps the budget, so
// it does not count against the follow-up limit.
func TestOnlyFollowUpRunsWithANewBudgetCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		e.settings.FollowUpLimit = 1
		id := e.succeed(12)
		e.poll(e.prComment(id, "lbianchi", "rename the flag"))
		e.mustCallTool(ghRef(12), "ask_question", `{"text":"which flag?"}`)
		e.wantState(id, domain.AntAwaitingInput)
		e.answers.set(id, &domain.ChangeEvent{Ref: ghRef(12), Body: "yes", Actor: "mrosani"})
		e.trigger(id)
		e.answers.set(id, nil)
		runs := e.wantRuns(id, 3)
		if runs[2].Kind != ports.RunFollowUp || runs[2].NewBudget != 0 {
			t.Fatalf("resumed run = %+v", runs[2])
		}
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Renamed."))
		e.wantState(id, domain.AntSucceeded)
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Hold added during a run".
func TestHoldAddedDuringARun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.mustAccept(12)
		e.setLabels(12, domain.DefaultHoldLabel, "")
		e.poll(label(12, domain.ChangeLabelAdded, domain.DefaultHoldLabel))
		e.wantState(id, domain.AntRunning)

		// While held, a change for the active run is stored, not
		// delivered.
		e.poll(itemComment(12, "lbianchi", "also cover Safari"))
		if inputs := e.execOf(ghRef(12)).Inputs(); len(inputs) != 0 {
			t.Errorf("live input while held = %q", inputs)
		}
		if got := e.stored(id); len(got) != 1 {
			t.Fatalf("stored = %+v", got)
		}

		// The run continues to its end, and no follow-up starts.
		e.commit(id, "Fix the login timeout")
		e.mustCallTool(ghRef(12), "finish", finishArgs(domain.FinishComplete, "Done."))
		e.wantState(id, domain.AntSucceeded)
		e.wantRuns(id, 1)
		e.wantParked(12)

		// Removing the label handles the stored change as if it had just
		// arrived.
		e.setLabels(12, "", domain.DefaultHoldLabel)
		e.poll(label(12, domain.ChangeLabelRemoved, domain.DefaultHoldLabel))
		e.wantRuns(id, 2)
		if call := e.briefs.last(); len(call.changes) != 1 || call.changes[0].Event.Body != "also cover Safari" {
			t.Errorf("input = %+v", call)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Retry while held".
func TestRetryWhileHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.mustAccept(12)
		e.end(ghRef(12), ports.ExecExited)
		e.wantState(id, domain.AntFailed)
		e.setLabels(12, domain.DefaultHoldLabel, "")

		err := e.do(func() error { return e.sup.Retry(e.ctx, id) })
		var refused *app.ActionRefusedError
		if !errors.As(err, &refused) || refused.Reason != app.ReasonOnHold || !strings.Contains(err.Error(), "item is on hold") {
			t.Fatalf("Retry = %v", err)
		}
		e.wantState(id, domain.AntFailed)
		e.wantRuns(id, 1)
		if !e.claimed(12) {
			t.Error("the claim was not kept")
		}
		err = e.do(func() error { return e.sup.Continue(e.ctx, id, 0) })
		if !errors.As(err, &refused) {
			t.Errorf("Continue of a failed ant = %v", err)
		}
		e.wantNoErrors()
	})
}

// A queued ant whose item is put on hold stays queued, and its run
// starts once the label is removed.
func TestQueuedAntOnHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		e.capacity = 1
		first := e.mustAccept(12)
		queued := e.mustAccept(13)
		e.wantState(queued, domain.AntQueued)
		e.setLabels(13, domain.DefaultHoldLabel, "")
		e.end(ghRef(12), ports.ExecExited)
		e.wantState(first, domain.AntFailed)
		e.wantState(queued, domain.AntQueued)

		e.setLabels(13, "", domain.DefaultHoldLabel)
		e.poll(label(13, domain.ChangeLabelRemoved, domain.DefaultHoldLabel))
		e.wantState(queued, domain.AntRunning)
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Label removed mid-run".
func TestOptInLabelRemovedMidRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.mustAccept(12)
		e.setLabels(12, "", "anthill")
		e.poll(label(12, domain.ChangeLabelRemoved, "anthill"))

		rec := e.wantState(id, domain.AntAbandoned)
		if r := lastReason(rec); r != "opt-in withdrawn" {
			t.Errorf("reason = %q", r)
		}
		if !e.execOf(ghRef(12)).Ended() {
			t.Error("the run was not ended")
		}
		if e.claimed(12) {
			t.Error("the claim was not released")
		}
		if rec.Worktree == "" {
			t.Error("the worktree was forgotten")
		}
		e.wantNotified(domain.NotifyClaimLost)
		e.wantActive()
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Withdrawal of opt-in": closing the item ends
// the ant's work too, and leaves its PR to the developers.
func TestItemClosedWithdrawsTheAnt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.poll(domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeStateChanged, State: string(domain.StateClosedNotPlanned), Actor: "mrosani", Origin: domain.OriginHuman})

		rec := e.wantState(id, domain.AntAbandoned)
		if r := lastReason(rec); r != "item closed" {
			t.Errorf("reason = %q", r)
		}
		if pr := e.onePR(id); pr.Ref != *rec.PR {
			t.Errorf("PR = %+v", pr)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Status comment by another instance": Anthill's
// own changes are ignored, except a shepherd finding on an ant PR.
func TestAnthillAuthoredChangesAreIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		pr := *e.ant(id).PR
		status := domain.AnthillComment{Role: domain.RoleAnt, Body: "working", Marker: domain.Marker{Version: domain.MarkerVersion, Instance: instanceB, Kind: domain.CommentStatus, Ref: "gh-12"}}
		finding := domain.AnthillComment{Role: domain.RoleShepherd, Body: "blocking: nil map", Marker: domain.Marker{Version: domain.MarkerVersion, Instance: instanceB, Kind: domain.CommentFinding, Ref: pr.String()}}
		e.poll(
			domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: "lbianchi", Body: status.String(), Origin: domain.OriginAnthill},
			domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: "lbianchi", Body: finding.String(), Origin: domain.OriginAnthill, ThreadID: "t1"},
		)

		e.wantRuns(id, 1)
		if got := e.classifier.calls(); len(got) != 0 {
			t.Errorf("classifier asked about %q", got)
		}
		got := e.findings.inputs()
		if len(got) != 1 || got[0].Event.ThreadID != "t1" || got[0].Ant == nil || got[0].Ant.ID != id || got[0].Item != ghRef(12) {
			t.Errorf("findings handler got %+v", got)
		}
		e.wantNoErrors()
	})
}

// The intake order: commands come first, whoever's item they are on,
// then answers; neither is classified. A command that asks to be routed,
// as /anthill fix on an ant PR, is routed without classification.
func TestCommandsAndAnswersComeFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.talk.answer = func(in app.ChangeInput) bool { return in.Event.Body == "60 seconds" }
		e.poll(
			itemComment(12, "lbianchi", "/anthill stop"),
			itemComment(14, "lbianchi", "/anthill review"),
			e.prComment(id, "mrosani", "60 seconds"),
			e.prComment(id, "mrosani", "rename the flag"),
		)

		cmds, answers := e.talk.got()
		if len(cmds) != 2 || !cmds[0].Responsible || cmds[1].Responsible || cmds[1].Item != ghRef(14) || cmds[1].Ant != nil {
			t.Errorf("commands got %+v", cmds)
		}
		if len(answers) != 2 || !answers[0].Responsible || answers[0].Ant == nil || answers[0].Item != ghRef(12) {
			t.Errorf("answers got %+v", answers)
		}
		if got := e.classifier.calls(); !slices.Equal(got, []string{"rename the flag"}) {
			t.Errorf("classifier asked about %q", got)
		}
		runs := e.wantRuns(id, 2)
		if call := e.briefs.last(); len(call.changes) != 1 || call.changes[0].Event.Body != "rename the flag" {
			t.Errorf("input = %+v, runs = %+v", call, runs)
		}
		e.wantNoErrors()
	})
}

// A command the conversation asks to route, such as /anthill fix on a
// thread of an ant PR, is routed as an actionable change.
func TestRoutedCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		e.talk.route = func(app.ChangeInput) bool { return true }
		fix := e.prComment(id, "lbianchi", "/anthill fix")
		fix.ThreadID = "t9"
		e.poll(fix)

		e.wantRuns(id, 2)
		if call := e.briefs.last(); len(call.changes) != 1 || call.changes[0].Event.ThreadID != "t9" {
			t.Errorf("input = %+v", call)
		}
		if got := e.classifier.calls(); len(got) != 0 {
			t.Errorf("classifier asked about %q", got)
		}
		if _, answers := e.talk.got(); len(answers) != 0 {
			t.Errorf("a routed command went to HandleAnswer: %+v", answers)
		}
		e.wantNoErrors()
	})
}

// Spec change-reaction, "Colleague's machine ignores the change": two
// instances on one provider; only the one holding the task claim reacts.
func TestColleaguesMachineIgnoresTheChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newReactEnv(t)
		id := a.succeed(12)

		// Instance B shares the provider. It once had an ant for #12 that
		// was never abandoned locally, but holds no claim on it.
		storeB := testkit.NewStateStore()
		old := ports.AntRecord{Item: ghRef(12), Base: a.ant(id).Base, History: []domain.Transition{{To: domain.AntQueued, At: t0}, {From: domain.AntQueued, To: domain.AntStopped, At: t0}}}
		if _, err := storeB.CreateAnt(a.ctx, old); err != nil {
			t.Fatal(err)
		}
		routeB := func(_ context.Context, ref domain.Ref) (app.ClaimTarget, error) {
			return app.ClaimTarget{Provider: a.gh.Client("lbianchi"), Instance: instanceB, Label: domain.DefaultClaimLabel}, nil
		}
		claimingB := app.NewClaiming(routeB, storeB, a.clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
		antsB, classifierB := &recordingRouter{}, &fakeClassifier{verdict: domain.Actionable}
		reactorB := a.newReactor(storeB, claimingB, antsB, classifierB)

		comment := itemComment(12, "lbianchi", "please also handle the empty-password case")
		a.poll(comment)
		a.mustDo(func() error {
			if err := reactorB.Handle(a.ctx, ports.OutboxEvent{ID: 1, Event: comment}); err != nil {
				return err
			}
			return reactorB.Flush(a.ctx)
		})

		a.wantRuns(id, 2)
		if got := a.classifier.calls(); len(got) != 1 {
			t.Errorf("A's classifier asked about %q", got)
		}
		if got := classifierB.calls(); len(got) != 0 {
			t.Errorf("B's classifier asked about %q", got)
		}
		if got := antsB.calls(); len(got) != 0 {
			t.Errorf("B acted: %q", got)
		}
		a.wantNoErrors()
	})
}

// Spec change-reaction, "Restart with a pending change": a change stored
// before a restart, whose follow-up run never started, starts it once.
func TestRestartWithAStoredChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newReactEnv(t)
		id := e.succeed(12)
		comment := e.prComment(id, "lbianchi", "rename the flag")
		// The change was routed, but the daemon stopped before Flush.
		e.mustDo(func() error {
			return e.sup.RouteChange(e.ctx, id, domain.AntChange{ID: 41, Event: comment})
		})
		e.wantRuns(id, 1)

		restarted := e.newReactor(e.store, e.claiming, e.sup, e.classifier)
		e.mustDo(func() error { return restarted.Reconcile(e.ctx) })
		e.wantRuns(id, 2)
		// The same outbox event handed out again is stored once.
		e.mustDo(func() error {
			return e.sup.RouteChange(e.ctx, id, domain.AntChange{ID: 41, Event: comment})
		})
		e.mustDo(func() error { return restarted.Reconcile(e.ctx) })
		e.wantRuns(id, 2)
		e.wantNoErrors()
	})
}

// recordingRouter records what the reactor asks of ant supervision.
type recordingRouter struct {
	mu  sync.Mutex
	log []string
}

func (r *recordingRouter) add(s string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, s)
	return nil
}

func (r *recordingRouter) RouteChange(context.Context, ports.AntID, domain.AntChange) error {
	return r.add("route")
}

func (r *recordingRouter) Trigger(context.Context, ports.AntID) error { return r.add("trigger") }

func (r *recordingRouter) Withdraw(context.Context, ports.AntID, string) error {
	return r.add("withdraw")
}

func (r *recordingRouter) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}
