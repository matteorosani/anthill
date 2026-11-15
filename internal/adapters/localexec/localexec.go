// Package localexec is the Executor that runs agents on this machine,
// over the runner adapters (design D21). Each process has one goroutine
// that waits for it to end, ends its tree at the deadline on the
// executor's own timer, and reports its events.
package localexec

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// DefaultStopGrace is how long a process ended at its deadline has to
// end on its own, such as to save its session, before its tree is
// killed.
const DefaultStopGrace = 10 * time.Second

// Options configure the executor.
type Options struct {
	// StopGrace is the grace of a process ended at its deadline; zero
	// means DefaultStopGrace.
	StopGrace time.Duration
}

// Executor runs agents on this machine. It is safe for concurrent use.
type Executor struct {
	clock   ports.Clock
	runners map[domain.Runner]ports.AgentRunner
	grace   time.Duration

	mu    sync.Mutex
	next  int64
	execs map[ports.ExecID]*execution
}

var _ ports.Executor = (*Executor)(nil)

// New returns an executor over runners, timing deadlines with clock.
func New(clock ports.Clock, runners map[domain.Runner]ports.AgentRunner, opts Options) *Executor {
	m := make(map[domain.Runner]ports.AgentRunner, len(runners))
	for name, r := range runners {
		m[name] = r
	}
	if opts.StopGrace <= 0 {
		opts.StopGrace = DefaultStopGrace
	}
	return &Executor{clock: clock, runners: m, grace: opts.StopGrace, execs: map[ports.ExecID]*execution{}}
}

// Start starts a process with the runner spec.Runner names.
func (e *Executor) Start(ctx context.Context, spec ports.ExecSpec) (ports.Execution, error) {
	runner, ok := e.runners[spec.Runner]
	if !ok {
		return ports.Execution{}, fmt.Errorf("executor: no runner %s", spec.Runner)
	}
	caps := runner.Capabilities()
	if spec.Session != "" && !caps.Resume {
		return ports.Execution{}, fmt.Errorf("executor: resume with %s: %w", spec.Runner, ports.ErrUnsupported)
	}

	e.mu.Lock()
	e.next++
	id := ports.ExecID("exec-" + strconv.FormatInt(e.next, 10))
	e.mu.Unlock()

	events := make(chan ports.ExecEvent)
	x := &execution{
		id:        id,
		caps:      caps,
		clock:     e.clock,
		grace:     e.grace,
		queue:     &queue{wake: make(chan struct{}, 1)},
		deadlines: make(chan deadlineRequest),
		done:      make(chan struct{}),
	}
	runSpec := spec.Run
	runSpec.Activity = x
	var (
		run ports.Run
		err error
	)
	if spec.Session != "" {
		run, err = runner.Resume(ctx, spec.Session, runSpec)
	} else {
		run, err = runner.Start(ctx, runSpec)
	}
	if err != nil {
		return ports.Execution{}, err
	}
	x.start(run, spec.Deadline, events)

	e.mu.Lock()
	e.execs[id] = x
	e.mu.Unlock()
	go func() {
		x.supervise()
		e.mu.Lock()
		delete(e.execs, id)
		e.mu.Unlock()
	}()
	return ports.Execution{ID: id, Capabilities: caps, Events: events}, nil
}

// Send gives input to the process as live input.
func (e *Executor) Send(ctx context.Context, id ports.ExecID, input string) error {
	x, err := e.find(id)
	if err != nil {
		return err
	}
	if !x.caps.LiveInput {
		return fmt.Errorf("live input: %w", ports.ErrUnsupported)
	}
	return x.run.Send(ctx, input)
}

// SetDeadline moves or clears the deadline. It returns once the new
// deadline is in force.
func (e *Executor) SetDeadline(id ports.ExecID, deadline time.Time) error {
	x, err := e.find(id)
	if err != nil {
		return err
	}
	req := deadlineRequest{at: deadline, set: make(chan struct{})}
	select {
	case x.deadlines <- req:
		<-req.set
		return nil
	case <-x.done:
		return unknown(id)
	}
}

// Stop ends the process tree, killing it after grace, and returns once
// the process has ended.
func (e *Executor) Stop(id ports.ExecID, grace time.Duration) error {
	x, err := e.find(id)
	if err != nil {
		return err
	}
	x.mu.Lock()
	x.stopping = true
	x.mu.Unlock()
	err = x.run.Stop(grace)
	<-x.done
	return err
}

func (e *Executor) find(id ports.ExecID) (*execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	x, ok := e.execs[id]
	if !ok {
		return nil, unknown(id)
	}
	return x, nil
}

// unknown is the error for an id that was never started or has ended.
func unknown(id ports.ExecID) error {
	return fmt.Errorf("execution %s: %w", id, ports.ErrUnknownExecution)
}

// deadlineRequest moves the deadline to at, or clears it when at is
// zero; set is closed once it is in force.
type deadlineRequest struct {
	at  time.Time
	set chan struct{}
}

