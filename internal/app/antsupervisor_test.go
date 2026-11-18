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
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The supervisor tests run inside a synctest bubble: acquiring a claim
// waits the re-read delay on the fake clock, and settle moves the clock
// on whenever every goroutine waits for it.

const supClone = "/src/shop"

var supCaps = ports.Capabilities{Resume: true, LiveInput: true, Usage: true}

// supEnv is one instance with a supervisor over fakes.
type supEnv struct {
	t        *testing.T
	ctx      context.Context
	clock    *testkit.Clock
	gh       *testkit.Provider
	store    *testkit.StateStore
	git      *testkit.SourceControl
	clone    ports.Clone
	exec     *testkit.Executor
	runner   *testkit.AgentRunner
	notifier *testkit.Notifier
	claiming *app.Claiming
	claims   *racingClaims
	tools    *app.AntTools
	briefs   *fakeBriefs
	answers  *fakeAnswers
	ends     *fakeRunEnds
	due      *fakeDue
	sup      *app.AntSupervisor
	settings domain.Effective
	capacity int

	mu   sync.Mutex
	errs []error
}

func newSupEnv(t *testing.T, capacity int, opts ...func(*app.AntSupervisorDeps)) *supEnv {
	t.Helper()
	e := &supEnv{t: t, ctx: context.Background(), capacity: capacity}
	e.clock = testkit.NewClock(t0)
	e.gh = testkit.NewProvider(e.clock)
	e.gh.AddWriter(shop, "mrosani", "lbianchi")
	for n := 12; n <= 15; n++ {
		e.putItem(n)
	}
	e.store = testkit.NewStateStore()
	e.git = testkit.NewSourceControl()
	e.clone = e.git.AddClone(supClone, shop, "main")
	e.exec = testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerClaudeCode: supCaps})
	e.runner = testkit.NewAgentRunner(supCaps)
	e.notifier = &testkit.Notifier{}
	e.settings = domain.Defaults()
	e.settings.Capacity = capacity

	route := func(_ context.Context, ref domain.Ref) (app.ClaimTarget, error) {
		return app.ClaimTarget{Provider: e.gh.Client("mrosani"), Instance: instanceA, Label: domain.DefaultClaimLabel}, nil
	}
	e.claiming = app.NewClaiming(route, e.store, e.clock, e.notifier, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
	e.claims = &racingClaims{TaskClaims: e.claiming}
	e.tools = app.NewAntTools(app.AntToolsDeps{Store: e.store, Claims: e.claiming, Clock: e.clock, Executor: e.exec}, app.AntToolsOptions{})
	e.briefs = &fakeBriefs{}
	e.answers = &fakeAnswers{answers: map[ports.AntID]*domain.ChangeEvent{}}
	e.ends = &fakeRunEnds{log: e.store}
	e.due = &fakeDue{}

	tracker := func(context.Context, domain.Ref) (ports.TrackerPort, error) { return e.gh.Client("mrosani"), nil }
	target := func(context.Context, domain.WorkItem) (app.ItemTarget, error) {
		return app.ItemTarget{Repository: shop, Available: true, Settings: e.settings}, nil
	}
	workspace := func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
		return app.ToolWorkspace{Clone: e.clone, Repo: shop, Settings: e.settings}, nil
	}
	deps := app.AntSupervisorDeps{
		Store:       e.store,
		Claims:      e.claims,
		Eligibility: app.NewEligibility(tracker, e.claiming, target, e.store),
		Workspace:   app.NewWorkspace(e.git, "/home/me/.anthill/worktrees"),
		Route:       workspace,
		Runners:     app.NewRunners(map[domain.Runner]ports.AgentRunner{domain.RunnerClaudeCode: e.runner}),
		Executor:    e.exec,
		Tokens:      e.tools,
		Briefs:      e.briefs,
		Answers:     e.answers,
		Clock:       e.clock,
		Notifier:    e.notifier,
		Capacity:    func() int { return e.capacity },
		Runs:        e.ends,
		Due:         e.due,
	}
	for _, o := range opts {
		o(&deps)
	}
	e.sup = app.NewAntSupervisor(deps, app.AntSupervisorOptions{
		MCPCommand: func(token string) []string { return []string{"anthill", "mcp", "--ant", token} },
		Environ:    []string{"PATH=/usr/bin", "GH_TOKEN=secret"},
		Errors: func(err error) {
			e.mu.Lock()
			e.errs = append(e.errs, err)
			e.mu.Unlock()
		},
	})
	t.Cleanup(func() {
		// End what a test left running, including runs that start when
		// a slot frees, so the bubble can exit.
		for ended := true; ended; {
			ended = false
			for _, x := range e.exec.Executions() {
				if !x.Ended() {
					x.End(ports.ExecExited, ports.Exit{})
					ended = true
				}
			}
			e.settle()
		}
		e.sup.Wait()
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, err := range e.errs {
			t.Errorf("background error: %v", err)
		}
	})
	return e
}

