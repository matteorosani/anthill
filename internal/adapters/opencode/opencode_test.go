package opencode_test

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/adapters/opencode"
	"github.com/matteorosani/anthill/internal/ports"
)

// fixtures are recorded outputs of opencode run --format json.
//
//go:embed testdata
var fixtures embed.FS

// fakeOpenCodeVar, set in the agent's environment, makes the test binary
// play opencode: it records its arguments, environment, configuration
// and input in that directory, prints output.jsonl from there once its
// input ends, and exits with the code in exit-code, if any. With a file
// named block, it keeps running instead.
const fakeOpenCodeVar = "ANTHILL_FAKE_OPENCODE"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeOpenCodeVar); dir != "" {
		os.Exit(fakeOpenCode(dir))
	}
	os.Exit(m.Run())
}

func fakeOpenCode(dir string) int {
	write := func(name string, b []byte) { _ = os.WriteFile(filepath.Join(dir, name), b, 0o600) }
	args, _ := json.Marshal(os.Args[1:])
	write("args.json", args)
	env, _ := json.Marshal(os.Environ())
	write("env.json", env)
	if b, err := os.ReadFile(os.Getenv("OPENCODE_CONFIG")); err == nil {
		write("config.json", b)
	}
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

type fake struct {
	dir    string
	runner *opencode.Runner
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
	return &fake{dir: dir, runner: opencode.New(opencode.Options{Command: exe})}
}

func (f *fake) spec(t *testing.T) ports.RunSpec {
	return ports.RunSpec{
		Worktree:   t.TempDir(),
		Brief:      "Fix the login timeout (gh-12).\nSay \"done\" when finished.",
		Budget:     time.Hour,
		MCPCommand: []string{"/opt/anthill/bin/anthill", "mcp", "--ant", "ant_tok3n"},
		ToolToken:  "ant_tok3n",
		Env:        append(os.Environ(), fakeOpenCodeVar+"="+f.dir, "OPENCODE_CONFIG=/home/ann/my-opencode.json"),
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

const recordedSession = "ses_6f2a9c1e4ffeK3w8Q1Zr7bN2xD"

// Task 13.5: a run starts opencode run with JSON events, its own
// configuration with the ant tools server and the permission rules, and
// the brief on standard input; it captures the session id and adds up
// the steps' tokens and cost.
func TestStart(t *testing.T) {
	f := newFake(t, "start")
	spec := f.spec(t)
	run, err := f.runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != (ports.Exit{}) {
		t.Errorf("exit = %+v", exit)
	}
	if got := run.SessionID(); got != recordedSession {
		t.Errorf("session = %q, want %q", got, recordedSession)
	}
	u := run.Usage()
	if u.Tokens == nil || *u.Tokens != (1200+80+9000+300)+(150+40+12+10300) {
		t.Errorf("tokens = %v, want every token of both steps", u.Tokens)
	}
	if u.CostUSD == nil || math.Abs(*u.CostUSD-0.0168) > 1e-9 {
		t.Errorf("cost = %v, want the sum of both steps", u.CostUSD)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, "input.txt"))
	if err != nil || string(b) != spec.Brief {
		t.Errorf("input = %q, %v; want the brief", b, err)
	}
	var args []string
	f.read(t, "args.json", &args)
	if !slices.Equal(args, []string{"run", "--format", "json"}) {
		t.Errorf("args = %q", args)
	}

	var env []string
	f.read(t, "env.json", &env)
	var configPath string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG="); ok {
			if configPath != "" {
				t.Error("OPENCODE_CONFIG is set twice")
			}
			configPath = v
		}
	}
	if configPath == "" || configPath == "/home/ann/my-opencode.json" {
		t.Errorf("OPENCODE_CONFIG = %q, want the run's own", configPath)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("the run's configuration %s is still there after Wait: %v", configPath, err)
	}

	var config struct {
		MCP map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
		Permission struct {
			Edit string            `json:"edit"`
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
	}
	f.read(t, "config.json", &config)
	srv := config.MCP["anthill"]
	if len(config.MCP) != 1 || srv.Type != "local" || !srv.Enabled ||
		!slices.Equal(srv.Command, []string{"/opt/anthill/bin/anthill", "mcp", "--ant", "ant_tok3n"}) {
		t.Errorf("mcp = %+v", config.MCP)
	}
	if config.Permission.Edit != "allow" || config.Permission.Bash["*"] != "allow" {
		t.Errorf("permission = %+v, want edits and commands allowed", config.Permission)
	}
	for _, p := range []string{"git push", "git push *", "gh", "gh *", "az", "az *"} {
		if config.Permission.Bash[p] != "deny" {
			t.Errorf("bash permission %q = %q, want deny", p, config.Permission.Bash[p])
		}
	}

	if err := run.Send(context.Background(), "12 minutes left"); !errors.Is(err, ports.ErrUnsupported) {
		t.Errorf("Send = %v, want ErrUnsupported", err)
	}
	if caps := f.runner.Capabilities(); caps != (ports.Capabilities{Resume: true, Usage: true}) {
		t.Errorf("capabilities = %+v", caps)
	}
}

// Task 13.5: resuming runs opencode run --session <id>.
func TestResume(t *testing.T) {
	f := newFake(t, "resume")
	run, err := f.runner.Resume(context.Background(), recordedSession, f.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := run.SessionID(); got != recordedSession {
		t.Errorf("session before the first event = %q", got)
	}
	run.Wait()
	var args []string
	f.read(t, "args.json", &args)
	if !slices.Equal(args, []string{"run", "--format", "json", "--session", recordedSession}) {
		t.Errorf("args = %q", args)
	}
	if u := run.Usage(); u.Tokens == nil || *u.Tokens != 90+30+11000 || u.CostUSD == nil || *u.CostUSD != 0.002 {
		t.Errorf("usage = %v tokens, %v USD", u.Tokens, u.CostUSD)
	}
	if _, err := f.runner.Resume(context.Background(), "", f.spec(t)); err == nil {
		t.Error("Resume without a session id succeeded")
	}
}

// A failed run reports its exit code and unknown usage; lines that are
// not JSON are skipped.
func TestFailedRun(t *testing.T) {
	f := newFake(t, "failed")
	run, err := f.runner.Start(context.Background(), f.spec(t))
	if err != nil {
		t.Fatal(err)
	}
	if exit := run.Wait(); exit != (ports.Exit{Code: 1}) {
		t.Errorf("exit = %+v", exit)
	}
	if run.SessionID() != recordedSession {
		t.Errorf("session = %q", run.SessionID())
	}
	if u := run.Usage(); u.Tokens != nil || u.CostUSD != nil {
		t.Errorf("usage = %+v, want unknown", u)
	}
}

// Task 13.5: Stop ends a run that is still working.
func TestStop(t *testing.T) {
	f := newFake(t, "")
	if err := os.WriteFile(filepath.Join(f.dir, "block"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
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
}

// Task 13.6: the CLI is available when found.
func TestAvailable(t *testing.T) {
	f := newFake(t, "")
	if err := f.runner.Available(context.Background()); err != nil {
		t.Errorf("Available = %v", err)
	}
	if err := opencode.New(opencode.Options{Command: "anthill-no-such-opencode"}).Available(context.Background()); err == nil {
		t.Error("a missing CLI is available")
	}
}
