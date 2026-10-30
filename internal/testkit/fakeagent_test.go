package testkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matteorosani/anthill/internal/testkit"
)

// echoServerFlag runs the test binary as a small MCP server with one
// tool, echo, standing in for anthill mcp.
const echoServerFlag = "-testkit-echo-mcp"

func TestMain(m *testing.M) {
	testkit.RunFakeAgentIfRequested()
	if len(os.Args) > 1 && os.Args[1] == echoServerFlag {
		os.Exit(serveEcho())
	}
	os.Exit(m.Run())
}

func serveEcho() int {
	server := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "1"}, nil)
	schema := json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: schema}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct{ Text string }
		_ = json.Unmarshal(req.Params.Arguments, &args)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Text}}}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return 1
	}
	return 0
}

// runFakeAgent runs the fake agent in dir, with no standard input and no
// terminal, and returns its events and how it ended.
func runFakeAgent(t *testing.T, dir string, script testkit.AgentScript) ([]testkit.AgentEvent, *os.ProcessState) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, testkit.FakeAgentArgs(script)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	var events []testkit.AgentEvent
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var e testkit.AgentEvent
		if json.Unmarshal([]byte(line), &e) == nil && e.Event != "" {
			events = append(events, e)
		}
	}
	if t.Failed() || stderr.Len() > 0 {
		t.Logf("stderr: %s", stderr.String())
	}
	return events, cmd.ProcessState
}

func eventNames(events []testkit.AgentEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.Event
	}
	return names
}

// gitRepo returns a new repository with a fixed identity, and makes git
// read no configuration of the machine.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, kv := range [][2]string{
		{"GIT_AUTHOR_NAME", "Ann"}, {"GIT_AUTHOR_EMAIL", "ann@example.com"},
		{"GIT_COMMITTER_NAME", "Ann"}, {"GIT_COMMITTER_EMAIL", "ann@example.com"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	repo := filepath.Join(dir, "repo")
	if out, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return repo
}

// Task 13.2: the fake agent runs headless, calls a tool over MCP,
// commits, prints usage and exits with the scripted code.
func TestFakeAgentFollowsItsScript(t *testing.T) {
	repo := gitRepo(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	events, state := runFakeAgent(t, repo, testkit.AgentScript{
		MCPCommand: []string{exe, echoServerFlag},
		Steps: []testkit.AgentStep{
			{Op: testkit.StepTool, Tool: "echo", Args: json.RawMessage(`{"text":"hi"}`)},
			{Op: testkit.StepCommit, File: "notes.txt", Text: "done", Message: "Add notes"},
			{Op: testkit.StepUsage, Tokens: 1200, CostUSD: 0.5},
			{Op: testkit.StepExit, Code: 3},
		},
	})
	if got, want := eventNames(events), []string{"start", "tool", "commit", "usage"}; !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if e := events[1]; e.Tool != "echo" || e.Text != "echo: hi" || e.IsError {
		t.Errorf("tool event = %+v", e)
	}
	if e := events[3]; e.Tokens != 1200 || e.CostUSD != 0.5 {
		t.Errorf("usage event = %+v", e)
	}
	if code := state.ExitCode(); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	out, err := exec.Command("git", "-C", repo, "log", "--format=%s").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "Add notes" {
		t.Errorf("git log = %q, want the agent's commit", got)
	}
}

// Without an MCP command, a tool step fails and the agent exits with 2.
func TestFakeAgentToolWithoutServer(t *testing.T) {
	events, state := runFakeAgent(t, t.TempDir(), testkit.AgentScript{
		Steps: []testkit.AgentStep{{Op: testkit.StepTool, Tool: "echo"}},
	})
	if got := eventNames(events); !slices.Equal(got, []string{"start", "error"}) {
		t.Fatalf("events = %v", got)
	}
	if code := state.ExitCode(); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

// Task 13.2: a crash ends the agent abnormally, unlike an exit.
func TestFakeAgentCrashes(t *testing.T) {
	_, state := runFakeAgent(t, t.TempDir(), testkit.AgentScript{
		Steps: []testkit.AgentStep{{Op: testkit.StepCrash}},
	})
	if runtime.GOOS == "windows" {
		if code := uint32(state.ExitCode()); code != 0xC0000005 {
			t.Errorf("exit code = %#x, want an access violation", code)
		}
		return
	}
	if state.Exited() {
		t.Errorf("the agent exited with %d, want killed by a signal", state.ExitCode())
	}
}

// The fake agent reads its input line by line, as live input.
func TestFakeAgentReadsInput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, testkit.FakeAgentArgs(testkit.AgentScript{
		Steps: []testkit.AgentStep{{Op: testkit.StepReadInput}, {Op: testkit.StepReadInput}},
	})...)
	cmd.Stdin = strings.NewReader("first\r\nsecond")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var e testkit.AgentEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Event == "input" {
			texts = append(texts, e.Text)
		}
	}
	if want := []string{"first", "second"}; !slices.Equal(texts, want) {
		t.Errorf("inputs = %q, want %q", texts, want)
	}
}
