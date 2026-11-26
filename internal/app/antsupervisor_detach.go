package app

import (
	"context"

	"github.com/matteorosani/anthill/internal/ports"
)

// Detach lets the daemon stop without waiting for its agents. The
// supervisor stops handling the events of active runs, so a run that ends
// from now on is not recorded: its ant keeps its active run in the store,
// and the next start's Reconcile handles it as a run the restart
// interrupted, or resumes it with auto-resume (spec ant-execution,
// "Restart resilience"). Queued ants are no longer started when a slot
// is freed; Reconcile starts them too.
//
// Work already in progress, such as handling a run that has just ended,
// finishes. Wait returns once it has, after which the supervisor uses
// the store no more, so the daemon can close it. Detach does not end
// agent processes. Calling it again does nothing.
func (s *AntSupervisor) Detach() {
	s.detachOnce.Do(func() { close(s.detached) })
}

// isDetached reports whether Detach was called.
func (s *AntSupervisor) isDetached() bool {
	select {
	case <-s.detached:
		return true
	default:
		return false
	}
}

// reportUnlessDone hands err to Errors, unless ctx is done: work that
// runs with a caller's ctx, such as the daemon's run loop, fails when the
// daemon stops, and that failure is the stop's doing.
func (s *AntSupervisor) reportUnlessDone(ctx context.Context, err error) {
	if ctx.Err() == nil {
		s.report(err)
	}
}

// dropEvents reads events until the executor closes them, after Detach. The
// supervisor does not wait for it: it ends when the agent's process does.
func dropEvents(events <-chan ports.ExecEvent) {
	for range events { //nolint:revive // The events are dropped on purpose.
	}
}