// execution is one process. It is the run's activity output too.
type execution struct {
	id    ports.ExecID
	caps  ports.Capabilities
	clock ports.Clock
	grace time.Duration
	queue *queue
	run   ports.Run

	deadlines chan deadlineRequest
	// timer fires at the deadline; it is nil without one.
	timer <-chan time.Time
	// done is closed once the last event is queued.
	done chan struct{}

	mu       sync.Mutex
	started  bool
	stopping bool
	session  string
	usage    ports.Usage
}

// start records the run, queues the first events, starts sending them
// to events and sets the deadline. Activity written while the runner
// was starting comes after the started event.
func (x *execution) start(run ports.Run, deadline time.Time, events chan<- ports.ExecEvent) {
	x.mu.Lock()
	x.run = run
	x.started = true
	x.queue.pushFront(x.event(ports.ExecEvent{Kind: ports.ExecStarted}))
	x.changesLocked()
	x.mu.Unlock()
	go x.queue.send(events)
	x.arm(deadline)
}

// arm makes timer fire at deadline, or never when it is zero. A timer
// replaced here may still fire; it is no longer read.
func (x *execution) arm(deadline time.Time) {
	if deadline.IsZero() {
		x.timer = nil
		return
	}
	x.timer = x.clock.After(deadline.Sub(x.clock.Now()))
}

// supervise waits for the process to end, ending its tree at the
// deadline, and queues the last event.
func (x *execution) supervise() {
	exited := make(chan ports.Exit, 1)
	go func() { exited <- x.run.Wait() }()
	for {
		select {
		case exit := <-exited:
			x.finish(ports.ExecExited, exit)
			return
		case req := <-x.deadlines:
			x.arm(req.at)
			close(req.set)
		case <-x.timer:
			x.mu.Lock()
			stopping := x.stopping
			x.mu.Unlock()
			if stopping {
				// Stop is ending the process: it reports exited.
				x.timer = nil
				continue
			}
			_ = x.run.Stop(x.grace)
			x.finish(ports.ExecEndedAtDeadline, <-exited)
			return
		}
	}
}

// finish queues the last changes and the final event, then closes the
// events.
func (x *execution) finish(kind ports.ExecEventKind, exit ports.Exit) {
	x.mu.Lock()
	x.changesLocked()
	x.queue.push(x.event(ports.ExecEvent{Kind: kind, Exit: exit, Usage: x.run.Usage()}))
	x.mu.Unlock()
	x.queue.close()
	close(x.done)
}

// Write takes the run's activity: each line of p becomes an activity
// event, followed by the session id and usage when they changed. The
// runner writes each output line in one Write; standard error comes in
// pieces, and a line split between two of them becomes two events, so
// that it never mixes with an output line.
func (x *execution) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, line := range bytes.Split(p, []byte("\n")) {
		if line = bytes.TrimSuffix(line, []byte("\r")); len(line) > 0 {
			x.queue.push(x.event(ports.ExecEvent{Kind: ports.ExecActivity, Line: string(line)}))
		}
	}
	if x.started {
		x.changesLocked()
	}
	return len(p), nil
}

// changesLocked queues the session id and usage when they changed.
func (x *execution) changesLocked() {
	if s := x.run.SessionID(); s != x.session {
		x.session = s
		x.queue.push(x.event(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: s}))
	}
	if u := x.run.Usage(); !sameUsage(u, x.usage) {
		x.usage = u
		x.queue.push(x.event(ports.ExecEvent{Kind: ports.ExecUsage, Usage: u}))
	}
}

func (x *execution) event(ev ports.ExecEvent) ports.ExecEvent {
	ev.At = x.clock.Now()
	return ev
}

func sameUsage(a, b ports.Usage) bool {
	return equalPtr(a.Tokens, b.Tokens) && equalPtr(a.CostUSD, b.CostUSD) && a.CostIsSessionTotal == b.CostIsSessionTotal
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// queue holds events until they are read, so that a slow reader never
// holds up the process: its output is read while events wait here.
type queue struct {
	mu     sync.Mutex
	items  []ports.ExecEvent
	closed bool
	// wake has a value when items or closed changed.
	wake chan struct{}
}

func (q *queue) push(ev ports.ExecEvent) {
	q.mu.Lock()
	q.items = append(q.items, ev)
	q.mu.Unlock()
	q.signal()
}

// pushFront puts ev ahead of the events queued so far: the started event
// comes before activity written while the runner was starting.
func (q *queue) pushFront(ev ports.ExecEvent) {
	q.mu.Lock()
	q.items = append([]ports.ExecEvent{ev}, q.items...)
	q.mu.Unlock()
	q.signal()
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// send sends the events to out, in order, and closes out after the
// last one.
func (q *queue) send(out chan<- ports.ExecEvent) {
	defer close(out)
	for {
		q.mu.Lock()
		items, closed := q.items, q.closed
		q.items = nil
		q.mu.Unlock()
		for _, ev := range items {
			out <- ev
		}
		if closed && len(items) == 0 {
			return
		}
		if len(items) == 0 {
			<-q.wake
		}
	}
}
