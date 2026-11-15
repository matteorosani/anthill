package localexec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/adapters/localexec"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestMain(m *testing.M) {
	testkit.RunFakeAgentIfRequested()
	os.Exit(m.Run())
}

// fakeRunner is an AgentRunner that starts the fake agent. The brief is
// the agent's script, as JSON. Its session id is fake-<pid>, and its
// usage is the last usage the agent printed.
type fakeRunner struct {
	caps        ports.Capabilities
	unavailable error

	mu   sync.Mutex
	runs []*fakeRun
}

func (r *fakeRunner) Capabilities() ports.Capabilities { return r.caps }
func (r *fakeRunner) Available(context.Context) error  { return r.unavailable }
func (r *fakeRunner) runsSoFar() []*fakeRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*fakeRun(nil), r.runs...)
}
func (r *fakeRunner) Start(_ context.Context, spec ports.RunSpec, out ports.RunIO) (ports.Run, error) {
	return r.start(spec, out, "")
}

func (r *fakeRunner) Resume(_ context.Context, session string, spec ports.RunSpec, out ports.RunIO) (ports.Run, error) {
	return r.start(spec, out, session)
}

func (r *fakeRunner) start(spec ports.RunSpec, out ports.RunIO, session string) (ports.Run, error) {
	var script testkit.AgentScript
	if err := json.Unmarshal([]byte(spec.Brief), &script); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	rn := &fakeRun{session: session, activity: out.Activity}
	proc, err := agentproc.Start(agentproc.Spec{
		Path:   exe,
		Args:   testkit.FakeAgentArgs(script),
		Dir:    spec.Worktree,
		Env:    os.Environ(),
		Stdin:  r.caps.LiveInput,
		Line:   rn.line,
		Stderr: out.Activity,
		Drain:  time.Second,
	})
	if err != nil {
		return nil, err
	}
	rn.proc = proc
	r.mu.Lock()
	r.runs = append(r.runs, rn)
	r.mu.Unlock()
	return rn, nil
}

type fakeRun struct {
	proc     *agentproc.Process
	activity interface{ Write([]byte) (int, error) }

	mu      sync.Mutex
	session string
	usage   ports.Usage
}

func (r *fakeRun) line(b []byte) {
	var ev testkit.AgentEvent
	if json.Unmarshal(b, &ev) == nil {
		r.mu.Lock()
		switch ev.Event {
		case "start":
			if r.session == "" {
				r.session = fmt.Sprintf("fake-%d", ev.Pid)
			}
		case "usage":
			tokens, cost := ev.Tokens, ev.CostUSD
			r.usage = ports.Usage{Tokens: &tokens, CostUSD: &cost}
		}
		r.mu.Unlock()
	}
	if r.activity != nil {
		_, _ = r.activity.Write(append(append([]byte(nil), b...), '\n'))
	}
}

func (r *fakeRun) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

func (r *fakeRun) Send(_ context.Context, input string) error {
	return r.proc.Write([]byte(input + "\n"))
}
func (r *fakeRun) Stop(grace time.Duration) error { return r.proc.Stop(grace) }
func (r *fakeRun) Wait() ports.Exit               { return r.proc.Wait() }
func (r *fakeRun) Usage() ports.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage
}

const fakeRunnerName domain.Runner = "fake"

var start = time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)

type env struct {
	clock  *testkit.Clock
	runner *fakeRunner
	exec   *localexec.Executor
}

func newEnv(caps ports.Capabilities) *env {
	return newEnvWith(caps, localexec.Options{StopGrace: 200 * time.Millisecond})
}

func newEnvWith(caps ports.Capabilities, opts localexec.Options) *env {
	clock := testkit.NewClock(start)
	runner := &fakeRunner{caps: caps}
	return &env{
		clock:  clock,
		runner: runner,
		exec:   localexec.New(clock, map[domain.Runner]ports.AgentRunner{fakeRunnerName: runner}, opts),
	}
}