func (e *supEnv) putItem(n int) {
	e.gh.PutItem(domain.WorkItem{
		Ref: ghRef(n), Title: fmt.Sprintf("Fix bug %d", n), Body: "Steps to reproduce.",
		State: domain.StateOpen, Labels: []string{"anthill"}, Author: "ada",
	})
}

// settle lets every goroutine run, moving the clock past the claim
// re-read delay whenever one waits for it.
func (e *supEnv) settle() {
	for {
		synctest.Wait()
		if e.clock.Waiters() == 0 {
			return
		}
		e.clock.Advance(domain.DefaultClaimRereadDelay)
	}
}

func (e *supEnv) accept(req app.AcceptRequest) (ports.AntID, error) {
	type result struct {
		id  ports.AntID
		err error
	}
	done := make(chan result, 1)
	go func() {
		id, err := e.sup.Accept(e.ctx, req)
		done <- result{id, err}
	}()
	e.settle()
	r := <-done
	e.settle()
	return r.id, r.err
}

func (e *supEnv) mustAccept(n int) ports.AntID {
	e.t.Helper()
	id, err := e.accept(app.AcceptRequest{Item: ghRef(n)})
	if err != nil {
		e.t.Fatalf("Accept(gh-%d) = %v", n, err)
	}
	return id
}

func (e *supEnv) trigger(id ports.AntID) {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.sup.Trigger(e.ctx, id) }()
	e.settle()
	if err := <-done; err != nil {
		e.t.Fatalf("Trigger(%d) = %v", id, err)
	}
	e.settle()
}

func (e *supEnv) ant(id ports.AntID) ports.AntRecord {
	e.t.Helper()
	rec, err := e.store.Ant(e.ctx, id)
	if err != nil {
		e.t.Fatalf("Ant(%d) = %v", id, err)
	}
	return rec
}

func (e *supEnv) wantState(id ports.AntID, want domain.AntState) ports.AntRecord {
	e.t.Helper()
	rec := e.ant(id)
	if got := rec.State(); got != want {
		e.t.Fatalf("ant %d is %s (%s), want %s", id, got, lastReason(rec), want)
	}
	return rec
}

func lastReason(rec ports.AntRecord) string {
	if len(rec.History) == 0 {
		return ""
	}
	return rec.History[len(rec.History)-1].Reason
}

// move makes the ant's state change as another service would, such as
// the conversation service after a question.
func (e *supEnv) move(id ports.AntID, to domain.AntState, reason string) {
	e.t.Helper()
	rec := e.ant(id)
	ant, err := domain.RestoreAnt(rec.Item, rec.Base, rec.Budget, rec.History)
	if err == nil {
		err = ant.Transition(to, e.clock.Now(), reason)
	}
	if err != nil {
		e.t.Fatal(err)
	}
	rec.History, rec.Budget = ant.History(), ant.Budget
	if err := e.store.SaveAnt(e.ctx, rec); err != nil {
		e.t.Fatal(err)
	}
}

