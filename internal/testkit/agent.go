package testkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// StoppedExit is how a FakeRun ends when it is stopped: killed.
var StoppedExit = ports.Exit{Code: -1, Crashed: true}

// RunScript says how one fake run behaves.
type RunScript struct {
	// SessionID is the session the run reports. When empty, a new run
	// gets session-<n> and a resumed run keeps the session it resumed.
	SessionID string
	Usage     ports.Usage
	// Exit, when set, ends the run at once with that exit. Otherwise the
	// run stays active until the test calls End or Anthill calls Stop.
	Exit *ports.Exit
}

// AgentRunner is a ports.AgentRunner whose runs follow scripts instead of
// starting a process. Queue scripts with Script; a run without a queued
// script stays active until ended. StartErr, when set, fails Start and
// Resume, and Unavailable fails Available.
type AgentRunner struct {
	mu          sync.Mutex
	caps        ports.Capabilities
	scripts     []RunScript
	runs        []*FakeRun
	StartErr    error
	Unavailable error
}

var _ ports.AgentRunner = (*AgentRunner)(nil)

// NewAgentRunner returns a runner with the given capabilities.
func NewAgentRunner(caps ports.Capabilities) *AgentRunner {
	return &AgentRunner{caps: caps}
}

// Script queues the behaviour of the next runs, in order.
func (r *AgentRunner) Script(scripts ...RunScript) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts = append(r.scripts, scripts...)
}

// Runs returns every run started so far, in order.
func (r *AgentRunner) Runs() []*FakeRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*FakeRun(nil), r.runs...)
}

// Capabilities returns the capabilities given to NewAgentRunner.
func (r *AgentRunner) Capabilities() ports.Capabilities {
	return r.caps
}

// Available returns Unavailable.
func (r *AgentRunner) Available(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Unavailable
}

// Start starts a run with a new session.
func (r *AgentRunner) Start(_ context.Context, spec ports.RunSpec) (ports.Run, error) {
	return r.start(spec, "")
}

// Resume starts a run that continues sessionID, or returns
// ports.ErrUnsupported when the capabilities have no Resume.
func (r *AgentRunner) Resume(_ context.Context, sessionID string, spec ports.RunSpec) (ports.Run, error) {
	if !r.caps.Resume {
		return nil, fmt.Errorf("resume: %w", ports.ErrUnsupported)
	}
	return r.start(spec, sessionID)
}

func (r *AgentRunner) start(spec ports.RunSpec, resumed string) (*FakeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.StartErr != nil {
		return nil, r.StartErr
	}
	var script RunScript
	if len(r.scripts) > 0 {
		script, r.scripts = r.scripts[0], r.scripts[1:]
	}
	session := script.SessionID
	switch {
	case session != "":
	case resumed != "":
		session = resumed
	default:
		session = fmt.Sprintf("session-%d", len(r.runs)+1)
	}
	run := &FakeRun{
		Spec:     spec,
		Resumed:  resumed,
		live:     r.caps.LiveInput,
		session:  session,
		usage:    script.Usage,
		finished: make(chan struct{}),
	}
	r.runs = append(r.runs, run)
	if script.Exit != nil {
		run.End(*script.Exit)
	}
	return run, nil
}

// FakeRun is one scripted run. It is safe for concurrent use.
type FakeRun struct {
	// Spec is what the run was started with.
	Spec ports.RunSpec
	// Resumed is the session it resumed, or empty for a new session.
	Resumed string

	mu       sync.Mutex
	live     bool
	session  string
	usage    ports.Usage
	inputs   []string
	stopped  bool
	exit     ports.Exit
	ended    bool
	finished chan struct{}
}

var _ ports.Run = (*FakeRun)(nil)

// SessionID returns the session the run reports.
func (f *FakeRun) SessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

// Send records input while the run is active. Without live input it
// returns ports.ErrUnsupported.
func (f *FakeRun) Send(_ context.Context, input string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case !f.live:
		return fmt.Errorf("send: %w", ports.ErrUnsupported)
	case f.ended:
		return errors.New("send: the run has ended")
	}
	f.inputs = append(f.inputs, input)
	return nil
}

// Stop ends an active run with StoppedExit. Stopping an ended run does
// nothing.
func (f *FakeRun) Stop(_ time.Duration) error {
	f.mu.Lock()
	ended := f.ended
	f.stopped = !ended
	f.mu.Unlock()
	if !ended {
		f.End(StoppedExit)
	}
	return nil
}

// Wait blocks until the run has ended.
func (f *FakeRun) Wait() ports.Exit {
	<-f.finished
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exit
}

// Usage returns the scripted usage, or what SetUsage set.
func (f *FakeRun) Usage() ports.Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage
}

// SetUsage changes the usage the run reports.
func (f *FakeRun) SetUsage(u ports.Usage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usage = u
}

// End ends the run with exit, as when the agent process exits or
// crashes. Ending an ended run does nothing.
func (f *FakeRun) End(exit ports.Exit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return
	}
	f.ended, f.exit = true, exit
	close(f.finished)
}

// Inputs returns what was sent to the run, in order.
func (f *FakeRun) Inputs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...)
}

// Stopped reports whether Stop ended the run.
func (f *FakeRun) Stopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}
