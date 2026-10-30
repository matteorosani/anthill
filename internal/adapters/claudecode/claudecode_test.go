package claudecode_test

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/claudecode"
	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/ports"
)

// fixtures are recorded Claude Code outputs: the turns of a run, in the
// order the fake claude prints them, and hook inputs.
//
//go:embed testdata
var fixtures embed.FS

// fakeClaudeVar, set in the agent's environment, makes the test binary
// play claude: it records its arguments, configuration files,
// environment and input in that directory, and after its n-th input line
// prints turn-<n>.jsonl from there. It exits when its input ends, with
// the code in exit-code if that file exists.
const fakeClaudeVar = "ANTHILL_FAKE_CLAUDE"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeClaudeVar); dir != "" {
		os.Exit(fakeClaude(dir))
	}
	os.Exit(m.Run())
}

func fakeClaude(dir string) int {
	args := os.Args[1:]
	record := func(name string, v any) {
		b, _ := json.Marshal(v)
		_ = os.WriteFile(filepath.Join(dir, name), b, 0o600)
	}
	record("args.json", args)
	record("env.json", os.Environ())
	for i, a := range args {
		if (a == "--mcp-config" || a == "--settings") && i+1 < len(args) {
			b, _ := os.ReadFile(args[i+1])
			_ = os.WriteFile(filepath.Join(dir, strings.TrimPrefix(a, "--")+".json"), b, 0o600)
		}
	}
	in := bufio.NewScanner(os.Stdin)
	for n := 1; in.Scan(); n++ {
		f, _ := os.OpenFile(filepath.Join(dir, "inputs.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		_, _ = fmt.Fprintln(f, in.Text())
		_ = f.Close()
		if b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("turn-%d.jsonl", n))); err == nil {
			_, _ = os.Stdout.Write(b)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "exit-code")); err == nil {
		var code int
		_, _ = fmt.Sscan(string(b), &code)
		return code
	}
	return 0
}

// fake is one fake claude: its directory and the runner that starts it.
type fake struct {
	dir    string
	runner *claudecode.Runner
}

// newFake copies the turns of the recorded run name into a new
// directory for the fake claude.
func newFake(t *testing.T, name string) *fake {
	t.Helper()
	dir := t.TempDir()
	if name != "" {
		entries, err := fs.ReadDir(fixtures, "testdata/"+name)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := fixtures.ReadFile("testdata/" + name + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := claudecode.New(claudecode.Options{Anthill: "/opt/anthill/bin/anthill", Command: exe})
	if err != nil {
		t.Fatal(err)
	}
	return &fake{dir: dir, runner: runner}
}

func (f *fake) spec(t *testing.T) ports.RunSpec {
	return ports.RunSpec{
		Worktree:   t.TempDir(),
		Brief:      "Fix the login timeout (gh-12).",
		Budget:     time.Hour,
		MCPCommand: []string{"/opt/anthill/bin/anthill", "mcp", "--ant", "ant_tok3n"},
		ToolToken:  "ant_tok3n",
		Env:        append(os.Environ(), fakeClaudeVar+"="+f.dir, "OTEL_LOG_USER_PROMPTS=1"),
	}
}

func (f *fake) read(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// inputs are the messages the fake claude read, as text.
func (f *fake) inputs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "inputs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type, Text string
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("input %q: %v", line, err)
		}
		if m.Type != "user" || m.Message.Role != "user" || len(m.Message.Content) != 1 || m.Message.Content[0].Type != "text" {
			t.Fatalf("input %q is not a user text message", line)
		}
		texts = append(texts, m.Message.Content[0].Text)
	}
	return texts
}

