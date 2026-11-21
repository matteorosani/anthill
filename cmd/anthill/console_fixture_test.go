package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The console tests run the daemon in-process with a queen console over
// the real application services and fakes: a fake provider, store, git,
// executor and clock. Nothing reaches the network or starts an agent.

var shopRepo = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}

func shopItem(n int) domain.Ref {
	return domain.Ref{Repo: shopRepo, Kind: domain.KindItem, Number: n}
}

// consoleEnv is a test environment whose daemon serves a console.
type consoleEnv struct {
	*testEnv
	t        *testing.T
	ctx      context.Context
	gh       *testkit.Provider
	store    *testkit.StateStore
	git      *testkit.SourceControl
	clone    ports.Clone
	exec     *testkit.Executor
	claiming *app.Claiming
	tools    *app.AntTools
	sup      *app.AntSupervisor
	hub      *app.ActivityHub
	settings domain.Effective

	// driving moves the clock past every timer while it is set, such as
	// the claim re-read delay of an acceptance.
	driving atomic.Bool

	mu   sync.Mutex
	errs []error
}

// newConsoleEnv builds the services over fakes, with items #12 to #15
// open and opted in, and starts the daemon with their console.
func newConsoleEnv(t *testing.T) *consoleEnv {
	t.Helper()
	te := newTestEnv(t)
	e := &consoleEnv{testEnv: te, t: t, ctx: context.Background()}
	e.gh = testkit.NewProvider(te.clock)
	e.gh.AddWriter(shopRepo, "mrosani", "lbianchi")
	for n := 12; n <= 15; n++ {
		e.gh.PutItem(domain.WorkItem{
			Ref: shopItem(n), Title: fmt.Sprintf("Fix bug %d", n), Body: "Steps to reproduce.",
			Type: domain.TypeBug, State: domain.StateOpen, Labels: []string{"anthill"}, Author: "ada",
		})
	}
	e.store = testkit.NewStateStore()
	e.git = testkit.NewSourceControl()
	e.clone = e.git.AddClone("/src/shop", shopRepo, "main")
	caps := ports.Capabilities{Resume: true, LiveInput: true, Usage: true}
	e.exec = testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerClaudeCode: caps})
	e.settings = domain.Defaults()
	e.hub = app.NewActivityHub()

	client := e.gh.Client("mrosani")
	tracker := func(context.Context, domain.Ref) (ports.TrackerPort, error) { return client, nil }
	codeHost := func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return client, nil }
	workspace := func(context.Context, domain.Ref) (app.ToolWorkspace, error) {
		return app.ToolWorkspace{Clone: e.clone, Repo: shopRepo, Settings: e.settings}, nil
	}
	target := func(context.Context, domain.WorkItem) (app.ItemTarget, error) {
		return app.ItemTarget{Repository: shopRepo, Available: true, Settings: e.settings}, nil
	}
	notifier := &testkit.Notifier{}
	claimRoute := func(context.Context, domain.Ref) (app.ClaimTarget, error) {
		return app.ClaimTarget{Provider: client, Instance: "mrosani@test", Label: domain.DefaultClaimLabel}, nil
	}
	e.claiming = app.NewClaiming(claimRoute, e.store, te.clock, notifier, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
	ants := &laterAnts{}
	e.tools = app.NewAntTools(app.AntToolsDeps{
		Store: e.store, Clock: te.clock, Executor: e.exec, Claims: e.claiming,
		Tracker: tracker, CodeHost: codeHost, Workspace: workspace, Git: e.git, Notifier: notifier,
		Finishes: ants, Runs: ants,
	}, app.AntToolsOptions{})
	conv := app.NewConversation(app.ConversationDeps{
		Store: e.store, Tracker: tracker, CodeHost: codeHost, Ants: ants, Clock: te.clock, Notifier: notifier,
		Queen: func(context.Context, domain.Ref) (ports.QueenCommentPort, error) { return client, nil },
	})
	eligibility := app.NewEligibility(tracker, e.claiming, target, e.store)
	e.sup = app.NewAntSupervisor(app.AntSupervisorDeps{
		Store: e.store, Claims: e.claiming, Eligibility: eligibility,
		Workspace: app.NewWorkspace(e.git, "/home/me/.anthill/worktrees"), Route: workspace,
		Runners:  app.NewRunners(map[domain.Runner]ports.AgentRunner{domain.RunnerClaudeCode: testkit.NewAgentRunner(caps)}),
		Executor: e.exec, Tokens: e.tools, Briefs: briefs{}, Answers: conv.Answers(),
		Clock: te.clock, Notifier: notifier, Capacity: func() int { return 2 },
		Runs:    e.hub.RunEnds(app.NewUsageRecorder(e.store)),
		Tracker: tracker, CodeHost: codeHost, Git: e.git,
		Activity: e.hub, Messages: e.tools, Extensions: e.tools,
	}, app.AntSupervisorOptions{
		MCPCommand: func(token string) []string { return []string{"anthill", "mcp", "--ant", token} },
		Errors: func(err error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.errs = append(e.errs, err)
		},
	})
	ants.sup = e.sup
	e.tools.WatchBudgets(e.sup)
	config := app.NewConfiguration(&testkit.ConfigSource{Config: ports.LocalConfig{Repositories: []ports.LocalEntry{{
		Entry: domain.RepositoryEntry{Provider: domain.ConfigGitHub, Remote: "acme/shop", Path: te.dir},
	}}}})
	if _, err := config.Load(e.ctx); err != nil {
		t.Fatal(err)
	}
	te.console = app.NewConsole(app.ConsoleDeps{
		Store: e.store, Ants: e.sup, Eligibility: eligibility,
		Candidates: app.TrackerCandidates(config, func(context.Context, domain.Repository) (ports.TrackerPort, error) { return client, nil }),
		Tracker:    tracker, Answers: conv, Activity: e.hub,
		Workspace: app.NewWorkspace(e.git, "/home/me/.anthill/worktrees"), Route: workspace,
		Repositories: func() []domain.Repository { return []domain.Repository{shopRepo} },
		Clock:        te.clock,
	})

	e.driving.Store(true)
	stopDriver := e.driveClock()
	te.startDaemon(t)
	t.Cleanup(func() {
		for _, x := range e.exec.Executions() {
			if !x.Ended() {
				x.End(ports.ExecExited, ports.Exit{})
			}
		}
		e.sup.Wait()
		stopDriver()
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, err := range e.errs {
			t.Errorf("background error: %v", err)
		}
	})
	return e
}

