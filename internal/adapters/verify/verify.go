// Package verify is the Verifier that runs a repository's verification
// command on this machine, in a process tree of its own that it ends
// when the command runs past its time (spec pr-shepherd, "Anthill
// verifies, pushes and replies").
package verify

import (
	"context"
	"os"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// StopGrace is how long a command that ran past its time, or whose
// context ended, has to end on its own before its tree is killed.
const StopGrace = 5 * time.Second

// Verifier runs verification commands through the system shell: sh -c
// on Unix, and a batch file run by cmd /C on Windows, so that the command
// is read as written whatever quotes it holds.
type Verifier struct {
	// environ is Anthill's own environment, which Verify turns into an
	// agent's.
	environ func() []string
}

var _ ports.Verifier = (*Verifier)(nil)

// New returns a verifier.
func New() *Verifier {
	return &Verifier{environ: os.Environ}
}

// Verify runs command in dir (ports.Verifier).
func (v *Verifier) Verify(ctx context.Context, dir, command string, timeout time.Duration) (ports.Verification, error) {
	if err := ctx.Err(); err != nil {
		return ports.Verification{}, err
	}
	sh, err := shell(command)
	if err != nil {
		return ports.Verification{}, err
	}
	defer sh.cleanup()
	p, err := agentproc.Start(agentproc.Spec{
		Path: sh.path,
		Args: sh.args,
		Dir:  dir,
		Env:  domain.AgentEnv(v.environ()),
	})
	if err != nil {
		return ports.Verification{}, err
	}
	done := make(chan ports.Exit, 1)
	go func() { done <- p.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case exit := <-done:
		return ports.Verification{Exit: exit}, nil
	case <-timer.C:
		_ = p.Stop(StopGrace)
		return ports.Verification{Exit: <-done, TimedOut: true}, nil
	case <-ctx.Done():
		_ = p.Stop(StopGrace)
		<-done
		return ports.Verification{}, ctx.Err()
	}
}

// command is how the shell is started for one command, and cleanup
// removes what that needed.
type command struct {
	path    string
	args    []string
	cleanup func()
}
