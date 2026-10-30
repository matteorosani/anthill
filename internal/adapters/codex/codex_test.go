package codex_test

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/codex"
	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/ports"
)

// fixtures are recorded outputs of codex exec --json.
//
//go:embed testdata
var fixtures embed.FS

// fakeCodexVar, set in the agent's environment, makes the test binary
// play codex: it records its arguments, environment and input in that
// directory, prints output.jsonl from there once its input ends, and
// exits with the code in exit-code, if any. With a file named block, it
// keeps running instead.
const fakeCodexVar = "ANTHILL_FAKE_CODEX"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeCodexVar); dir != "" {
		os.Exit(fakeCodex(dir))
	}
	os.Exit(m.Run())
}

func fakeCodex(dir string) int {
	write := func(name string, b []byte) { _ = os.WriteFile(filepath.Join(dir, name), b, 0o600) }
	args, _ := json.Marshal(os.Args[1:])
	write("args.json", args)
	env, _ := json.Marshal(os.Environ())
	write("env.json", env)
	if _, err := os.Stat(filepath.Join(dir, "block")); err == nil {
		time.Sleep(time.Hour)
	}
	input, _ := io.ReadAll(os.Stdin)
	write("input.txt", input)
	if b, err := os.ReadFile(filepath.Join(dir, "output.jsonl")); err == nil {
		_, _ = os.Stdout.Write(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "exit-code")); err == nil {
		var code int
		_, _ = fmt.Sscan(string(b), &code)
		return code
	}
	return 0
}

// worktree returns a linked worktree of a new repository, and the
// repository's git directory.
func worktree(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.com",
			"GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	repo, wt := filepath.Join(dir, "repo"), filepath.Join(dir, "wt")
	git("init", "--quiet", repo)
	git("-C", repo, "commit", "--quiet", "--allow-empty", "-m", "init")
	git("-C", repo, "worktree", "add", "--quiet", "-b", "ant/gh-12", wt)
	gitDir, err := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	return wt, gitDir
}

type fake struct {
	dir    string
	runner *codex.Runner
}

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
	return &fake{dir: dir, runner: codex.New(codex.Options{Command: exe})}
}