// driveClock moves the fake clock past each timer that something waits
// for while driving is set, until the returned function is called.
func (e *consoleEnv) driveClock() func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
			if e.driving.Load() && e.clock.Waiters() > 0 {
				e.clock.Advance(domain.DefaultClaimRereadDelay)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// eventually waits until cond holds, failing the test after 10 seconds.
func (e *consoleEnv) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ant is the item's current ant.
func (e *consoleEnv) ant(n int) ports.AntRecord {
	e.t.Helper()
	rec, err := e.store.CurrentAnt(e.ctx, shopItem(n))
	if err != nil {
		e.t.Fatalf("ant of #%d: %v", n, err)
	}
	return rec
}

// execOf is the item's latest execution.
func (e *consoleEnv) execOf(n int) *testkit.FakeExecution {
	e.t.Helper()
	execs := e.exec.Executions()
	for i := len(execs) - 1; i >= 0; i-- {
		if strings.Contains(execs[i].Spec.Run.Worktree, fmt.Sprintf("gh-%d-", n)) {
			return execs[i]
		}
	}
	e.t.Fatalf("no execution for #%d", n)
	return nil
}

// mustRun runs a command line and fails the test unless it succeeds;
// it returns the output.
func (e *consoleEnv) mustRun(args ...string) string {
	e.t.Helper()
	code, stdout, stderr := e.command(args...)
	if code != 0 {
		e.t.Fatalf("anthill %s = %d: %s%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// call makes the item's active run call an ant tool.
func (e *consoleEnv) call(n int, tool, args string) app.ToolResult {
	e.t.Helper()
	return e.tools.Call(e.ctx, e.execOf(n).Spec.Run.ToolToken, tool, json.RawMessage(args))
}

// laterAnts lets the ant tools and the conversation reach the
// supervisor, which is made after them.
type laterAnts struct{ sup *app.AntSupervisor }

func (l *laterAnts) Finish(ctx context.Context, r app.FinishRequest) error {
	return l.sup.Finish(ctx, r)
}
func (l *laterAnts) EndRun(ctx context.Context, ant ports.AntID, run ports.RunID, reason string) error {
	return l.sup.EndRun(ctx, ant, run, reason)
}
func (l *laterAnts) Trigger(ctx context.Context, id ports.AntID) error { return l.sup.Trigger(ctx, id) }
func (l *laterAnts) Stop(ctx context.Context, id ports.AntID, reason string) error {
	return l.sup.Stop(ctx, id, reason)
}
func (l *laterAnts) Retry(ctx context.Context, id ports.AntID) error { return l.sup.Retry(ctx, id) }

// briefs writes short recognisable run inputs.
type briefs struct{}

func (briefs) Task(_ context.Context, ant ports.AntRecord, _ *domain.ChangeEvent, _ []domain.AntChange) (string, error) {
	return "task brief for " + ant.Item.String(), nil
}

func (briefs) Summary(_ context.Context, ant ports.AntRecord, _ *domain.ChangeEvent, _ []domain.AntChange) (string, error) {
	return "summary brief for " + ant.Item.String(), nil
}

func (briefs) Resume(_ context.Context, _ ports.AntRecord, answer *domain.ChangeEvent, _ []domain.AntChange) (string, error) {
	if answer != nil {
		return "answer: " + answer.Body, nil
	}
	return "resume", nil
}