func (e *env) start(t *testing.T, deadline time.Time, steps ...testkit.AgentStep) (ports.Execution, *recorder) {
	t.Helper()
	brief, err := json.Marshal(testkit.AgentScript{Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	x, err := e.exec.Start(context.Background(), ports.ExecSpec{
		Runner:   fakeRunnerName,
		Run:      ports.RunSpec{Worktree: t.TempDir(), Brief: string(brief)},
		Deadline: deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := record(x.Events)
	t.Cleanup(func() {
		_ = e.exec.Stop(x.ID, 0)
		rec.waitEnd(t)
	})
	return x, rec
}

// recorder reads an execution's events as they come.
type recorder struct {
	mu     sync.Mutex
	events []ports.ExecEvent
	more   chan struct{}
	end    chan struct{}
}

func record(events <-chan ports.ExecEvent) *recorder {
	r := &recorder{more: make(chan struct{}, 1), end: make(chan struct{})}
	go func() {
		defer close(r.end)
		for ev := range events {
			r.mu.Lock()
			r.events = append(r.events, ev)
			r.mu.Unlock()
			select {
			case r.more <- struct{}{}:
			default:
			}
		}
	}()
	return r
}

func (r *recorder) all() []ports.ExecEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ports.ExecEvent(nil), r.events...)
}

// waitFor waits for the first event that matches.
func (r *recorder) waitFor(t *testing.T, what string, match func(ports.ExecEvent) bool) ports.ExecEvent {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		for _, ev := range r.all() {
			if match(ev) {
				return ev
			}
		}
		select {
		case <-r.more:
		case <-r.end:
			for _, ev := range r.all() {
				if match(ev) {
					return ev
				}
			}
			t.Fatalf("the events ended without %s: %+v", what, r.all())
		case <-timeout:
			t.Fatalf("no %s within 30 s: %+v", what, r.all())
		}
	}
}

func (r *recorder) waitActivity(t *testing.T, contains string) {
	t.Helper()
	r.waitFor(t, "activity "+contains, func(ev ports.ExecEvent) bool {
		return ev.Kind == ports.ExecActivity && strings.Contains(ev.Line, contains)
	})
}

// waitEnd waits until the events channel is closed, and returns the last
// event.
func (r *recorder) waitEnd(t *testing.T) ports.ExecEvent {
	t.Helper()
	select {
	case <-r.end:
	case <-time.After(30 * time.Second):
		t.Fatalf("the events did not end within 30 s: %+v", r.all())
	}
	all := r.all()
	if len(all) == 0 {
		t.Fatal("no events")
	}
	return all[len(all)-1]
}

// stillRunning checks that the process is active: it answers live
// input.
func stillRunning(t *testing.T, e *env, x ports.Execution, rec *recorder, word string) {
	t.Helper()
	if err := e.exec.Send(context.Background(), x.ID, word); err != nil {
		t.Fatalf("send %q: %v", word, err)
	}
	rec.waitActivity(t, word)
}

// waitingAgent waits for two inputs, after starting a child process
// that ignores the request to end, so that only killing the tree ends
// it.
func waitingAgent() []testkit.AgentStep {
	return []testkit.AgentStep{
		{Op: testkit.StepIgnoreInterrupt},
		{Op: testkit.StepSpawnChild},
		{Op: testkit.StepReadInput},
		{Op: testkit.StepReadInput},
		{Op: testkit.StepIdle, Duration: "1h"},
	}
}

func checkTreeEnded(t *testing.T, e *env) {
	t.Helper()
	for _, r := range e.runner.runsSoFar() {
		if r.proc.Escaped() {
			t.Error("a process of the tree survived")
		}
	}
}

// Task 14.10: a process that ends on its own reports started, its
// session id, its activity and usage, then exited with its exit code.
func TestExitedOnItsOwn(t *testing.T) {
	e := newEnv(ports.Capabilities{})
	_, rec := e.start(t, time.Time{},
		testkit.AgentStep{Op: testkit.StepPrint, Text: "working"},
		testkit.AgentStep{Op: testkit.StepUsage, Tokens: 1200, CostUSD: 0.25},
		testkit.AgentStep{Op: testkit.StepExit, Code: 3},
	)
	last := rec.waitEnd(t)
	if last.Kind != ports.ExecExited || last.Exit != (ports.Exit{Code: 3}) {
		t.Errorf("last event = %+v, want exited with code 3", last)
	}
	if last.Usage.Tokens == nil || *last.Usage.Tokens != 1200 {
		t.Errorf("final usage = %+v, want 1200 tokens", last.Usage)
	}
	all := rec.all()
	if all[0].Kind != ports.ExecStarted || !all[0].At.Equal(start) {
		t.Errorf("first event = %+v, want started at the clock's time", all[0])
	}
	var kinds []ports.ExecEventKind
	for _, ev := range all {
		if len(kinds) == 0 || kinds[len(kinds)-1] != ev.Kind {
			kinds = append(kinds, ev.Kind)
		}
	}
	want := []ports.ExecEventKind{ports.ExecStarted, ports.ExecActivity, ports.ExecSessionID, ports.ExecActivity, ports.ExecUsage, ports.ExecExited}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Errorf("event kinds = %v, want %v", kinds, want)
	}
	rec.waitActivity(t, "working")
	rec.waitFor(t, "the session id", func(ev ports.ExecEvent) bool {
		return ev.Kind == ports.ExecSessionID && strings.HasPrefix(ev.SessionID, "fake-")
	})
	rec.waitFor(t, "the usage", func(ev ports.ExecEvent) bool {
		return ev.Kind == ports.ExecUsage && ev.Usage.CostUSD != nil && *ev.Usage.CostUSD == 0.25
	})
}

