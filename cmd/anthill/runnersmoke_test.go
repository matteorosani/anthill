package main

import (
	"os"
	"strings"
	"testing"
)

// anthill runner-smoke starts a run through the runner adapter, and
// reports its session id, exit and usage.
func TestRunnerSmoke(t *testing.T) {
	te := newTestEnv(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeClaudeVar, "1")
	code, stdout, stderr := te.command("runner-smoke", "--runner", "claude-code", "--command", exe, "Call smoke_ping.")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"smoke: run started with claude-code",
		"smoke: session id: smoke-session",
		"smoke: run ended: exit code 0, crashed false",
		"smoke: usage: 30 tokens, 0.0100 USD",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, `"session_id":"smoke-session"`) {
		t.Errorf("the agent's output is not logged on stderr: %q", stderr)
	}
}

// A smoke run needs a known runner whose CLI is installed, and no
// daemon.
func TestRunnerSmokeRefuses(t *testing.T) {
	te := newTestEnv(t)
	if code, _, stderr := te.command("runner-smoke", "--runner", "nope", "x"); code != 2 || !strings.Contains(stderr, `unknown runner "nope"`) {
		t.Errorf("unknown runner: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := te.command("runner-smoke", "--runner", "claude-code"); code != 2 || !strings.Contains(stderr, "needs the brief") {
		t.Errorf("no brief: code %d, stderr %q", code, stderr)
	}
	for _, r := range []string{"codex", "opencode"} {
		if code, _, stderr := te.command("runner-smoke", "--runner", r, "--command", "anthill-no-such-cli", "x"); code != 1 || !strings.Contains(stderr, "runner "+r+" not available") {
			t.Errorf("%s missing: code %d, stderr %q", r, code, stderr)
		}
	}
	if code, _, stderr := te.command("runner-smoke", "--runner", "claude-code", "--command", "anthill-no-such-claude", "x"); code != 1 || !strings.Contains(stderr, "runner claude-code not available") {
		t.Errorf("missing CLI: code %d, stderr %q", code, stderr)
	}
	te.startDaemon(t)
	if code, _, stderr := te.command("runner-smoke", "--runner", "claude-code", "x"); code != 1 || !strings.Contains(stderr, "stop the daemon before a smoke run") {
		t.Errorf("with a daemon: code %d, stderr %q", code, stderr)
	}
}
