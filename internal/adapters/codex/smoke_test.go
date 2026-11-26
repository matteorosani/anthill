package codex_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/ports"
)

// smokeRecording replays testdata/smoke/<check>.events.jsonl, an event
// stream recorded by the Windows smoke run of issue #149 with codex-cli
// 0.163.0, through the fake codex.
func smokeRecording(t *testing.T, check string) *fake {
	t.Helper()
	f := newFake(t, "")
	b, err := fixtures.ReadFile("testdata/smoke/" + check + ".events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "output.jsonl"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// Task 13.8: the event streams recorded in #149 give the session id,
// tokens and last message the smoke run reported (x<n>.out.txt), and a
// stream cut by a stop keeps its session id with unknown tokens.
func TestSmokeRecordings(t *testing.T) {
	for _, c := range []struct {
		check, session string
		// tokens is 0 for unknown.
		tokens      int64
		lastMessage string
	}{
		{"x1", "01a1b0fd-d26e-7fd0-8988-276bf1449a75", 37124,
			"smoke_ping returned no word: the call required approval, but approval policy is set to never."},
		{"x2", "01a1b0fe-07f3-72b2-baa8-42480a7d748c", 68429, "Created `hello.txt` containing `hi`."},
		{"x3", "01a1b0fe-8ed9-7aa3-bf5b-105937886ad6", 37161, "```text\nfatal: unable to access"},
		{"x4", "01a1b0fd-d26e-7fd0-8988-276bf1449a75", 57032, "None"},
		{"x5", "01a1b0fe-ee21-7b03-9401-5aad0e9aa2ee", 50866, "I couldn’t call `smoke_ping`"},
		{"x6", "01a1b0ff-35a1-7e62-9766-4d197f51d5b8", 0, "I’ll run the command in the foreground and wait for it to finish."},
	} {
		t.Run(c.check, func(t *testing.T) {
			wt, _ := worktree(t)
			f := smokeRecording(t, c.check)
			run, err := f.runner.Start(context.Background(), f.spec(wt), ports.RunIO{})
			if err != nil {
				t.Fatal(err)
			}
			run.Wait()
			if got := run.SessionID(); got != c.session {
				t.Errorf("session = %q, want %q", got, c.session)
			}
			u := run.Usage()
			switch {
			case c.tokens == 0 && u.Tokens != nil:
				t.Errorf("tokens = %d, want unknown", *u.Tokens)
			case c.tokens != 0 && (u.Tokens == nil || *u.Tokens != c.tokens):
				t.Errorf("tokens = %v, want %d", u.Tokens, c.tokens)
			}
			if u.CostUSD != nil {
				t.Errorf("cost = %v, want unknown", *u.CostUSD)
			}
			if got := run.LastMessage(); !strings.HasPrefix(got, c.lastMessage) {
				t.Errorf("last message = %q, want it to start with %q", got, c.lastMessage)
			}
		})
	}
}

// Task 13.8: in X1 and X5, Codex refused the ant tool call because the
// tool needed approval under approval_policy "never". The adapter now
// approves the ant tools server's tools (TestStart), which is what that
// refusal asks for.
func TestSmokeToolCallNeededApproval(t *testing.T) {
	for _, check := range []string{"x1", "x5"} {
		b, err := fixtures.ReadFile("testdata/smoke/" + check + ".events.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"server":"anthill","tool":"smoke_ping"`) ||
			!strings.Contains(string(b), "MCP tool call requires approval, but approval policy is never") {
			t.Errorf("%s: the recording does not show the ant tool call refused for approval", check)
		}
	}
}
