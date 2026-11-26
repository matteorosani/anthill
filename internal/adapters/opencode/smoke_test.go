package opencode_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/ports"
)

// smokeRecording replays testdata/smoke/<check>.events.jsonl, an event
// stream recorded by the Windows smoke run of issue #150 with OpenCode
// v2.2.5, through the fake opencode.
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

// Task 13.9: the event streams recorded in #150 give the session id,
// tokens, cost and last message the smoke run reported (o<n>.out.txt),
// and a stream cut by a stop keeps its session id with unknown usage.
func TestSmokeRecordings(t *testing.T) {
	for _, c := range []struct {
		check, session string
		// tokens is 0 for unknown usage.
		tokens      int64
		lastMessage string
	}{
		{"o1", "ses_e4eed6049ffeYR9m55J4tnlPUv", 48246, "I can't do that — there is no `smoke_ping` tool"},
		{"o2", "ses_e4eed1c0affelIDlL62M0AXpFd", 22532, "Both commands failed."},
		{"o3", "ses_e4eeceb16ffeDFcV5Qf0Ed338k", 14795, "The command failed."},
		{"o4", "ses_e4eed6049ffeYR9m55J4tnlPUv", 8647, "No tool was called, so no word was returned."},
		{"o5", "ses_e4eecc367ffeYTzmeSmcAQaxsX", 7512, "I can't run that one"},
		{"o6", "ses_e4eeca393ffec7DbLrZPKI5keN", 0, ""},
	} {
		t.Run(c.check, func(t *testing.T) {
			f := smokeRecording(t, c.check)
			run, err := f.runner.Start(context.Background(), f.spec(t), ports.RunIO{})
			if err != nil {
				t.Fatal(err)
			}
			run.Wait()
			if got := run.SessionID(); got != c.session {
				t.Errorf("session = %q, want %q", got, c.session)
			}
			u := run.Usage()
			if c.tokens == 0 {
				if u.Tokens != nil || u.CostUSD != nil {
					t.Errorf("usage = %v tokens, %v USD; want unknown", u.Tokens, u.CostUSD)
				}
			} else if u.Tokens == nil || *u.Tokens != c.tokens || u.CostUSD == nil || *u.CostUSD != 0 {
				// The smoke runs used a free model.
				t.Errorf("usage = %v tokens, %v USD; want %d tokens, 0 USD", u.Tokens, u.CostUSD, c.tokens)
			}
			if got := run.LastMessage(); !strings.HasPrefix(got, c.lastMessage) {
				t.Errorf("last message = %q, want it to start with %q", got, c.lastMessage)
			}
		})
	}
}
