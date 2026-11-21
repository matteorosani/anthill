package testkit

import (
	"context"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// VerifyCall is one call of the fake verifier.
type VerifyCall struct {
	Dir     string
	Command string
	Timeout time.Duration
}

// Verifier is an in-memory ports.Verifier: every command ends as Result
// says, or fails with Err. The zero value passes every command.
type Verifier struct {
	mu     sync.Mutex
	result ports.Verification
	err    error
	calls  []VerifyCall
}

var _ ports.Verifier = (*Verifier)(nil)

// Set makes later commands end with result, or fail with err.
func (v *Verifier) Set(result ports.Verification, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.result, v.err = result, err
}

// Calls returns the commands run so far.
func (v *Verifier) Calls() []VerifyCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]VerifyCall(nil), v.calls...)
}

// Verify records the call and returns the set result.
func (v *Verifier) Verify(ctx context.Context, dir, command string, timeout time.Duration) (ports.Verification, error) {
	if err := ctx.Err(); err != nil {
		return ports.Verification{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, VerifyCall{Dir: dir, Command: command, Timeout: timeout})
	return v.result, v.err
}
