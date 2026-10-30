package agentproc_test

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestMain(m *testing.M) {
	testkit.RunFakeAgentIfRequested()
	os.Exit(m.Run())
}

// events collects the fake agent's events from its output lines.
type events struct {
	mu   sync.Mutex
	list []testkit.AgentEvent
}

func (e *events) line(b []byte) {
	var ev testkit.AgentEvent
	if json.Unmarshal(b, &ev) != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var names []string
	for _, ev := range e.list {
		names = append(names, ev.Event)
	}
	return names
}

// waitFor waits until the agent printed the event.
func (e *events) waitFor(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !slices.Contains(e.names(), name) {
		if time.Now().After(deadline) {
			t.Fatalf("no %s event; got %v", name, e.names())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// start starts the fake agent following steps.
func start(t *testing.T, stdin bool, steps ...testkit.AgentStep) (*agentproc.Process, *events) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ev := &events{}
	var stderr bytes.Buffer
	p, err := agentproc.Start(agentproc.Spec{
		Path:   exe,
		Args:   testkit.FakeAgentArgs(testkit.AgentScript{Steps: steps}),
		Dir:    t.TempDir(),
		Env:    os.Environ(),
		Stdin:  stdin,
		Line:   ev.line,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Stop(0)
		if t.Failed() {
			t.Logf("stderr: %s", stderr.String())
		}
	})
	return p, ev
}

// Task 13.1: a process that ends on its own reports its exit code and
// its output, line by line.
func TestExitCodeAndOutput(t *testing.T) {
	p, ev := start(t, false,
		testkit.AgentStep{Op: testkit.StepUsage, Tokens: 7},
		testkit.AgentStep{Op: testkit.StepExit, Code: 3},
	)
	if got := p.Wait(); got != (ports.Exit{Code: 3}) {
		t.Errorf("exit = %+v, want code 3", got)
	}
	if got := ev.names(); !slices.Equal(got, []string{"start", "usage"}) {
		t.Errorf("events = %v", got)
	}
	if p.Escaped() {
		t.Error("Escaped is set for a process that started nothing")
	}
}

// Task 13.1: a crash is reported as such.
func TestCrash(t *testing.T) {
	p, _ := start(t, false, testkit.AgentStep{Op: testkit.StepCrash})
	if got := p.Wait(); !got.Crashed {
		t.Errorf("exit = %+v, want a crash", got)
	}
}

// Task 13.1: stopping an agent that ignores the request to end kills
// it, and the process it started, after the grace period. No process of
// the tree still holds the output when Wait returns.
func TestStopKillsTheWholeTreeAfterGrace(t *testing.T) {
	p, ev := start(t, false,
		testkit.AgentStep{Op: testkit.StepIgnoreInterrupt},
		testkit.AgentStep{Op: testkit.StepSpawnChild},
		testkit.AgentStep{Op: testkit.StepIdle, Duration: "1h"},
	)
	ev.waitFor(t, "child")
	const grace = 300 * time.Millisecond
	begin := time.Now()
	if err := p.Stop(grace); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed < grace {
		t.Errorf("Stop returned after %v, before the grace period", elapsed)
	}
	if got := p.Wait(); got != agentproc.StoppedExit {
		t.Errorf("exit = %+v, want %+v", got, agentproc.StoppedExit)
	}
	if p.Escaped() {
		t.Error("a child process survived Stop")
	}
}

// Task 13.1: an agent that ends on its own takes the processes it
// started with it.
func TestExitKillsWhatTheAgentStarted(t *testing.T) {
	p, _ := start(t, false,
		testkit.AgentStep{Op: testkit.StepSpawnChild},
		testkit.AgentStep{Op: testkit.StepExit, Code: 0},
	)
	if got := p.Wait(); got != (ports.Exit{}) {
		t.Errorf("exit = %+v, want code 0", got)
	}
	if p.Escaped() {
		t.Error("a child process survived the agent")
	}
}

// Task 13.1: an agent that ends when asked does so within the grace
// period, so it can save its session. Only on Unix: on Windows,
// CTRL_BREAK reaches the agent only when it shares Anthill's console.
func TestStopAsksTheAgentToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CTRL_BREAK needs a shared console")
	}
	p, ev := start(t, false,
		testkit.AgentStep{Op: testkit.StepExitOnInterrupt, Code: 0},
		testkit.AgentStep{Op: testkit.StepSpawnChild},
		testkit.AgentStep{Op: testkit.StepIdle, Duration: "1h"},
	)
	ev.waitFor(t, "child")
	begin := time.Now()
	if err := p.Stop(time.Minute); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > 30*time.Second {
		t.Errorf("Stop took %v: the agent did not end when asked", elapsed)
	}
	if !slices.Contains(ev.names(), "interrupted") {
		t.Errorf("events = %v, want interrupted", ev.names())
	}
	if got := p.Wait(); got != agentproc.StoppedExit {
		t.Errorf("exit = %+v, want %+v", got, agentproc.StoppedExit)
	}
	if p.Escaped() {
		t.Error("a child process survived Stop")
	}
}

