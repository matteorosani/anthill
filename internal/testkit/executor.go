package testkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Executor is a ports.Executor that starts no process. It records what
// was started, sent, given as a deadline and stopped, and a test drives
// each execution's events with Emit and End. StartErr fails Start, and
// SendErr fails Send for a runner with live input.
type Executor struct {
	mu       sync.Mutex
	caps     map[domain.Runner]ports.Capabilities
	execs    []*FakeExecution
	StartErr error
	SendErr  error
}

var _ ports.Executor = (*Executor)(nil)

// NewExecutor returns an executor whose runners have the given
// capabilities; a runner not listed has none.
func NewExecutor(caps map[domain.Runner]ports.Capabilities) *Executor {
	m := make(map[domain.Runner]ports.Capabilities, len(caps))
	for name, c := range caps {
		m[name] = c
	}
	return &Executor{caps: m}
}

// SetCapabilities sets the capabilities of runner for the next starts.
func (x *Executor) SetCapabilities(runner domain.Runner, caps ports.Capabilities) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.caps[runner] = caps
}

// Executions returns every execution started so far, in order.
func (x *Executor) Executions() []*FakeExecution {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]*FakeExecution(nil), x.execs...)
}

// Execution returns the execution with id, or nil.
func (x *Executor) Execution(id ports.ExecID) *FakeExecution {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, f := range x.execs {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// Deadline is the deadline currently set for the execution with id: the
// start's, or the last one SetDeadline gave. The zero time means none,
// and so does an unknown id.
func (x *Executor) Deadline(id ports.ExecID) time.Time {
	if f := x.Execution(id); f != nil {
		return f.Deadline()
	}
	return time.Time{}
}

// Start records spec and queues the started event. It returns
// ports.ErrUnsupported for a resume without the Resume capability.
func (x *Executor) Start(_ context.Context, spec ports.ExecSpec) (ports.Execution, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.StartErr != nil {
		return ports.Execution{}, x.StartErr
	}
	caps := x.caps[spec.Runner]
	if spec.Session != "" && !caps.Resume {
		return ports.Execution{}, fmt.Errorf("resume: %w", ports.ErrUnsupported)
	}
	events := make(chan ports.ExecEvent)
	f := &FakeExecution{
		ID:       ports.ExecID(fmt.Sprintf("exec-%d", len(x.execs)+1)),
		Spec:     spec,
		caps:     caps,
		deadline: spec.Deadline,
		wake:     make(chan struct{}, 1),
	}
	f.push(ports.ExecEvent{Kind: ports.ExecStarted})
	go f.send(events)
	x.execs = append(x.execs, f)
	return ports.Execution{ID: f.ID, Capabilities: caps, Events: events}, nil
}

// Send records input for a runner with live input. Without it, it
// returns ports.ErrUnsupported.
func (x *Executor) Send(_ context.Context, id ports.ExecID, input string) error {
	f, err := x.active(id)
	if err != nil {
		return err
	}
	if !f.caps.LiveInput {
		return fmt.Errorf("live input: %w", ports.ErrUnsupported)
	}
	x.mu.Lock()
	sendErr := x.SendErr
	x.mu.Unlock()
	if sendErr != nil {
		return sendErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, input)
	return nil
}

// SetDeadline records the new deadline. It ends nothing: a test reaches
// the deadline with End(ports.ExecEndedAtDeadline, …).
func (x *Executor) SetDeadline(id ports.ExecID, deadline time.Time) error {
	f, err := x.active(id)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadline = deadline
	f.deadlines = append(f.deadlines, deadline)
	return nil
}

// Stop records grace and ends the execution with ExecExited and
// StoppedExit. Stopping an ended execution records nothing and returns
// nil, as the real executor does.
func (x *Executor) Stop(id ports.ExecID, grace time.Duration) error {
	f := x.Execution(id)
	if f == nil {
		return fmt.Errorf("execution %s: %w", id, ports.ErrUnknownExecution)
	}
	if f.Ended() {
		return nil
	}
	f.mu.Lock()
	f.stops = append(f.stops, grace)
	f.mu.Unlock()
	f.End(ports.ExecExited, StoppedExit)
	return nil
}

func (x *Executor) active(id ports.ExecID) (*FakeExecution, error) {
	f := x.Execution(id)
	if f == nil || f.Ended() {
		return nil, fmt.Errorf("execution %s: %w", id, ports.ErrUnknownExecution)
	}
	return f, nil
}

// FakeExecution is one execution of the fake executor. It is safe for
// concurrent use.
type FakeExecution struct {
	ID ports.ExecID
	// Spec is what it was started with.
	Spec ports.ExecSpec

	caps ports.Capabilities

	mu        sync.Mutex
	deadline  time.Time
	deadlines []time.Time
	inputs    []string
	stops     []time.Duration
	ended     bool
	queued    []ports.ExecEvent
	closed    bool
	wake      chan struct{}
}

// Emit queues an event, such as activity, a session id or usage. Use
// End for the last event. It panics after End.
func (f *FakeExecution) Emit(ev ports.ExecEvent) {
	if ev.Final() {
		panic("testkit.FakeExecution: use End for the last event")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		panic("testkit.FakeExecution: Emit after End")
	}
	f.pushLocked(ev)
}

// EmitLastMessage queues an ExecLastMessage event with text, as when the
// agent gives a new message. It panics after End.
func (f *FakeExecution) EmitLastMessage(text string) {
	f.Emit(ports.ExecEvent{Kind: ports.ExecLastMessage, Message: text})
}

// End queues the last event, of kind ports.ExecExited or
// ports.ExecEndedAtDeadline, with exit, and closes the events. Ending an
// ended execution does nothing.
func (f *FakeExecution) End(kind ports.ExecEventKind, exit ports.Exit) {
	if kind != ports.ExecExited && kind != ports.ExecEndedAtDeadline {
		panic("testkit.FakeExecution: End takes exited or ended_at_deadline")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return
	}
	f.ended = true
	f.pushLocked(ports.ExecEvent{Kind: kind, Exit: exit})
	f.closed = true
	f.signal()
}

// Ended reports whether End was called, or Stop.
func (f *FakeExecution) Ended() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended
}

// Deadline is the deadline in force: the start's, or the last one set.
// The zero time means none.
func (f *FakeExecution) Deadline() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deadline
}

// Deadlines are the deadlines set after the start, in order.
func (f *FakeExecution) Deadlines() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.deadlines...)
}

// Inputs are the messages given as live input, in order.
func (f *FakeExecution) Inputs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...)
}

// Stops are the graces of the Stop calls.
func (f *FakeExecution) Stops() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.stops...)
}

func (f *FakeExecution) push(ev ports.ExecEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushLocked(ev)
}

func (f *FakeExecution) pushLocked(ev ports.ExecEvent) {
	f.queued = append(f.queued, ev)
	f.signal()
}

func (f *FakeExecution) signal() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// send gives the queued events to out, in order, so that Emit never
// waits for the reader, and closes out after the last one.
func (f *FakeExecution) send(out chan<- ports.ExecEvent) {
	defer close(out)
	for {
		f.mu.Lock()
		items, closed := f.queued, f.closed
		f.queued = nil
		f.mu.Unlock()
		for _, ev := range items {
			out <- ev
		}
		if len(items) == 0 {
			if closed {
				return
			}
			<-f.wake
		}
	}
}
