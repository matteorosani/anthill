package main

import (
	"context"
	"os"
	"testing"

	"github.com/matteorosani/anthill/internal/testkit"
)

// anthillFlag, as the first argument of the test binary, makes it run as
// the anthill command with the arguments that follow, so tests can start
// anthill as a separate process without building it.
const anthillFlag = "-anthill-main"

func TestMain(m *testing.M) {
	testkit.RunFakeAgentIfRequested()
	if len(os.Args) > 1 && os.Args[1] == anthillFlag {
		os.Exit(run(context.Background(), os.Args[2:], defaultEnvironment()))
	}
	os.Exit(m.Run())
}