func (f *fake) spec(wt string) ports.RunSpec {
	return ports.RunSpec{
		Worktree:   wt,
		Brief:      "Fix the login timeout (gh-12).\nSay \"done\" when finished.",
		Budget:     time.Hour,
		MCPCommand: []string{"/opt/anthill/bin/anthill", "mcp", "--ant", "ant_tok3n"},
		ToolToken:  "ant_tok3n",
		Env:        append(os.Environ(), fakeCodexVar+"="+f.dir),
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

const recordedThread = "0199a213-81c0-7800-8aa1-bbab2a035a53"

// Task 13.4: a run starts codex exec with JSON events, the sandbox, the
// git directory writable, only the ant tools server and the brief on
// standard input; it captures the thread id and the turn's tokens.
func TestStart(t *testing.T) {
	wt, gitDir := worktree(t)
	f := newFake(t, "start")
	spec := f.spec(wt)
	run, err := f.runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != (ports.Exit{}) {
		t.Errorf("exit = %+v", exit)
	}
	if got := run.SessionID(); got != recordedThread {
		t.Errorf("session = %q, want %q", got, recordedThread)
	}
	if u := run.Usage(); u.Tokens == nil || *u.Tokens != 24763+122 || u.CostUSD != nil {
		t.Errorf("usage = %v tokens, %v USD; want input (cached included) plus output, cost unknown", u.Tokens, u.CostUSD)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, "input.txt"))
	if err != nil || string(b) != spec.Brief {
		t.Errorf("input = %q, %v; want the brief", b, err)
	}

	var args []string
	f.read(t, "args.json", &args)
	if args[0] != "exec" || args[len(args)-1] != "-" {
		t.Errorf("args %q: want codex exec … -", args)
	}
	got, err := filepath.EvalSymlinks(after(args, "--add-dir"))
	if err != nil || got != gitDir {
		t.Errorf("--add-dir %q, want the repository's git directory %q", after(args, "--add-dir"), gitDir)
	}
	for _, want := range [][]string{
		{"--json"},
		{"--sandbox", "workspace-write"},
		{"-c", "sandbox_workspace_write.network_access=true"},
		{"-c", `approval_policy="never"`},
		{"-c", "mcp_servers={}"},
		{"-c", `mcp_servers.anthill.command="/opt/anthill/bin/anthill"`},
		{"-c", `mcp_servers.anthill.args=["mcp","--ant","ant_tok3n"]`},
		{"-c", "otel.log_user_prompt=false"},
	} {
		if !containsSeq(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if slices.Index(args, "mcp_servers={}") > slices.Index(args, `mcp_servers.anthill.command="/opt/anthill/bin/anthill"`) {
		t.Error("the developer's MCP servers are cleared after the ant tools server is added")
	}
	if slices.Contains(args, "resume") || strings.Contains(strings.Join(args, " "), "login timeout") {
		t.Errorf("args %q: a new session must not resume, and the brief goes on standard input", args)
	}

	if err := run.Send(context.Background(), "12 minutes left"); !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("Send = %v, want ErrUnsupported", err)
	}
	caps := f.runner.Capabilities()
	if caps != (ports.Capabilities{Resume: true, Usage: true}) {
		t.Errorf("capabilities = %+v", caps)
	}
}

// Task 13.4: resuming runs codex exec … resume <thread id> -.
func TestResume(t *testing.T) {
	wt, _ := worktree(t)
	f := newFake(t, "resume")
	run, err := f.runner.Resume(context.Background(), recordedThread, f.spec(wt))
	if err != nil {
		t.Fatal(err)
	}
	if got := run.SessionID(); got != recordedThread {
		t.Errorf("session before the first event = %q, want the resumed one", got)
	}
	run.Wait()
	var args []string
	f.read(t, "args.json", &args)
	if !slices.Equal(args[len(args)-3:], []string{"resume", recordedThread, "-"}) {
		t.Errorf("args %q do not end with resume %s -", args, recordedThread)
	}
	if u := run.Usage(); u.Tokens == nil || *u.Tokens != 31002+64 {
		t.Errorf("tokens = %v", u.Tokens)
	}
	if _, err := f.runner.Resume(context.Background(), "", f.spec(wt)); err == nil {
		t.Error("Resume without a session id succeeded")
	}
}

// A failed turn reports the exit code and no usage; lines that are not
// JSON are skipped.
func TestFailedTurn(t *testing.T) {
	wt, _ := worktree(t)
	f := newFake(t, "failed")
	run, err := f.runner.Start(context.Background(), f.spec(wt))
	if err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != (ports.Exit{Code: 1}) {
		t.Errorf("exit = %+v", exit)
	}
	if run.SessionID() != recordedThread {
		t.Errorf("session = %q", run.SessionID())
	}
	if u := run.Usage(); u.Tokens != nil {
		t.Errorf("tokens = %d, want unknown", *u.Tokens)
	}
}

// Task 13.4: Stop ends a run that is still working.
func TestStop(t *testing.T) {
	wt, _ := worktree(t)
	f := newFake(t, "")
	if err := os.WriteFile(filepath.Join(f.dir, "block"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := f.runner.Start(context.Background(), f.spec(wt))
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Stop(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != agentproc.StoppedExit {
		t.Errorf("exit = %+v", exit)
	}
}

// A run outside a git worktree does not start: Codex could not commit.
func TestStartOutsideGit(t *testing.T) {
	f := newFake(t, "start")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	if _, err := f.runner.Start(context.Background(), f.spec(t.TempDir())); err == nil {
		t.Error("Start outside a git worktree succeeded")
	}
}

// Task 13.4: with export on, Codex gets its OTLP exporter and the
// standard OTLP variables, with the headers only in the environment.
func TestTelemetry(t *testing.T) {
	wt, _ := worktree(t)
	f := newFake(t, "resume")
	spec := f.spec(wt)
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
	var args, env []string
	f.read(t, "args.json", &args)
	f.read(t, "env.json", &env)
	if !containsSeq(args, []string{"-c", `otel.exporter={ otlp-http = { endpoint = "https://otel.example.com:4318", protocol = "binary" } }`}) {
		t.Errorf("args %q lack the OTLP exporter", args)
	}
	if strings.Contains(strings.Join(args, " "), "s3cret") {
		t.Error("the OTLP headers are on the command line")
	}
	for _, want := range []string{
		"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20s3cret",
		"OTEL_RESOURCE_ATTRIBUTES=anthill.ant_id=7,anthill.instance=mrosani@LAPTOP-42,anthill.item=gh-12",
		"TRACEPARENT=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
}

// Task 13.6: the CLI is available when found.
func TestAvailable(t *testing.T) {
	f := newFake(t, "")
	if err := f.runner.Available(context.Background()); err != nil {
		t.Errorf("Available = %v", err)
	}
	if err := codex.New(codex.Options{Command: "anthill-no-such-codex"}).Available(context.Background()); err == nil {
		t.Error("a missing CLI is available")
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

func after(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