// execOf is the last execution started for the item.
func (e *supEnv) execOf(item domain.Ref) *testkit.FakeExecution {
	e.t.Helper()
	execs := e.exec.Executions()
	for i := len(execs) - 1; i >= 0; i-- {
		if strings.Contains(execs[i].Spec.Run.Worktree, fmt.Sprintf("gh-%d-", item.Number)) {
			return execs[i]
		}
	}
	e.t.Fatalf("no execution for %s", item)
	return nil
}

func (e *supEnv) end(item domain.Ref, kind ports.ExecEventKind) {
	e.execOf(item).End(kind, ports.Exit{Code: 0})
	e.settle()
}

func (e *supEnv) runs(id ports.AntID) []ports.RunRecord {
	e.t.Helper()
	runs, err := e.store.Runs(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return runs
}

func (e *supEnv) claimed(n int) bool {
	held, err := e.store.HeldClaims(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return slices.ContainsFunc(held, func(h ports.HeldClaim) bool { return h.Ref == ghRef(n) })
}

func (e *supEnv) wantActive(items ...int) {
	e.t.Helper()
	var want []domain.Ref
	for _, n := range items {
		want = append(want, ghRef(n))
	}
	if got := e.sup.ActiveRuns(); !slices.Equal(got, want) {
		e.t.Fatalf("active runs = %v, want %v", got, want)
	}
}

// racingClaims lets a test act just before an acquisition, such as
// another instance winning the race.
type racingClaims struct {
	app.TaskClaims
	before func()
}

func (r *racingClaims) Acquire(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (ports.HeldClaim, error) {
	if r.before != nil {
		r.before()
	}
	return r.TaskClaims.Acquire(ctx, ref, kind)
}

// fakeBriefs writes recognisable inputs and records what it was given.
type fakeBriefs struct {
	mu    sync.Mutex
	calls []briefCall
}

type briefCall struct {
	kind    string
	item    domain.Ref
	answer  *domain.ChangeEvent
	changes []domain.AntChange
}

func (b *fakeBriefs) record(kind string, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, briefCall{kind, ant.Item, answer, changes})
	text := fmt.Sprintf("%s brief for %s on %s", kind, ant.Item, ant.Branch)
	if answer != nil {
		text += "; answer: " + answer.Body
	}
	return text + fmt.Sprintf("; %d changes", len(changes)), nil
}

func (b *fakeBriefs) Task(_ context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	return b.record("task", ant, answer, changes)
}

func (b *fakeBriefs) Summary(_ context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	return b.record("summary", ant, answer, changes)
}

func (b *fakeBriefs) Resume(_ context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	return b.record("resume", ant, answer, changes)
}

func (b *fakeBriefs) last() briefCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[len(b.calls)-1]
}

type fakeAnswers struct {
	mu      sync.Mutex
	answers map[ports.AntID]*domain.ChangeEvent
}

func (a *fakeAnswers) Answer(_ context.Context, ant ports.AntID) (*domain.ChangeEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.answers[ant], nil
}

func (a *fakeAnswers) set(ant ports.AntID, answer *domain.ChangeEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answers[ant] = answer
}

// fakeRunEnds records each end and stores it with the reported usage.
type fakeRunEnds struct {
	log  ports.RunLog
	mu   sync.Mutex
	ends []runEnd
}

type runEnd struct {
	run      ports.RunRecord
	reported ports.Usage
	resumed  string
}

func (f *fakeRunEnds) EndRun(ctx context.Context, r ports.RunRecord, reported ports.Usage, resumed string) error {
	f.mu.Lock()
	f.ends = append(f.ends, runEnd{r, reported, resumed})
	f.mu.Unlock()
	r.Usage = reported
	return f.log.EndRun(ctx, r)
}

func (f *fakeRunEnds) all() []runEnd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runEnd(nil), f.ends...)
}

type fakeDue struct {
	mu  sync.Mutex
	run *domain.StartRun
}

