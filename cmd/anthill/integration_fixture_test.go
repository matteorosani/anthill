package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/adapters/sqlite"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The integration checks (task group 19) run whole daemons in-process
// over the real composition root, each with its own anthill home, real
// configuration file, control interface and SQLite store. Agents are the
// fake agent, a real process whose tool calls go through anthill mcp and
// the control interface. The instances share only a fake provider and
// its clock, as two machines share GitHub.

// world is what instances share: the provider and its clock.
type world struct {
	t     *testing.T
	gh    *testkit.Provider
	clock *testkit.Clock
}

func newWorld(t *testing.T) *world {
	clock := testkit.NewClock(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC))
	return &world{t: t, gh: testkit.NewProvider(clock), clock: clock}
}

// realTimeout bounds every wait for the daemons, in real time.
const realTimeout = 20 * time.Second

// eventually waits until cond holds, failing the test after realTimeout.
// It never moves the clock. ins are the instances whose logs a failure
// shows.
func (w *world) eventually(what string, cond func() bool, ins ...*instance) {
	w.t.Helper()
	w.advanceUntil(what, 0, cond, ins...)
}

// advanceUntil moves the clock by step while cond does not hold, and
// fails the test when cond still does not hold after realTimeout. It
// moves the clock only while no fake agent of ins is running, since an
// agent works in real time and its budget or time limit counts on the
// clock; Anthill then reacts before cond is checked again. So time moves
// only as far as needed, through a claim's re-read delay or to the next
// poll, and never under an agent. A zero step never moves the clock.
func (w *world) advanceUntil(what string, step time.Duration, cond func() bool, ins ...*instance) {
	w.t.Helper()
	deadline := time.Now().Add(realTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			w.t.Fatalf("timed out waiting for %s%s", what, failureReport(ins))
		}
		if step > 0 && !agentsRunning(ins) {
			w.clock.Advance(step)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// agentsRunning reports whether a fake agent of one of ins is running.
func agentsRunning(ins []*instance) bool {
	for _, in := range ins {
		if x := in.executor(); x != nil && x.Running() > 0 {
			return true
		}
	}
	return false
}

// failureReport is what the instances did, for a failure message: their
// logs, executions, notifications and fake agents' output.
func failureReport(ins []*instance) string {
	var b strings.Builder
	for _, in := range ins {
		b.WriteString("\n--- daemon log of " + in.name() + ":\n" + in.stderr.String())
		for _, x := range in.executions() {
			fmt.Fprintf(&b, "\nexecution %s: tool token %v, ended %v, stops %v", x.ID, x.Spec.Run.ToolToken != "", x.Ended(), x.Stops())
		}
		for _, n := range in.notifier.Sent() {
			fmt.Fprintf(&b, "\nnotification: %s: %s", n.Title, n.Body)
		}
		b.WriteString(in.agentOutput())
	}
	return b.String()
}

// instanceOptions set up one instance.
type instanceOptions struct {
	login, machine string
	// settings are extra lines of the global configuration file, such as
	// "auto-resume: true".
	settings string
}

// testPollInterval is the poll interval of the instances.
const testPollInterval = time.Minute

// instance is one Anthill instance: a daemon that a test starts, stops
// and starts again on the same anthill home and store.
type instance struct {
	*testEnv
	t    *testing.T
	w    *world
	opts instanceOptions
	git  *testkit.SourceControl
	// notifier records the desktop notifications of every daemon run.
	notifier *testkit.Notifier

	mu sync.Mutex
	// reply answers the comment classifier calls; nil holds them. Set it
	// with setReply.
	reply func(spec ports.ExecSpec) (string, bool)
	// queue is given to the executor of the next daemon start.
	queue []testkit.AgentRun
	exec  *testkit.AgentExecutor
	store *sqlite.Store
	stop  func(between func()) int
	// beforeClose, when set, is called once by the next daemon stop
	// after the daemon has let go of its runs and waited for its
	// background work, and before it closes its store: the adapters'
	// Close.
	beforeClose func()
	// execs keeps the executor of every daemon start, the current one
	// last.
	execs []*testkit.AgentExecutor
}

func newInstance(t *testing.T, w *world, opts instanceOptions) *instance {
	t.Helper()
	te := newTestEnv(t)
	te.clock = w.clock
	te.environment.clock = w.clock
	in := &instance{
		testEnv: te, t: t, w: w, opts: opts,
		git: testkit.NewSourceControl(), notifier: &testkit.Notifier{},
		reply: func(ports.ExecSpec) (string, bool) { return "actionable", true },
	}
	w.gh.AddWriter(shopRepo, opts.login)
	in.git.AddClone(te.dir, shopRepo, "main")
	config := "poll-interval: " + testPollInterval.String() + "\n" + opts.settings +
		"repositories:\n  - provider: github\n    remote: acme/shop\n    path: '" + te.dir + "'\n"
	if err := os.WriteFile(te.configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	te.desktop = func(*slog.Logger) ports.Notifier { return in.notifier }
	te.ports = in.ports
	t.Cleanup(in.cleanup)
	return in
}

func (in *instance) name() string { return in.opts.login + "@" + in.opts.machine }

// ports builds the adapters of one daemon start: the SQLite store in the
// anthill home, and a new executor, since the processes of the previous
// daemon are gone.
func (in *instance) ports(ctx context.Context, env *environment, _ *app.ConfigSnapshot, _ *slog.Logger) (daemonPorts, error) {
	if err := os.MkdirAll(env.home, 0o700); err != nil {
		return daemonPorts{}, err
	}
	store, err := sqlite.Open(ctx, filepath.Join(env.home, storeFileName))
	if err != nil {
		return daemonPorts{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return daemonPorts{}, err
	}
	caps := ports.Capabilities{Resume: true, LiveInput: true, Usage: true}
	agentDir := filepath.Join(in.dir, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return daemonPorts{}, err
	}
	exec := testkit.NewAgentExecutor(
		testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerClaudeCode: caps}),
		testkit.AgentExecutorOptions{
			Dir: agentDir,
			MCP: func(token string) []string {
				return []string{exe, anthillFlag, "mcp", "--ant", token, "--address", env.address}
			},
			Reply: func(spec ports.ExecSpec) (string, bool) {
				in.mu.Lock()
				reply := in.reply
				in.mu.Unlock()
				if reply == nil {
					return "", false
				}
				return reply(spec)
			},
		})
	in.mu.Lock()
	exec.Queue(in.queue...)
	in.queue = nil
	in.exec, in.store = exec, store
	in.execs = append(in.execs, exec)
	in.mu.Unlock()
	host := testkit.NewCodeHost(in.w.gh, in.git, in.dir, shopRepo)
	client := host.Client(in.opts.login)
	login := in.opts.login
	return daemonPorts{
		Store: store,
		Connections: []providerConnection{{
			Name: domain.ConnectionGitHub, Provider: client,
			Login: func(context.Context) (string, error) { return login, nil },
		}},
		Git: host.Git(), Files: testkit.NewWorktreeFiles(), Executor: exec,
		Runners:  map[domain.Runner]ports.AgentRunner{domain.RunnerClaudeCode: testkit.NewAgentRunner(caps)},
		Verifier: &testkit.Verifier{}, Machine: in.opts.machine,
		Close: func() error {
			in.mu.Lock()
			before := in.beforeClose
			in.beforeClose = nil
			in.mu.Unlock()
			if before != nil {
				before()
			}
			exec.Close()
			return store.Close()
		},
	}, nil
}

// setReply sets how the classifier calls of the instance are answered
// from now on; nil holds them.
func (in *instance) setReply(reply func(spec ports.ExecSpec) (string, bool)) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.reply = reply
}

// queueAgents gives the fake agents of the next executions, in order.
// What the daemon running now has not used when it stops is lost.
func (in *instance) queueAgents(runs ...testkit.AgentRun) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.exec != nil {
		in.exec.Queue(runs...)
		return
	}
	in.queue = append(in.queue, runs...)
}

