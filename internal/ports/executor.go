package ports

import (
	"context"
	"errors"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// ExecID identifies one agent process that an Executor runs: a run, a
// review or fix agent, or a comment classifier call. It is unique within
// one executor.
type ExecID string

// ExecSpec is what the executor starts (design D21).
type ExecSpec struct {
	// Runner is the runner's name, such as claude-code.
	Runner domain.Runner
	// Session is the session to resume, or empty for a new session
	// (design D17).
	Session string
	// Run is what the runner starts with. A short call, such as a comment
	// classifier call (design D8), may leave out what its runner does not
	// need, such as the MCP command or the worktree.
	Run RunSpec
	// Deadline is when the executor ends the process tree: the budget
	// plus the grace period, which the timekeeper computes (spec
	// ant-timebox). The zero time means no deadline.
	Deadline time.Time
}

// Execution is a started agent process: its id, what its runner
// supports, and its events.
type Execution struct {
	ID           ExecID
	Capabilities Capabilities
	// Events has the process's events in order. The first is ExecStarted,
	// and the last is ExecExited or ExecEndedAtDeadline, after which the
	// channel is closed. The executor never holds up the process for a
	// slow reader: events wait in a queue until they are read, and what
	// waits is bounded. Started and the last event are never dropped.
	// The latest usage and session id are never lost, but unread
	// intermediate values may be merged into the latest one. Activity
	// lines are dropped when too many wait unread: the oldest go, and
	// one activity event in their place says how many.
	Events <-chan ExecEvent
}

// ExecEventKind says what an ExecEvent reports.
type ExecEventKind string

// The kinds of ExecEvent.
const (
	// ExecStarted: the process has started.
	ExecStarted ExecEventKind = "started"
	// ExecSessionID: the runner reported the session id, in SessionID.
	// It comes again if the id changes.
	ExecSessionID ExecEventKind = "session_id"
	// ExecActivity: one line of the agent's activity, in Line.
	ExecActivity ExecEventKind = "activity"
	// ExecUsage: what the runner reported so far changed, in Usage.
	ExecUsage ExecEventKind = "usage"
	// ExecExited: the process ended on its own or was stopped, with Exit
	// and the final Usage.
	ExecExited ExecEventKind = "exited"
	// ExecEndedAtDeadline: the executor ended the process tree at its
	// deadline, with Exit and the final Usage.
	ExecEndedAtDeadline ExecEventKind = "ended_at_deadline"
)

// ExecEvent is one event of an execution. Kind says which fields are
// set.
type ExecEvent struct {
	Kind ExecEventKind
	// At is when the executor saw it, by its clock.
	At        time.Time
	SessionID string
	Line      string
	Usage     Usage
	Exit      Exit
}

// Final reports whether the event is the last of its execution.
func (e ExecEvent) Final() bool {
	return e.Kind == ExecExited || e.Kind == ExecEndedAtDeadline
}

// ErrUnknownExecution is returned for an id that the executor never
// gave, and by Send and SetDeadline once the process has ended.
var ErrUnknownExecution = errors.New("unknown or ended execution")

// Executor starts and ends agent processes and reports their events
// (design D21). Its API carries only plain values: a spec in, events
// out, messages, deadlines and stop requests in. It computes no budget
// and sends no notice of its own: the warning and the notice to stop
// come from the timekeeper as messages (design D5).
//
// Send, SetDeadline and Stop return ErrUnknownExecution for an id the
// executor never gave; Send and SetDeadline also return it once the
// process has ended.
type Executor interface {
	// Start starts a process with spec, resuming spec.Session when it is
	// set. It returns ErrUnsupported for a resume the runner cannot do,
	// and the runner's own error when its CLI cannot be started
	// (AgentRunner.Available). When Start fails, nothing runs and no
	// events are sent.
	Start(ctx context.Context, spec ExecSpec) (Execution, error)
	// Send gives input to the process as live input. It returns
	// ErrUnsupported when the runner has no live input, and another
	// error when sending fails; the caller then delivers the message by
	// another means (design D5). A send that races the process's exit
	// may return such another error rather than ErrUnknownExecution.
	Send(ctx context.Context, id ExecID, input string) error
	// SetDeadline moves the deadline later or earlier; the zero time
	// clears it, as while request_extension waits. A deadline that has
	// passed ends the process at once. At the deadline the executor
	// starts ending the process tree, and kills it after a short fixed
	// grace of its own: the deadline is already the budget plus the
	// grace period of ant-timebox (design D5).
	SetDeadline(id ExecID, deadline time.Time) error
	// Stop ends the process tree, killing it after grace. It returns
	// once the process has ended; its last event is then ExecExited, or
	// ExecEndedAtDeadline when the deadline came first. Stopping a
	// process that has already ended returns nil, so a stop that races
	// the process's exit does not fail.
	Stop(id ExecID, grace time.Duration) error
}
