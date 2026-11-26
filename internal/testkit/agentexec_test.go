package testkit_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// drain reads an execution's events until the last one.
func drain(t *testing.T, x ports.Execution) []ports.ExecEvent {
	t.Helper()
	var out []ports.ExecEvent
	timeout := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-x.Events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("execution %s did not end; events so far: %+v", x.ID, out)
		}
	}
}

func newAgentExecutor(t *testing.T, reply func(ports.ExecSpec) (string, bool)) *testkit.AgentExecutor {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Not t.TempDir: on Windows, removing it fails while a killed agent's
	// MCP server is still exiting there.
	dir, err := os.MkdirTemp("", "agentexec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	x := testkit.NewAgentExecutor(
		testkit.NewExecutor(map[domain.Runner]ports.Capabilities{domain.RunnerClaudeCode: {Resume: true}}),
		testkit.AgentExecutorOptions{
			Dir:   dir,
			MCP:   func(string) []string { return []string{exe, echoServerFlag} },
			Reply: reply,
		})
	t.Cleanup(x.Close)
	return x
}

// An execution with a tool token runs the next queued fake agent, whose
// tool call reaches the MCP server, and ends with the agent's exit code;
// it reports its session, and Prepare may change the script first.
func TestAgentExecutorRunsTheFakeAgent(t *testing.T) {
	x := newAgentExecutor(t, nil)
	var prepared ports.ExecSpec
	x.Queue(testkit.AgentRun{
		Script: testkit.AgentScript{Steps: []testkit.AgentStep{{Op: testkit.StepExit, Code: 3}}},
		Prepare: func(spec ports.ExecSpec, script *testkit.AgentScript) {
			prepared = spec
			script.Steps = append([]testkit.AgentStep{
				{Op: testkit.StepTool, Tool: "echo", Args: json.RawMessage(`{"text":"hi"}`)},
			}, script.Steps...)
		},
	})
	spec := ports.ExecSpec{Runner: domain.RunnerClaudeCode, Session: "s-1", Run: ports.RunSpec{ToolToken: "ant_1", Worktree: "/w"}}
	ex, err := x.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, ex)
	if prepared.Run.Worktree != "/w" {
		t.Errorf("Prepare got %+v", prepared)
	}
	if len(events) != 3 || events[0].Kind != ports.ExecStarted ||
		events[1].Kind != ports.ExecSessionID || events[1].SessionID != "s-1" ||
		events[2].Kind != ports.ExecExited || events[2].Exit.Code != 3 {
		t.Errorf("events = %+v", events)
	}
	if out := x.Output(ex.ID); !strings.Contains(out, `"text":"echo: hi"`) {
		t.Errorf("agent output = %q, want the tool result", out)
	}
	if x.Queued() != 0 || x.Running() != 0 {
		t.Errorf("queued %d, running %d", x.Queued(), x.Running())
	}
}

// Without a queued run, or with a held one, an execution stays active
// until it is stopped; a new session is named after the execution.
func TestAgentExecutorHoldsAndStops(t *testing.T) {
	x := newAgentExecutor(t, nil)
	x.Queue(testkit.AgentRun{Hold: true})
	for range 2 {
		ex, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerClaudeCode, Run: ports.RunSpec{ToolToken: "ant_1"}})
		if err != nil {
			t.Fatal(err)
		}
		f := x.Execution(ex.ID)
		if f.Ended() {
			t.Fatal("a held execution ended")
		}
		if err := x.Stop(ex.ID, time.Second); err != nil {
			t.Fatal(err)
		}
		events := drain(t, ex)
		if events[1].SessionID != "session-"+string(ex.ID) || events[len(events)-1].Exit != testkit.StoppedExit {
			t.Errorf("events = %+v", events)
		}
	}
}

// Stop kills a running fake agent.
func TestAgentExecutorStopKillsTheAgent(t *testing.T) {
	x := newAgentExecutor(t, nil)
	x.Queue(testkit.AgentRun{Script: testkit.AgentScript{Steps: []testkit.AgentStep{{Op: testkit.StepIdle, Duration: "1h"}}}})
	ex, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerClaudeCode, Run: ports.RunSpec{ToolToken: "ant_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if x.Running() != 1 {
		t.Fatalf("running = %d, want 1", x.Running())
	}
	if err := x.Stop(ex.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if events := drain(t, ex); events[len(events)-1].Exit != testkit.StoppedExit {
		t.Errorf("events = %+v", events)
	}
	x.Close()
	if x.Running() != 0 {
		t.Errorf("running after Close = %d", x.Running())
	}
}

// An execution without a tool token, such as a classifier call, gets
// Reply's answer, or stays active when Reply declines.
func TestAgentExecutorReplies(t *testing.T) {
	x := newAgentExecutor(t, func(spec ports.ExecSpec) (string, bool) {
		return "actionable", spec.Run.Brief == "classify"
	})
	ex, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerClaudeCode, Run: ports.RunSpec{Brief: "classify"}})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, ex)
	if len(events) != 3 || events[1].Message != "actionable" || events[2].Exit.Code != 0 {
		t.Errorf("events = %+v", events)
	}
	held, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerClaudeCode, Run: ports.RunSpec{Brief: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if x.Execution(held.ID).Ended() {
		t.Error("a declined call ended")
	}
}

// Close kills the agents and leaves their executions active, as when the
// daemon's process ends.
func TestAgentExecutorCloseAbandons(t *testing.T) {
	x := newAgentExecutor(t, nil)
	x.Queue(testkit.AgentRun{Script: testkit.AgentScript{Steps: []testkit.AgentStep{{Op: testkit.StepIdle, Duration: "1h"}}}})
	ex, err := x.Start(context.Background(), ports.ExecSpec{Runner: domain.RunnerClaudeCode, Run: ports.RunSpec{ToolToken: "ant_1"}})
	if err != nil {
		t.Fatal(err)
	}
	x.Close()
	if x.Running() != 0 || x.Execution(ex.ID).Ended() {
		t.Errorf("after Close: running %d, ended %v; want no process and the execution active", x.Running(), x.Execution(ex.ID).Ended())
	}
}
