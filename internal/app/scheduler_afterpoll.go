package app

import "context"

// SetAfterPoll sets fn to be called after each poll of a connection that
// did not fail on the store, before the wait for the next one. The
// daemon uses it to handle the poll's change events, re-evaluate
// eligibility and trigger the shepherd (spec task-eligibility, "Proposal
// list"). Set it before Run; the connections' loops call it
// concurrently.
func (s *Scheduler) SetAfterPoll(fn func(ctx context.Context, conn Connection, report PollReport)) {
	s.afterPoll = fn
}