// Stopping an ended process changes nothing.
func TestStopAfterExit(t *testing.T) {
	p, _ := start(t, false, testkit.AgentStep{Op: testkit.StepExit, Code: 4})
	p.Wait()
	if err := p.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	if got := p.Wait(); got != (ports.Exit{Code: 4}) {
		t.Errorf("exit = %+v, want code 4", got)
	}
}

// Task 13.1: input written to the process arrives line by line, and
// writing fails once the input is closed.
func TestWriteAndCloseInput(t *testing.T) {
	p, ev := start(t, true,
		testkit.AgentStep{Op: testkit.StepReadInput},
		testkit.AgentStep{Op: testkit.StepReadInput},
	)
	if err := p.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := p.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := p.CloseInput(); err != nil {
		t.Fatal(err)
	}
	if err := p.Write([]byte("third\n")); err == nil {
		t.Error("Write after CloseInput succeeded")
	}
	if got := p.Wait(); got != (ports.Exit{}) {
		t.Errorf("exit = %+v", got)
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	var inputs []string
	for _, e := range ev.list {
		if e.Event == "input" {
			inputs = append(inputs, e.Text)
		}
	}
	if want := []string{"first", "second"}; !slices.Equal(inputs, want) {
		t.Errorf("inputs = %q, want %q", inputs, want)
	}
}

// Without Spec.Stdin, the process reads an empty input and Write fails.
func TestNoInput(t *testing.T) {
	p, ev := start(t, false, testkit.AgentStep{Op: testkit.StepReadInput})
	if err := p.Write([]byte("x\n")); err == nil {
		t.Error("Write without Stdin succeeded")
	}
	if got := p.Wait(); got.Code != 2 {
		t.Errorf("exit = %+v, want 2 for the failed read", got)
	}
	if !slices.Contains(ev.names(), "error") {
		t.Errorf("events = %v", ev.names())
	}
}

// A program that does not exist fails Start.
func TestStartFails(t *testing.T) {
	_, err := agentproc.Start(agentproc.Spec{Path: "anthill-no-such-agent", Dir: t.TempDir()})
	if err == nil {
		t.Fatal("Start succeeded")
	}
}

// Lines end at \n or \r\n; empty lines and lines over the limit are
// dropped, and the lines after them still arrive.
func TestLines(t *testing.T) {
	const limit = 200 << 10
	long := strings.Repeat("x", limit)
	input := "one\r\n\n" + long + "\ntwo\n" + long[:limit-1] + "\nthree"
	var got []string
	agentproc.ReadLines(strings.NewReader(input), limit, func(b []byte) { got = append(got, string(b)) })
	if len(got) == 4 && len(got[2]) == limit-1 {
		got[2] = "long"
	}
	if want := []string{"one", "two", "long", "three"}; !slices.Equal(got, want) {
		t.Errorf("lines = %.40q, want %q", got, want)
	}
}
