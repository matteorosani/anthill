package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/configfile"
	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// syncBuffer is a bytes.Buffer that a running daemon can write while the
// test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testEnv is an environment in a temporary directory, with a fake clock
// and a fake notifier, and the buffers its commands write to.
type testEnv struct {
	*environment
	dir            string
	clock          *testkit.Clock
	stdout, stderr *syncBuffer
}

// newTestEnv returns an environment of its own: a short temporary
// directory, because macOS limits socket paths to 103 bytes and
// t.TempDir() can be longer, or a uniquely named pipe on Windows.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "ah")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	home := filepath.Join(dir, ".anthill")
	address := filepath.Join(home, "control.sock")
	if runtime.GOOS == "windows" {
		address = fmt.Sprintf(`\\.\pipe\anthill-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	clock := testkit.NewClock(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC))
	te := &testEnv{dir: dir, clock: clock, stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	te.environment = &environment{
		stdout:     te.stdout,
		stderr:     te.stderr,
		home:       home,
		configPath: filepath.Join(dir, "config.yml"),
		address:    address,
		clock:      clock,
		desktop:    func(*slog.Logger) ports.Notifier { return &testkit.Notifier{} },
	}
	return te
}

// command runs one command line in a fresh copy of the environment, with
// its own output buffers, and returns the exit code and output.
func (te *testEnv) command(args ...string) (code int, stdout, stderr string) {
	env := *te.environment
	var out, errOut bytes.Buffer
	env.stdout, env.stderr = &out, &errOut
	code = run(context.Background(), args, &env)
	return code, out.String(), errOut.String()
}

// startDaemon runs `anthill daemon` in-process until the test ends, and
// waits until it answers on the control interface. The returned function
// stops it, as a signal would, and returns its exit code.
func (te *testEnv) startDaemon(t *testing.T) func() int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"daemon"}, te.environment) }()
	var once sync.Once
	var code int
	stop := func() int {
		once.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(10 * time.Second):
				t.Error("the daemon did not stop")
				code = -1
			}
		})
		return code
	}
	t.Cleanup(func() { stop() })

	client := control.NewClient(te.address)
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case code := <-done:
			t.Fatalf("the daemon exited with %d: %s", code, te.stderr.String())
		default:
		}
		err := client.Call(context.Background(), methodStatus, nil, nil)
		if err == nil {
			return stop
		}
		if !errors.Is(err, control.ErrNotRunning) || time.Now().After(deadline) {
			t.Fatalf("daemon not reachable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// End to end: `anthill status` asks a running daemon for its status and
// prints its uptime, measured by the daemon's clock.
func TestStatusReportsUptimeFromRunningDaemon(t *testing.T) {
	te := newTestEnv(t)
	stop := te.startDaemon(t)
	te.clock.Advance(90 * time.Minute)

	code, stdout, stderr := te.command("status")
	want := fmt.Sprintf("daemon: running (pid %d, anthill dev)\nuptime: 1h30m0s\n", os.Getpid())
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("status = %d, %q, %q\nwant 0, %q", code, stdout, stderr, want)
	}

	if code := stop(); code != 0 {
		t.Errorf("daemon exit code = %d: %s", code, te.stderr.String())
	}
	for _, want := range []string{"daemon started", "daemon stopped"} {
		if !strings.Contains(te.stderr.String(), want) {
			t.Errorf("daemon log %q does not contain %q", te.stderr.String(), want)
		}
	}
	// Once stopped, there is no daemon to ask.
	if code, stdout, _ := te.command("status"); code != 1 || stdout != "daemon not running\n" {
		t.Errorf("status after stop = %d, %q", code, stdout)
	}
}

func TestStatusWithoutDaemon(t *testing.T) {
	code, stdout, stderr := newTestEnv(t).command("status")
	if code != 1 || stdout != "daemon not running\n" || stderr != "" {
		t.Errorf("status = %d, %q, %q", code, stdout, stderr)
	}
}

// Scenario "Second daemon start": a second daemon exits at once with the
// pid of the one already running, which keeps running.
func TestSecondDaemonStart(t *testing.T) {
	te := newTestEnv(t)
	stop := te.startDaemon(t)

	code, _, stderr := te.command("daemon")
	want := fmt.Sprintf("anthill: daemon already running (pid %d)\n", os.Getpid())
	if code != 1 || stderr != want {
		t.Errorf("second daemon = %d, %q\nwant 1, %q", code, stderr, want)
	}
	if code, _, _ := te.command("status"); code != 0 {
		t.Error("the first daemon stopped")
	}

	// After the first daemon stops, a new one starts.
	if code := stop(); code != 0 {
		t.Fatalf("daemon exit code = %d", code)
	}
	te.startDaemon(t)
}

// Scenario "Warning threshold out of range" (anthill-configuration): an
// invalid global configuration prevents startup, and the lock is given
// back.
func TestDaemonRefusesInvalidConfiguration(t *testing.T) {
	te := newTestEnv(t)
	if err := os.WriteFile(te.configPath, []byte("warning-threshold: 120\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := te.command("daemon")
	if code != 1 || !strings.Contains(stderr, "warning threshold must be between 1 and 99 percent") {
		t.Errorf("daemon = %d, %q", code, stderr)
	}
	if err := os.WriteFile(te.configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	te.startDaemon(t)
}

// newTestConfiguration loads the test environment's configuration.
func newTestConfiguration(t *testing.T, te *testEnv) *app.Configuration {
	t.Helper()
	config := app.NewConfiguration(configfile.New(te.configPath))
	if _, err := config.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return config
}

// The daemon log redacts secrets in messages and attributes.
func TestLoggerRedactsSecrets(t *testing.T) {
	te := newTestEnv(t)
	var logs bytes.Buffer
	config := newTestConfiguration(t, te)
	logger := newLogger(&logs, config)
	token := "ghp_" + strings.Repeat("a", 36)
	logger.Info("token "+token, "value", token, "error", errors.New("bad "+token))
	if strings.Contains(logs.String(), token) || strings.Count(logs.String(), "[REDACTED]") != 3 {
		t.Errorf("log = %q", logs.String())
	}
}

func TestRunAllStopsTheOthersOnFailure(t *testing.T) {
	failure := errors.New("listener broke")
	err := runAll(context.Background(),
		func(ctx context.Context) error { <-ctx.Done(); return nil },
		func(context.Context) error { return failure },
	)
	if !errors.Is(err, failure) {
		t.Errorf("err = %v, want %v", err, failure)
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version command", args: []string{"version"}, wantStdout: "anthill dev\n"},
		{name: "version flag", args: []string{"--version"}, wantStdout: "anthill dev\n"},
		{
			name:       "no arguments",
			wantCode:   2,
			wantStderr: "anthill: a command is required\nRun 'anthill --help' for usage.\n",
		},
		{
			name:       "unknown command",
			args:       []string{"fly"},
			wantCode:   2,
			wantStderr: "anthill: unknown command \"fly\"\nRun 'anthill --help' for usage.\n",
		},
		{
			name:       "extra arguments",
			args:       []string{"version", "now"},
			wantCode:   2,
			wantStderr: "anthill: version takes no arguments\nRun 'anthill --help' for usage.\n",
		},
		{
			name:       "unknown flag",
			args:       []string{"status", "--json"},
			wantCode:   2,
			wantStderr: "anthill: unknown flag: --json\nRun 'anthill --help' for usage.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := newTestEnv(t).command(tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if stdout != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout, tt.wantStdout)
			}
			if stderr != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr, tt.wantStderr)
			}
		})
	}
}

func TestHelpListsTheCommands(t *testing.T) {
	code, stdout, _ := newTestEnv(t).command("--help")
	for _, want := range []string{"daemon", "status", "version"} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("help = %d, %q, missing %q", code, stdout, want)
		}
	}
}

// failingWriter is an io.Writer whose every write fails.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRunReportsWriteFailure(t *testing.T) {
	env := *newTestEnv(t).environment
	env.stdout = failingWriter{}
	env.stderr = io.Discard
	if code := run(context.Background(), []string{"version"}, &env); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestDefaultEnvironment(t *testing.T) {
	env := defaultEnvironment()
	if env.err != nil {
		t.Skipf("no home directory: %v", env.err)
	}
	if filepath.Base(env.home) != ".anthill" || env.configPath == "" || env.address == "" {
		t.Errorf("environment = %+v", env)
	}
}
