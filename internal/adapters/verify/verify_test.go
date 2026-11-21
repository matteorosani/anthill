package verify_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/verify"
)

// script picks the Unix or the Windows form of a command.
func script(unix, windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return unix
}

// Task 16.5: the verification command runs in the fix worktree and its
// exit code decides whether the fixes are pushed.
func TestVerify(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	cases := []struct {
		name    string
		command string
		code    int
		passed  bool
	}{
		{"passes", "exit 0", 0, true},
		{"fails", "exit 3", 3, false},
		{"runs in the worktree", script("test -f marker", `if exist marker (exit 0) else (exit 1)`), 0, true},
		{"keeps quotes", script(`test "a b" = "a b"`, `if "a b"=="a b" (exit 0) else (exit 1)`), 0, true},
		{"has no provider token", script(`test -z "$GITHUB_TOKEN"`, `if defined GITHUB_TOKEN (exit 1) else (exit 0)`), 0, true},
	}
	v := verify.New()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := v.Verify(context.Background(), dir, c.command, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if got.Exit.Code != c.code || got.Passed() != c.passed || got.TimedOut {
				t.Errorf("Verify() = %+v, passed %v; want code %d", got, got.Passed(), c.code)
			}
		})
	}
}

// Task 16.6: a command that runs past its time is ended and fails.
func TestVerifyTimeout(t *testing.T) {
	start := time.Now()
	got, err := verify.New().Verify(context.Background(), t.TempDir(), script("sleep 60", "ping -n 60 127.0.0.1 > nul"), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TimedOut || got.Passed() {
		t.Errorf("Verify() = %+v", got)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("Verify() took %v", d)
	}
}

func TestVerifyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := verify.New().Verify(ctx, t.TempDir(), script("sleep 60", "ping -n 60 127.0.0.1 > nul"), time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Verify() error = %v", err)
	}
}