// start runs `anthill daemon` in-process, as a signal-driven process
// would run, and waits until it answers on the control interface.
func (in *instance) start() {
	in.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"daemon"}, in.environment) }()
	in.mu.Lock()
	in.stop = func(between func()) int {
		cancel()
		if between != nil {
			between()
		}
		select {
		case code := <-done:
			return code
		case <-time.After(realTimeout):
			in.t.Errorf("%s: the daemon did not stop%s", in.name(), failureReport([]*instance{in}))
			return -1
		}
	}
	in.mu.Unlock()
	client := control.NewClient(in.address)
	deadline := time.Now().Add(realTimeout)
	for {
		select {
		case code := <-done:
			in.t.Fatalf("%s: the daemon exited with %d: %s", in.name(), code, in.stderr.String())
		default:
		}
		err := client.Call(context.Background(), methodStatus, nil, nil)
		if err == nil {
			return
		}
		if !errors.Is(err, control.ErrNotRunning) || time.Now().After(deadline) {
			in.t.Fatalf("%s: daemon not reachable: %v", in.name(), err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// stopDaemon stops the daemon, as a signal would, and waits until it has
// closed its store. Once the daemon's context is cancelled, the
// classifier calls still in progress end as killed: they die with the
// daemon's process, and the daemon waits for them. The daemon lets go of
// the runs' agents before it closes its store; the executor's Close then
// kills and ends them, as their processes would end after the daemon's,
// and nothing records those ends.
func (in *instance) stopDaemon() {
	in.t.Helper()
	in.mu.Lock()
	stop := in.stop
	in.stop = nil
	in.mu.Unlock()
	if stop == nil {
		return
	}
	code := stop(func() {
		for _, x := range in.classifierCalls() {
			x.End(ports.ExecExited, testkit.StoppedExit)
		}
	})
	if code != 0 {
		in.t.Errorf("%s: the daemon exited with %d", in.name(), code)
	}
}

// classifierCalls are the instance's comment classifier calls: the
// executions without a tool token.
func (in *instance) classifierCalls() []*testkit.FakeExecution {
	var out []*testkit.FakeExecution
	for _, x := range in.executions() {
		if x.Spec.Run.ToolToken == "" {
			out = append(out, x)
		}
	}
	return out
}

func (in *instance) cleanup() {
	in.stopDaemon()
	var bad []string
	log := in.stderr.String()
	for line := range strings.Lines(log) {
		line = strings.TrimSuffix(line, "\n")
		if strings.Contains(line, "level=ERROR") {
			bad = append(bad, line)
		}
	}
	if len(bad) > 0 {
		in.t.Errorf("%s: daemon log has errors:\n%s", in.name(), strings.Join(bad, "\n"))
	}
	if in.t.Failed() {
		in.t.Logf("daemon log of %s:\n%s%s", in.name(), in.stderr.String(), in.agentOutput())
	}
}

// executor is the executor of the daemon running now, or of the last.
func (in *instance) executor() *testkit.AgentExecutor {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.exec
}

// db is the store of the daemon running now. Only read it while the
// daemon runs.
func (in *instance) db() *sqlite.Store {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.store
}

// executions are the executions of every daemon start, in order.
func (in *instance) executions() []*testkit.FakeExecution {
	in.mu.Lock()
	execs := append([]*testkit.AgentExecutor(nil), in.execs...)
	in.mu.Unlock()
	var out []*testkit.FakeExecution
	for _, x := range execs {
		out = append(out, x.Executions()...)
	}
	return out
}

// agentRuns are the executions with a tool token: runs, review agents and
// fix agents, without the classifier calls.
func (in *instance) agentRuns() []*testkit.FakeExecution {
	var out []*testkit.FakeExecution
	for _, x := range in.executions() {
		if x.Spec.Run.ToolToken != "" {
			out = append(out, x)
		}
	}
	return out
}

// agentOutput is what every fake agent printed, for failure messages.
func (in *instance) agentOutput() string {
	in.mu.Lock()
	execs := append([]*testkit.AgentExecutor(nil), in.execs...)
	in.mu.Unlock()
	var b strings.Builder
	for _, x := range execs {
		for _, f := range x.Executions() {
			if out := x.Output(f.ID); out != "" {
				b.WriteString("\n--- fake agent " + string(f.ID) + ":\n" + out)
			}
		}
	}
	return b.String()
}

// ant is the current ant of an item, read from the running daemon's
// store; ok is false when there is none.
func (in *instance) ant(ref domain.Ref) (ports.AntRecord, bool) {
	rec, err := in.db().CurrentAnt(context.Background(), ref)
	return rec, err == nil
}

// antIn reports whether the item's ant is in state st.
func (in *instance) antIn(ref domain.Ref, st domain.AntState) func() bool {
	return func() bool {
		rec, ok := in.ant(ref)
		return ok && rec.State() == st
	}
}

// mustRun runs a command line against the daemon and fails the test
// unless it succeeds; it returns the output.
func (in *instance) mustRun(args ...string) string {
	in.t.Helper()
	code, stdout, stderr := in.command(args...)
	if code != 0 {
		in.t.Fatalf("%s: anthill %s = %d: %s%s\ndaemon log:\n%s%s", in.name(), strings.Join(args, " "), code, stdout, stderr, in.stderr.String(), in.agentOutput())
	}
	return stdout
}