// Task 14.10: at the deadline, the executor ends the whole process tree
// on its own timer, then reports ended at the deadline.
func TestDeadlineReached(t *testing.T) {
	e := newEnv(ports.Capabilities{LiveInput: true})
	_, rec := e.start(t, start.Add(time.Minute), waitingAgent()...)
	rec.waitActivity(t, `"child"`)
	e.clock.Advance(59 * time.Second)
	e.clock.Advance(time.Second)
	last := rec.waitEnd(t)
	if last.Kind != ports.ExecEndedAtDeadline || last.Exit != agentproc.StoppedExit {
		t.Errorf("last event = %+v, want ended at the deadline, killed", last)
	}
	checkTreeEnded(t, e)
}

// Task 14.10: a deadline moved later lets the process outlive the old
// one, and the new one ends it.
func TestDeadlineMovedLater(t *testing.T) {
	e := newEnv(ports.Capabilities{LiveInput: true})
	x, rec := e.start(t, start.Add(time.Minute), waitingAgent()...)
	rec.waitActivity(t, `"child"`)
	if err := e.exec.SetDeadline(x.ID, start.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Minute)
	stillRunning(t, e, x, rec, "after-the-old-deadline")
	e.clock.Advance(3 * time.Minute)
	if last := rec.waitEnd(t); last.Kind != ports.ExecEndedAtDeadline {
		t.Errorf("last event = %+v, want ended at the new deadline", last)
	}
	checkTreeEnded(t, e)
}

// Task 14.10: a deadline moved earlier, to a time already past, ends the
// process at once.
func TestDeadlineMovedEarlier(t *testing.T) {
	e := newEnv(ports.Capabilities{LiveInput: true})
	x, rec := e.start(t, start.Add(time.Hour), waitingAgent()...)
	rec.waitActivity(t, `"child"`)
	e.clock.Advance(time.Minute)
	if err := e.exec.SetDeadline(x.ID, start); err != nil {
		t.Fatal(err)
	}
	if last := rec.waitEnd(t); last.Kind != ports.ExecEndedAtDeadline {
		t.Errorf("last event = %+v, want ended at the deadline", last)
	}
}