func waitSession(t *testing.T, run ports.Run) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for run.SessionID() == "" {
		if time.Now().After(deadline) {
			t.Fatal("no session id")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const recordedSession = "7f3c2a9e-5d1b-4c8e-9a6f-2b1d0e4c3a58"

// Task 13.3: a run starts claude headless with streaming JSON, the ant
// tools server and the hooks, gives it the brief and live input, captures
// the session id and adds up the usage of its results. When every input
// has its result, the input closes and claude exits.
func TestStartRunsHeadlessWithLiveInput(t *testing.T) {
	f := newFake(t, "start")
	run, err := f.runner.Start(context.Background(), f.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, run)
	if got := run.SessionID(); got != recordedSession {
		t.Errorf("session = %q, want %q", got, recordedSession)
	}
	if err := run.Send(context.Background(), "Anthill notice: 12 minutes left."); err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != (ports.Exit{}) {
		t.Errorf("exit = %+v", exit)
	}
	if got, want := f.inputs(t), []string{"Fix the login timeout (gh-12).", "Anthill notice: 12 minutes left."}; !slices.Equal(got, want) {
		t.Errorf("inputs = %q, want %q", got, want)
	}
	u := run.Usage()
	if u.Tokens == nil || *u.Tokens != 17510+17424 {
		t.Errorf("tokens = %v, want the sum of both results, cached ones included", u.Tokens)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.0452 {
		t.Errorf("cost = %v, want the last total", u.CostUSD)
	}
	if err := run.Send(context.Background(), "late"); err == nil {
		t.Error("Send after the run ended succeeded")
	}

	var args []string
	f.read(t, "args.json", &args)
	for _, want := range [][]string{
		{"-p"},
		{"--input-format", "stream-json"},
		{"--output-format", "stream-json"},
		{"--verbose"},
		{"--strict-mcp-config"},
		{"--permission-mode", "acceptEdits"},
		{"--allowedTools", "Bash,Edit,Write,Read,Glob,Grep,mcp__anthill"},
	} {
		if !containsSeq(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if slices.Contains(args, "--resume") || slices.Contains(args, "Fix the login timeout (gh-12).") {
		t.Errorf("args %q: a new session must not resume, and the brief goes on standard input", args)
	}

	var mcp struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	f.read(t, "mcp-config.json", &mcp)
	srv, ok := mcp.MCPServers["anthill"]
	if len(mcp.MCPServers) != 1 || !ok || srv.Type != "stdio" || srv.Command != "/opt/anthill/bin/anthill" ||
		!slices.Equal(srv.Args, []string{"mcp", "--ant", "ant_tok3n"}) {
		t.Errorf("mcp config = %+v", mcp)
	}

	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	f.read(t, "settings.json", &settings)
	pre, post := settings.Hooks["PreToolUse"], settings.Hooks["PostToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "Bash" || pre[0].Hooks[0].Command != `"/opt/anthill/bin/anthill" hook pre-tool-use` {
		t.Errorf("PreToolUse = %+v", pre)
	}
	if len(post) != 1 || post[0].Matcher != "*" || post[0].Hooks[0].Command != `"/opt/anthill/bin/anthill" hook post-tool-use --ant "ant_tok3n"` {
		t.Errorf("PostToolUse = %+v", post)
	}

	tmp := filepath.Dir(firstArgAfter(args, "--settings"))
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("the run's configuration %s is still there after Wait: %v", tmp, err)
	}
}

// Task 13.3: resuming passes the session id back, and the input of the
// resumed session is the first message.
func TestResumeContinuesTheSession(t *testing.T) {
	f := newFake(t, "resume")
	spec := f.spec(t)
	spec.Brief = "Answer to your question: 60 seconds."
	run, err := f.runner.Resume(context.Background(), recordedSession, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := run.SessionID(); got != recordedSession {
		t.Errorf("session before the init event = %q, want the resumed one", got)
	}
	if exit := run.Wait(); exit != (ports.Exit{}) {
		t.Errorf("exit = %+v", exit)
	}
	var args []string
	f.read(t, "args.json", &args)
	if !containsSeq(args, []string{"--resume", recordedSession}) {
		t.Errorf("args %q lack --resume %s", args, recordedSession)
	}
	if got := f.inputs(t); !slices.Equal(got, []string{spec.Brief}) {
		t.Errorf("inputs = %q", got)
	}
	if u := run.Usage(); u.Tokens == nil || *u.Tokens != 21986 || u.CostUSD == nil || *u.CostUSD != 0.0215 {
		t.Errorf("usage = %v tokens, %v USD", u.Tokens, u.CostUSD)
	}
	if _, err := f.runner.Resume(context.Background(), "", spec); err == nil {
		t.Error("Resume without a session id succeeded")
	}
}

// A failed run reports its exit code; lines that are not JSON are
// skipped.
func TestFailedRun(t *testing.T) {
	f := newFake(t, "failed")
	if err := os.WriteFile(filepath.Join(f.dir, "exit-code"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := f.runner.Start(context.Background(), f.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != (ports.Exit{Code: 1}) {
		t.Errorf("exit = %+v, want code 1", exit)
	}
	if got := run.SessionID(); got != recordedSession {
		t.Errorf("session = %q", got)
	}
	if u := run.Usage(); u.Tokens == nil || *u.Tokens != 0 {
		t.Errorf("tokens = %v, want a reported 0", u.Tokens)
	}
}

// Task 13.3: Stop ends a run that is still working, and its usage stays
// unknown when it reported none.
func TestStopAndUnknownUsage(t *testing.T) {
	f := newFake(t, "")
	run, err := f.runner.Start(context.Background(), f.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Stop(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != agentproc.StoppedExit {
		t.Errorf("exit = %+v", exit)
	}
	if u := run.Usage(); u.Tokens != nil || u.CostUSD != nil {
		t.Errorf("usage = %+v, want unknown", u)
	}
	if run.SessionID() != "" {
		t.Errorf("session = %q, want none reported", run.SessionID())
	}
}

// Task 13.3: with export on, the agent gets Claude Code's OpenTelemetry
// variables, linked to the run; prompt and tool detail logging are off
// whether export is on or not.
func TestTelemetryVariables(t *testing.T) {
	f := newFake(t, "resume")
	spec := f.spec(t)
	spec.Telemetry = &ports.AgentTelemetry{
		Endpoint:           "https://otel.example.com:4318",
		Protocol:           "http/protobuf",
		Headers:            map[string]string{"Authorization": "Bearer s3cret"},
		ResourceAttributes: map[string]string{"anthill.item": "gh-12", "anthill.ant_id": "7", "anthill.instance": "mrosani@LAPTOP-42"},
		TraceParent:        "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	run, err := f.runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	run.Wait()
	env := f.env(t)
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT=https://otel.example.com:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20s3cret",
		"OTEL_RESOURCE_ATTRIBUTES=anthill.ant_id=7,anthill.instance=mrosani@LAPTOP-42,anthill.item=gh-12",
		"TRACEPARENT=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"OTEL_LOG_USER_PROMPTS=0",
		"OTEL_LOG_TOOL_DETAILS=0",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
	if slices.Contains(env, "OTEL_LOG_USER_PROMPTS=1") {
		t.Error("the developer's OTEL_LOG_USER_PROMPTS=1 was kept")
	}

	f = newFake(t, "resume")
	run, err = f.runner.Start(context.Background(), f.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	run.Wait()
	env = f.env(t)
	if !slices.Contains(env, "OTEL_LOG_USER_PROMPTS=0") || !slices.Contains(env, "OTEL_LOG_TOOL_DETAILS=0") {
		t.Error("prompt logging is not off without export")
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CODE_ENABLE_TELEMETRY=") || strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_") {
			t.Errorf("export variable %s set while export is off", kv)
		}
	}
}

func (f *fake) env(t *testing.T) []string {
	var env []string
	f.read(t, "env.json", &env)
	return env
}

// Task 13.6: the CLI is available when found on the PATH, or at its
// path.
func TestAvailable(t *testing.T) {
	f := newFake(t, "")
	if err := f.runner.Available(context.Background()); err != nil {
		t.Errorf("Available = %v", err)
	}
	missing, err := claudecode.New(claudecode.Options{Anthill: "anthill", Command: "anthill-no-such-claude"})
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.Available(context.Background()); err == nil {
		t.Error("a missing CLI is available")
	}
	if _, err := claudecode.New(claudecode.Options{}); err == nil {
		t.Error("New without the anthill program succeeded")
	}
	caps := f.runner.Capabilities()
	if !caps.Resume || !caps.LiveInput || !caps.Hooks || !caps.Usage {
		t.Errorf("capabilities = %+v, want all", caps)
	}
}

// Spec ant-tools, "Agent tries gh": the PreToolUse hook refuses gh pr
// create and git push with the tool to use, and lets other commands and
// tools through. Inputs are recorded hook inputs.
func TestPreToolUse(t *testing.T) {
	tests := []struct {
		fixture string
		want    string // in the message; empty: not refused
	}{
		{"pre_tool_use_gh_pr_create.json", "call the open_pull_request tool"},
		{"pre_tool_use_git_push.json", "call the push tool"},
		{"pre_tool_use_go_test.json", ""},
		{"pre_tool_use_edit.json", ""},
	}
	for _, tt := range tests {
		b, err := fixtures.ReadFile("testdata/hooks/" + tt.fixture)
		if err != nil {
			t.Fatal(err)
		}
		msg, refused := claudecode.PreToolUse(b)
		if refused != (tt.want != "") || !strings.Contains(msg, tt.want) {
			t.Errorf("%s: refused %v, message %q; want %q", tt.fixture, refused, msg, tt.want)
		}
	}
	if _, refused := claudecode.PreToolUse([]byte("not json")); refused {
		t.Error("an unreadable input was refused")
	}
}

// The PostToolUse hook adds the notices as context, or prints nothing.
func TestPostToolUseOutput(t *testing.T) {
	if out := claudecode.PostToolUseOutput(nil); out != nil {
		t.Errorf("output without notices = %q", out)
	}
	out := claudecode.PostToolUseOutput([]string{"Anthill notice: 12 minutes left.", "Anthill notice: extension approved."})
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.HookSpecificOutput.HookEventName != "PostToolUse" ||
		got.HookSpecificOutput.AdditionalContext != "Anthill notice: 12 minutes left.\nAnthill notice: extension approved." {
		t.Errorf("output = %s", out)
	}
}

func containsSeq(args, seq []string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

func firstArgAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
