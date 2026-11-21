package main

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// daemonEnv runs `anthill daemon` in-process over the real composition
// root (buildDaemon) and the real configuration file, with every port a
// fake: a fake provider, store, git, executor, runner and clock. Nothing
// reaches the network or starts an agent.
type daemonEnv struct {
	*testEnv
	t        *testing.T
	gh       *testkit.Provider
	store    *testkit.StateStore
	git      *testkit.SourceControl
	exec     *testkit.Executor
	notifier *testkit.Notifier
	// driving moves the clock past every timer while it is set, such as
	// the next poll and the claim re-read delay.
	driving atomic.Bool
}

func newDaemonEnv(t *testing.T) *daemonEnv {
	t.Helper()
	te := newTestEnv(t)
	e := &daemonEnv{testEnv: te, t: t, notifier: &testkit.Notifier{}}
	e.gh = testkit.NewProvider(te.clock)
	e.gh.AddWriter(shopRepo, "mrosani")
	e.store = testkit.NewStateStore()
	e.git = testkit.NewSourceControl()
	e.git.AddClone(te.dir, shopRepo, "main")
	caps := ports.Capabilities{Resume: true, LiveInput: true, Usage: true}
	e.exec = testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerClaudeCode: caps})
	runners := map[domain.Runner]ports.AgentRunner{domain.RunnerClaudeCode: testkit.NewAgentRunner(caps)}

	config := "repositories:\n  - provider: github\n    remote: acme/shop\n    path: '" + te.dir + "'\n"
	if err := os.WriteFile(te.configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	te.desktop = func(*slog.Logger) ports.Notifier { return e.notifier }
	te.ports = func(context.Context, *environment, *app.ConfigSnapshot, *slog.Logger) (daemonPorts, error) {
		client := e.gh.Client("mrosani")
		return daemonPorts{
			Store: e.store,
			Connections: []providerConnection{{
				Name: domain.ConnectionGitHub, Provider: client,
				Login: func(context.Context) (string, error) { return "mrosani", nil },
			}},
			Git: e.git, Files: testkit.NewWorktreeFiles(), Executor: e.exec, Runners: runners,
			Verifier: &testkit.Verifier{}, Machine: "test",
		}, nil
	}
	return e
}

// start starts the daemon and the clock driver.
func (e *daemonEnv) start() {
	e.t.Helper()
	e.driving.Store(true)
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
	stop := e.startDaemon(e.t)
	e.t.Cleanup(func() {
		for _, x := range e.exec.Executions() {
			if !x.Ended() {
				x.End(ports.ExecExited, ports.Exit{})
			}
		}
		stop()
		close(done)
		<-stopped
		if strings.Contains(e.stderr.String(), "level=ERROR") {
			e.t.Errorf("daemon log has errors:\n%s", e.stderr.String())
		}
	})
}

// eventually waits until cond holds, failing the test after 10 seconds.
func (e *daemonEnv) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s\ndaemon log:\n%s", what, e.stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// proposals are the "new proposal" notifications raised so far.
func (e *daemonEnv) proposals() []ports.Notification {
	var out []ports.Notification
	for _, n := range e.notifier.Sent() {
		if n.Kind == ports.NotifyProposal {
			out = append(out, n)
		}
	}
	return out
}

// End to end on fake adapters (task 11.5): an opted-in item is polled,
// proposed with a "new proposal" notification, and listed by `anthill
// proposals`; once accepted, it gets an ant, its task claim and a run.
func TestDaemonProposesAndRunsAnOptedInItem(t *testing.T) {
	e := newDaemonEnv(t)
	e.gh.PutItem(domain.WorkItem{
		Ref: shopItem(12), Title: "Fix login timeout", Body: "Steps to reproduce.",
		Type: domain.TypeBug, State: domain.StateOpen, Labels: []string{"anthill"}, Author: "ada",
	})
	e.gh.PutItem(domain.WorkItem{
		Ref: shopItem(13), Title: "Not opted in", Type: domain.TypeBug, State: domain.StateOpen, Author: "ada",
	})
	e.start()

	e.eventually("the new proposal notification", func() bool { return len(e.proposals()) > 0 })
	n := e.proposals()[0]
	if n.Title != "New proposal: #12" || n.Body != "Fix login timeout" || n.Ref == nil || *n.Ref != shopItem(12) {
		t.Errorf("notification = %+v", n)
	}
	e.eventually("a poll", func() bool {
		st, err := e.store.PollStatus(context.Background(), domain.ConnectionGitHub)
		return err == nil && !st.LastSuccess.IsZero()
	})

	out := e.mustRun("proposals")
	if !strings.Contains(out, "#12") || !strings.Contains(out, "Fix login timeout") || strings.Contains(out, "#13") {
		t.Errorf("proposals:\n%s", out)
	}

	e.mustRun("accept", "#12")
	e.eventually("the run of #12", func() bool { return len(e.exec.Executions()) == 1 })
	rec, err := e.store.CurrentAnt(context.Background(), shopItem(12))
	if err != nil {
		t.Fatal(err)
	}
	if st := rec.State(); st != domain.AntStarting && st != domain.AntRunning {
		t.Errorf("ant state = %s", st)
	}
	x := e.exec.Executions()[0]
	if !strings.Contains(x.Spec.Run.Worktree, "gh-12-") || !slices.Contains(x.Spec.Run.MCPCommand, "--ant") {
		t.Errorf("run = %+v", x.Spec.Run)
	}
	if item, _ := e.gh.Item(shopItem(12)); !domain.HasLabel(item.Labels, domain.DefaultClaimLabel) {
		t.Errorf("labels = %v, want the claim label", item.Labels)
	}
	held, err := e.store.HeldClaims(context.Background())
	if err != nil || len(held) != 1 || held[0].Instance != "mrosani@test" {
		t.Errorf("held claims = %+v, %v", held, err)
	}

	// The accepted item is no longer a proposal, and later polls raise no
	// new notification for it.
	e.mustRun("ant", "#12")
	if out := e.mustRun("proposals"); strings.Contains(out, "#12") {
		t.Errorf("proposals after accept:\n%s", out)
	}
	if got := len(e.proposals()); got != 1 {
		t.Errorf("%d proposal notifications, want 1", got)
	}
}

// mustRun runs a command line and fails the test unless it succeeds; it
// returns the output.
func (e *daemonEnv) mustRun(args ...string) string {
	e.t.Helper()
	code, stdout, stderr := e.command(args...)
	if code != 0 {
		e.t.Fatalf("anthill %s = %d: %s%s\ndaemon log:\n%s", strings.Join(args, " "), code, stdout, stderr, e.stderr.String())
	}
	return stdout
}