func (d *fakeDue) Due(context.Context, ports.AntRecord) (*domain.StartRun, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.run, nil
}

func TestAcceptStartsTheFirstRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)

		// The fake executor reports the start at once.
		rec := e.wantState(id, domain.AntRunning)
		const branch = "ant/gh-12-fix-bug-12"
		if rec.Branch != branch || rec.BaseBranch != "main" || rec.Runner != string(domain.RunnerClaudeCode) || rec.Base != time.Hour {
			t.Errorf("record = %+v", rec)
		}
		if !e.claimed(12) {
			t.Error("the task claim is not held")
		}
		if item, _ := e.gh.Item(ghRef(12)); !domain.HasLabel(item.Labels, domain.DefaultClaimLabel) {
			t.Errorf("labels = %v, want the claim label", item.Labels)
		}
		if got := e.git.Worktrees(supClone); !slices.Equal(got, []string{rec.Worktree}) {
			t.Errorf("worktrees = %v, want %s", got, rec.Worktree)
		}

		x := e.execOf(ghRef(12))
		spec := x.Spec
		if spec.Session != "" || spec.Runner != domain.RunnerClaudeCode || spec.Run.Worktree != rec.Worktree {
			t.Errorf("spec = %+v", spec)
		}
		if want := "task brief for gh-12 on " + branch + "; 0 changes"; spec.Run.Brief != want {
			t.Errorf("brief = %q, want %q", spec.Run.Brief, want)
		}
		if spec.Run.ToolToken == "" || !slices.Equal(spec.Run.MCPCommand, []string{"anthill", "mcp", "--ant", spec.Run.ToolToken}) {
			t.Errorf("MCP command = %v, token %q", spec.Run.MCPCommand, spec.Run.ToolToken)
		}
		if slices.Contains(spec.Run.Env, "GH_TOKEN=secret") {
			t.Error("the agent's environment has the provider token")
		}
		runs := e.runs(id)
		if len(runs) != 1 || runs[0].Kind != ports.RunTask || !runs[0].EndedAt.IsZero() {
			t.Fatalf("runs = %+v", runs)
		}
		if want := runs[0].StartedAt.Add(time.Hour + domain.DefaultGracePeriod); !spec.Deadline.Equal(want) || spec.Run.Budget != time.Hour {
			t.Errorf("deadline %v, budget %v; want %v, 1h", spec.Deadline, spec.Run.Budget, want)
		}
		e.wantActive(12)

		// The token reaches the run's process for live input.
		if _, err := e.tools.Deliver(e.ctx, id, "80% of the budget is used"); err != nil {
			t.Fatal(err)
		}
		if got := x.Inputs(); !slices.Equal(got, []string{"80% of the budget is used"}) {
			t.Errorf("live inputs = %v", got)
		}

		x.Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "sess-12"})
		tokens := int64(1200)
		x.Emit(ports.ExecEvent{Kind: ports.ExecUsage, Usage: ports.Usage{Tokens: &tokens}})
		e.settle()
		rec = e.wantState(id, domain.AntRunning)
		if rec.SessionID != "sess-12" {
			t.Errorf("session = %q, want sess-12", rec.SessionID)
		}

		// A run that exits without finishing makes the ant failed (spec
		// ant-execution, "Exit without finishing").
		e.end(ghRef(12), ports.ExecExited)
		rec = e.wantState(id, domain.AntFailed)
		if r := lastReason(rec); r != app.ReasonExitedWithoutFinish {
			t.Errorf("reason = %q", r)
		}
		ends := e.ends.all()
		if len(ends) != 1 || ends[0].run.Outcome != ports.OutcomeFailed || ends[0].run.SessionID != "sess-12" ||
			ends[0].resumed != "" || ends[0].reported.Tokens == nil || *ends[0].reported.Tokens != 1200 {
			t.Errorf("ends = %+v", ends)
		}
		if _, err := e.tools.Tools(spec.Run.ToolToken); !errors.Is(err, app.ErrUnknownToken) {
			t.Errorf("token after the run: %v, want ErrUnknownToken", err)
		}
		e.wantActive()
	})
}

func TestAcceptClaimLostAtStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		other := app.NewClaiming(func(context.Context, domain.Ref) (app.ClaimTarget, error) {
			return app.ClaimTarget{Provider: e.gh.Client("lbianchi"), Instance: instanceB, Label: domain.DefaultClaimLabel}, nil
		}, testkit.NewStateStore(), e.clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
		// The other instance wins the race after the re-check; settle
		// moves the clock past its re-read delay.
		e.claims.before = func() {
			if _, err := other.Acquire(e.ctx, ghRef(12), domain.ClaimTask); err != nil {
				t.Errorf("the other instance's claim: %v", err)
			}
		}

		_, err := e.accept(app.AcceptRequest{Item: ghRef(12)})
		var taken *app.ClaimTakenError
		if !errors.As(err, &taken) || taken.Holder != instanceB || err.Error() != "claimed by "+instanceB {
			t.Fatalf("Accept() = %v, want claimed by %s", err, instanceB)
		}
		if got := e.git.Worktrees(supClone); len(got) != 0 {
			t.Errorf("worktrees = %v, want none", got)
		}
		if got := e.git.LocalBranches(supClone); slices.ContainsFunc(got, func(b string) bool { return strings.HasPrefix(b, "ant/") }) {
			t.Errorf("local branches = %v, want no ant branch", got)
		}
		if _, err := e.store.CurrentAnt(e.ctx, ghRef(12)); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("CurrentAnt() = %v, want no ant", err)
		}
		if n := len(e.exec.Executions()); n != 0 {
			t.Errorf("%d executions started", n)
		}
		e.wantActive()
	})
}

func TestAcceptBecameIneligible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.State = domain.StateClosedCompleted })

		_, err := e.accept(app.AcceptRequest{Item: ghRef(12)})
		var ineligible *app.IneligibleError
		if !errors.As(err, &ineligible) || err.Error() != "no longer eligible: open" {
			t.Fatalf("Accept() = %v, want no longer eligible: open", err)
		}
		if _, err := e.store.CurrentAnt(e.ctx, ghRef(12)); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("CurrentAnt() = %v, want no ant", err)
		}
		if e.claimed(12) || len(e.git.Worktrees(supClone)) != 0 || len(e.exec.Executions()) != 0 {
			t.Error("acceptance did something")
		}
	})
}

func TestAcceptRunnerNotAvailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		e.runner.Unavailable = errors.New("claude: not found")

		_, err := e.accept(app.AcceptRequest{Item: ghRef(12)})
		if err == nil || err.Error() != "runner claude-code not available" {
			t.Fatalf("Accept() = %v, want runner claude-code not available", err)
		}
		if e.claimed(12) {
			t.Error("a claim was taken")
		}
	})
}

func TestAcceptUndoesEveryStepWhenTheRunCannotStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		e.exec.StartErr = errors.New("spawn failed")

		id, err := e.accept(app.AcceptRequest{Item: ghRef(12)})
		if err == nil || !strings.Contains(err.Error(), "spawn failed") {
			t.Fatalf("Accept() = %v, want the start error", err)
		}
		rec := e.wantState(id, domain.AntAbandoned)
		if r := lastReason(rec); r != app.ReasonStartFailed+": spawn failed" {
			t.Errorf("reason = %q", r)
		}
		if got := e.git.Worktrees(supClone); len(got) != 0 {
			t.Errorf("worktrees = %v, want none", got)
		}
		if got := e.git.LocalBranches(supClone); slices.Contains(got, rec.Branch) {
			t.Errorf("local branches = %v, want %s deleted", got, rec.Branch)
		}
		if e.claimed(12) {
			t.Error("the claim is still held")
		}
		if item, _ := e.gh.Item(ghRef(12)); domain.HasLabel(item.Labels, domain.DefaultClaimLabel) {
			t.Errorf("labels = %v, want no claim label", item.Labels)
		}
		if runs := e.runs(id); len(runs) != 1 || runs[0].Outcome != ports.OutcomeFailed {
			t.Errorf("runs = %+v", runs)
		}
		if got := e.notifier.Kinds(); !slices.Equal(got, []ports.NotificationKind{domain.NotifyFailed}) {
			t.Errorf("notifications = %v", got)
		}
		e.wantActive()
	})
}

func TestAcceptQueuedForCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 1)
		first := e.mustAccept(12)
		queued, err := e.accept(app.AcceptRequest{Item: ghRef(13), BudgetMinutes: 30})
		if err != nil {
			t.Fatal(err)
		}
		rec := e.wantState(queued, domain.AntQueued)
		if r := lastReason(rec); r != app.ReasonCapacityFull || rec.Base != 30*time.Minute {
			t.Errorf("queued ant = %+v", rec)
		}
		// Queued, it holds no claim and has no worktree yet.
		if e.claimed(13) || len(e.git.Worktrees(supClone)) != 1 {
			t.Error("the queued ant was claimed or got a worktree")
		}
		// A change stored while queued goes to the first run.
		change := domain.ChangeEvent{Ref: ghRef(13), Kind: domain.ChangeCommentAdded, Actor: "ada", Body: "also on Firefox"}
		if err := e.store.StoreChange(e.ctx, ports.StoredChange{Ant: queued, Source: 1, Event: change}); err != nil {
			t.Fatal(err)
		}

		e.end(ghRef(12), ports.ExecExited)
		e.wantState(first, domain.AntFailed)
		rec = e.wantState(queued, domain.AntRunning)
		if !e.claimed(13) || rec.Branch == "" {
			t.Errorf("the ant started without its claim or branch: %+v", rec)
		}
		if b := e.briefs.last(); b.kind != "task" || b.item != ghRef(13) || len(b.changes) != 1 || b.changes[0].Event.Body != "also on Firefox" {
			t.Errorf("brief = %+v", b)
		}
		if left, _ := e.store.StoredChanges(e.ctx, queued); len(left) != 0 {
			t.Errorf("stored changes after the start = %+v", left)
		}
		if spec := e.execOf(ghRef(13)).Spec; spec.Run.Budget != 30*time.Minute {
			t.Errorf("budget = %v, want the 30 min override", spec.Run.Budget)
		}
		e.wantActive(13)
	})
}

func TestQueuedAntNoLongerEligibleWhenASlotFrees(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 1)
		e.mustAccept(12)
		queued := e.mustAccept(13)
		e.wantState(queued, domain.AntQueued)
		e.gh.EditItem(ghRef(13), func(w *domain.WorkItem) { w.State = domain.StateClosedCompleted })

		e.end(ghRef(12), ports.ExecExited)
		rec := e.wantState(queued, domain.AntAbandoned)
		if r := lastReason(rec); r != "no longer eligible: open" {
			t.Errorf("reason = %q", r)
		}
		if e.claimed(13) || len(e.exec.Executions()) != 1 {
			t.Error("the ineligible ant was claimed or started")
		}
		if got := e.notifier.Kinds(); !slices.Equal(got, []ports.NotificationKind{domain.NotifyFailed}) {
			t.Errorf("notifications = %v", got)
		}
	})
}

func TestRepositoryCapacityWithinMachineCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 3)
		e.settings.Capacity = 1
		e.mustAccept(12)
		queued := e.mustAccept(13)
		e.wantState(queued, domain.AntQueued)
		e.wantActive(12)
	})
}

func TestAnswerArrivesWhileCapacityIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		asker := e.mustAccept(12)
		x := e.execOf(ghRef(12))
		x.Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "sess-12"})
		e.settle()

		// The ant asks a question: the conversation service moves it to
		// awaiting-input and ends its run.
		e.move(asker, domain.AntAwaitingInput, "asked a question")
		e.sup.EndingRun(ghRef(12), ports.OutcomeStopped, "asked a question")
		if err := e.exec.Stop(x.ID, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		e.settle()
		if ends := e.ends.all(); len(ends) != 1 || ends[0].run.Outcome != ports.OutcomeStopped || ends[0].run.Reason != "asked a question" {
			t.Fatalf("ends = %+v", ends)
		}
		e.wantState(asker, domain.AntAwaitingInput)

		e.mustAccept(13)
		e.mustAccept(14)
		e.wantActive(13, 14)

		// The answer arrives while capacity is full.
		answer := &domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: "ada", Body: "Use the v2 endpoint."}
		e.answers.set(asker, answer)
		e.trigger(asker)
		rec := e.wantState(asker, domain.AntQueued)
		if r := lastReason(rec); r != app.ReasonCapacityFull {
			t.Errorf("reason = %q", r)
		}
		if n := len(e.exec.Executions()); n != 3 {
			t.Fatalf("%d executions, want 3", n)
		}

		// One of the other runs ends: the queued ant's run resumes its
		// session with the answer.
		e.end(ghRef(13), ports.ExecExited)
		e.wantState(asker, domain.AntRunning)
		spec := e.execOf(ghRef(12)).Spec
		if spec.Session != "sess-12" {
			t.Errorf("session = %q, want sess-12", spec.Session)
		}
		if b := e.briefs.last(); b.kind != "resume" || b.answer != answer {
			t.Errorf("brief = %+v, want the resume input with the answer", b)
		}
		if !strings.Contains(spec.Run.Brief, "Use the v2 endpoint.") {
			t.Errorf("input = %q", spec.Run.Brief)
		}
		runs := e.runs(asker)
		if len(runs) != 2 || runs[1].SessionID != "sess-12" || runs[1].Kind != ports.RunTask {
			t.Errorf("runs = %+v", runs)
		}
		e.wantActive(12, 14)

		// The resumed run's end tells the usage recorder which session it
		// resumed.
		e.end(ghRef(12), ports.ExecEndedAtDeadline)
		ends := e.ends.all()
		last := ends[len(ends)-1]
		if last.resumed != "sess-12" || last.run.Outcome != ports.OutcomeTimedOut {
			t.Errorf("last end = %+v", last)
		}
	})
}

func TestRunEndedAtTheDeadlineTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 2)
		id := e.mustAccept(12)
		x := e.execOf(ghRef(12))
		x.Emit(ports.ExecEvent{Kind: ports.ExecStarted})
		e.settle()
		e.wantState(id, domain.AntRunning)
		e.end(ghRef(12), ports.ExecEndedAtDeadline)
		rec := e.wantState(id, domain.AntTimedOut)
		if r := lastReason(rec); r != app.ReasonBudgetUsedUp {
			t.Errorf("reason = %q", r)
		}
		if ends := e.ends.all(); len(ends) != 1 || ends[0].run.Outcome != ports.OutcomeTimedOut {
			t.Errorf("ends = %+v", ends)
		}
	})
}

func TestDueRunOfAQueuedRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSupEnv(t, 1)
		id := e.mustAccept(12)
		e.execOf(ghRef(12)).Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "sess-12"})
		e.settle()
		e.end(ghRef(12), ports.ExecExited)
		e.wantState(id, domain.AntFailed)

		// A retry queued the ant; the queen actions say which run is due.
		e.mustAccept(13)
		e.move(id, domain.AntQueued, app.ReasonCapacityFull)
		e.trigger(id)
		e.wantState(id, domain.AntQueued)

		e.due.run = &domain.StartRun{Kind: domain.RoleTaskRun}
		e.end(ghRef(13), ports.ExecExited)
		e.wantState(id, domain.AntRunning)
		if spec := e.execOf(ghRef(12)).Spec; spec.Session != "sess-12" {
			t.Errorf("session = %q, want the retry to resume sess-12", spec.Session)
		}
	})
}
