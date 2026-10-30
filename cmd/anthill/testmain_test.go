package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/matteorosani/anthill/internal/testkit"
)

// anthillFlag, as the first argument of the test binary, makes it run as
// the anthill command with the arguments that follow, so tests can start
// anthill as a separate process without building it.
const anthillFlag = "-anthill-main"

// fakeClaudeVar, set in the environment, makes the test binary play a
// Claude Code run that answers its first input with a session and a
// result, then exits when its input ends.
const fakeClaudeVar = "ANTHILL_TEST_FAKE_CLAUDE"

func TestMain(m *testing.M) {
	testkit.RunFakeAgentIfRequested()
	if os.Getenv(fakeClaudeVar) != "" {
		in := bufio.NewScanner(os.Stdin)
		for n := 0; in.Scan(); n++ {
			if n == 0 {
				fmt.Println(`{"type":"system","subtype":"init","session_id":"smoke-session"}`)
				fmt.Println(`{"type":"result","subtype":"success","session_id":"smoke-session","total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":20}}`)
			}
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == anthillFlag {
		os.Exit(run(context.Background(), os.Args[2:], defaultEnvironment()))
	}
	os.Exit(m.Run())
}