// Task 14.10: a cleared deadline, as while request_extension waits,
// never ends the process; a new one does.
func TestDeadlineCleared(t *testing.T) {
	e := newEnv(ports.Capabilities{LiveInput: true})
	x, rec := e.start(t, start.Add(time.Minute), waitingAgent()...)
	rec.waitActivity(t, `"child"`)
	if err := e.exec.SetDeadline(x.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	stillRunning(t, e, x, rec, "while-cleared")
	if err := e.exec.SetDeadline(x.ID, e.clock.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if last := rec.waitEnd(t); last.Kind != ports.ExecEndedAtDeadline {
		t.Errorf("last event = %+v, want ended at the new deadline", last)
	}
}

// Task 14.10: Stop ends the whole tree after its grace and reports
// exited; the ended execution then refuses every request.
func TestStop(t *testing.T) {
	e := newEnv(ports.Capabilities{LiveInput: true})
	x, rec := e.start(t, start.Add(time.Hour), waitingAgent()...)
	rec.waitActivity(t, `"child"`)
	if err := e.exec.Stop(x.ID, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	last := rec.waitEnd(t)
	if last.Kind != ports.ExecExited || last.Exit != agentproc.StoppedExit {
		t.Errorf("last event = %+v, want exited, stopped", last)
	}
	checkTreeEnded(t, e)
	for name, err := range map[string]error{
		"send":     e.exec.Send(context.Background(), x.ID, "late"),
		"deadline": e.exec.SetDeadline(x.ID, start),
	} {
		if !errors.Is(err, ports.ErrUnknownExecution) {
			t.Errorf("%s after the end: %v, want ErrUnknownExecution", name, err)
		}
	}
	// A stop that comes after the process ended, as when it races the
	// exit, does not fail; an id the executor never gave does.
	if err := e.exec.Stop(x.ID, 0); err != nil {
		t.Errorf("stop after the end = %v, want nil", err)
	}
	for _, id := range []ports.ExecID{"exec-99", "other"} {
		if err := e.exec.Stop(id, 0); !errors.Is(err, ports.ErrUnknownExecution) {
			t.Errorf("stop of %s = %v, want ErrUnknownExecution", id, err)
		}
	}
}

// Task 14.10: live input reaches the process when the runner supports
// it; otherwise Send reports ErrUnsupported, so the message is queued.
func TestSend(t *testing.T) {
	e := newEnv(ports.Capabilities{LiveInput: true})
	x, rec := e.start(t, time.Time{}, testkit.AgentStep{Op: testkit.StepReadInput})
	if !x.Capabilities.LiveInput {
		t.Error("the execution does not show the runner's capabilities")
	}
	if err := e.exec.Send(context.Background(), x.ID, "Anthill notice: 12 minutes left."); err != nil {
		t.Fatal(err)
	}
	rec.waitActivity(t, "12 minutes left")
	if last := rec.waitEnd(t); last.Kind != ports.ExecExited {
		t.Errorf("last event = %+v", last)
	}

	e = newEnv(ports.Capabilities{})
	x, _ = e.start(t, time.Time{}, testkit.AgentStep{Op: testkit.StepIdle, Duration: "1h"})
	if err := e.exec.Send(context.Background(), x.ID, "hello"); !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("send without live input: %v, want ErrUnsupported", err)
	}
}

// Task 14.10: each execution reports its own activity.
func TestActivityPerExecution(t *testing.T) {
	e := newEnv(ports.Capabilities{})
	_, a := e.start(t, time.Time{}, testkit.AgentStep{Op: testkit.StepPrint, Text: "from-a"})
	_, b := e.start(t, time.Time{}, testkit.AgentStep{Op: testkit.StepPrint, Text: "from-b"})
	a.waitEnd(t)
	b.waitEnd(t)
	for name, c := range map[string]struct {
		rec        *recorder
		own, other string
	}{"a": {a, "from-a", "from-b"}, "b": {b, "from-b", "from-a"}} {
		c.rec.waitActivity(t, c.own)
		for _, ev := range c.rec.all() {
			if strings.Contains(ev.Line, c.other) {
				t.Errorf("execution %s reports the other's activity %q", name, ev.Line)
			}
		}
	}
}

// Task 14.10: a resume goes to the runner's Resume, and a runner that
// cannot resume refuses it.
func TestResume(t *testing.T) {
	e := newEnv(ports.Capabilities{Resume: true})
	brief, _ := json.Marshal(testkit.AgentScript{})
	x, err := e.exec.Start(context.Background(), ports.ExecSpec{
		Runner: fakeRunnerName, Session: "ses-1",
		Run: ports.RunSpec{Worktree: t.TempDir(), Brief: string(brief)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := record(x.Events)
	rec.waitEnd(t)
	if ev := rec.all()[1]; ev.Kind != ports.ExecSessionID || ev.SessionID != "ses-1" {
		t.Errorf("second event = %+v, want the resumed session", ev)
	}

	e = newEnv(ports.Capabilities{})
	_, err = e.exec.Start(context.Background(), ports.ExecSpec{Runner: fakeRunnerName, Session: "ses-1"})
	if !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("resume without the capability: %v, want ErrUnsupported", err)
	}
	if _, err := e.exec.Start(context.Background(), ports.ExecSpec{Runner: "nobody"}); err == nil {
		t.Error("an unknown runner started")
	}
}

// Task 14.10: when a reader stalls, the oldest activity lines are
// dropped beyond the limit, and one activity event says how many;
// lifecycle events are all kept.
func TestActivityDroppedForASlowReader(t *testing.T) {
	e := newEnvWith(ports.Capabilities{}, localexec.Options{MaxQueuedActivity: 3})
	var steps []testkit.AgentStep
	for i := 1; i <= 10; i++ {
		steps = append(steps, testkit.AgentStep{Op: testkit.StepPrint, Text: fmt.Sprintf("line-%d", i)})
	}
	steps = append(steps, testkit.AgentStep{Op: testkit.StepUsage, Tokens: 7})
	brief, _ := json.Marshal(testkit.AgentScript{Steps: steps})
	x, err := e.exec.Start(context.Background(), ports.ExecSpec{Runner: fakeRunnerName, Run: ports.RunSpec{Brief: string(brief)}})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing reads the events until the process has ended and all its
	// output was taken.
	e.runner.runsSoFar()[0].proc.Wait()
	rec := record(x.Events)
	if last := rec.waitEnd(t); last.Kind != ports.ExecExited {
		t.Errorf("last event = %+v", last)
	}
	var lines []string
	kinds := map[ports.ExecEventKind]int{}
	for _, ev := range rec.all() {
		kinds[ev.Kind]++
		if ev.Kind == ports.ExecActivity {
			lines = append(lines, ev.Line)
		}
	}
	// The process wrote 12 lines: start, 10 prints and usage. The last 3
	// wait, after one event for the 9 dropped.
	if len(lines) != 4 || lines[0] != localexec.DroppedActivity(9) || lines[1] != "line-9" || lines[2] != "line-10" || !strings.Contains(lines[3], `"usage"`) {
		t.Errorf("activity = %q, want the dropped count, line-9, line-10 and the usage line", lines)
	}
	if kinds[ports.ExecStarted] != 1 || kinds[ports.ExecSessionID] != 1 || kinds[ports.ExecUsage] != 1 || kinds[ports.ExecExited] != 1 {
		t.Errorf("lifecycle events = %v, want each once", kinds)
	}
}

// Task 14.10: a failed start runs nothing and sends no events; an
// unavailable runner's own error is returned.
func TestStartFails(t *testing.T) {
	e := newEnv(ports.Capabilities{})
	notInstalled := errors.New("fake: executable file not found")
	e.runner.unavailable = notInstalled
	if _, err := e.exec.Start(context.Background(), ports.ExecSpec{Runner: fakeRunnerName, Run: ports.RunSpec{Brief: "{}"}}); !errors.Is(err, notInstalled) {
		t.Errorf("start = %v, want the runner's error", err)
	}
	e.runner.unavailable = nil
	if _, err := e.exec.Start(context.Background(), ports.ExecSpec{Runner: fakeRunnerName, Run: ports.RunSpec{Brief: "not a script"}}); err == nil {
		t.Error("a start the runner refused succeeded")
	}
	if n := len(e.runner.runsSoFar()); n != 0 {
		t.Errorf("%d processes started", n)
	}
	// The failed starts used no id.
	if err := e.exec.Stop("exec-1", 0); !errors.Is(err, ports.ErrUnknownExecution) {
		t.Errorf("stop of exec-1 = %v, want ErrUnknownExecution", err)
	}
}

// Task 14.10: a short call, such as a comment classifier call, starts
// from a spec with only a brief when the runner allows it.
func TestShortCall(t *testing.T) {
	e := newEnv(ports.Capabilities{})
	brief, _ := json.Marshal(testkit.AgentScript{Steps: []testkit.AgentStep{{Op: testkit.StepPrint, Text: "informational"}}})
	x, err := e.exec.Start(context.Background(), ports.ExecSpec{Runner: fakeRunnerName, Run: ports.RunSpec{Brief: string(brief)}})
	if err != nil {
		t.Fatal(err)
	}
	rec := record(x.Events)
	rec.waitActivity(t, "informational")
	if last := rec.waitEnd(t); last.Kind != ports.ExecExited || last.Exit != (ports.Exit{}) {
		t.Errorf("last event = %+v", last)
	}
}
