package ports

import (
	"context"
	"time"
)

// Verification is how a verification command ended.
type Verification struct {
	// Exit is the command's exit. Passed reads it.
	Exit Exit
	// TimedOut is set when the command ran past its time and its whole
	// tree was ended.
	TimedOut bool
}

// Passed reports whether the command passed: it ended in time with exit
// code 0.
func (v Verification) Passed() bool {
	return !v.TimedOut && !v.Exit.Crashed && v.Exit.Code == 0
}

// Verifier runs a repository's verification command on the result of a
// fix pass, before Anthill pushes it (spec pr-shepherd, "Anthill
// verifies, pushes and replies"; design D9).
type Verifier interface {
	// Verify runs command through the system shell in dir, with an
	// agent's environment, so it never sees a provider credential (design
	// D4). It ends the command's whole process tree once timeout has
	// passed, and returns how the command ended; a command that fails or
	// times out is not an error. The command's output is not kept: it
	// could hold anything, so nothing of it is posted. It returns an
	// error when the command cannot be started, and ctx's error, with the
	// tree ended, when ctx is done first.
	Verify(ctx context.Context, dir, command string, timeout time.Duration) (Verification, error)
}
