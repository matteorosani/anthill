package app

import (
	"context"
	"fmt"

	"github.com/matteorosani/anthill/internal/ports"
)

// OutboxHandler handles one change event: eligibility, change reaction,
// the shepherd or a command (design D7).
//
// Delivery is at least once: a crash after the handler's effects but
// before the event is marked done hands the same event out again after
// restart. A handler must therefore be idempotent, keyed by the event's
// outbox ID: local writes record it (as ports.StoredChange.Source does)
// and provider writes use it as the marker ID, so that a repeat finds its
// earlier comment. Together this gives exactly-once handling.
type OutboxHandler func(ctx context.Context, e ports.OutboxEvent) error

// OutboxDispatcher hands the outbox's pending events to a handler in
// detection order and marks each one done after it is handled.
type OutboxDispatcher struct {
	outbox ports.Outbox
	clock  ports.Clock
	handle OutboxHandler
}

// NewOutboxDispatcher returns a dispatcher over outbox.
func NewOutboxDispatcher(outbox ports.Outbox, clock ports.Clock, handle OutboxHandler) *OutboxDispatcher {
	return &OutboxDispatcher{outbox: outbox, clock: clock, handle: handle}
}

// Drain handles every pending event, the ones left by an earlier run of
// the daemon included, and returns how many it handled. It stops at the
// first error, so that later events of the same item are not handled
// before an earlier one; that event and the ones after it stay pending
// for the next Drain.
func (d *OutboxDispatcher) Drain(ctx context.Context) (int, error) {
	pending, err := d.outbox.Pending(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the outbox: %w", err)
	}
	for i, e := range pending {
		if err := d.handle(ctx, e); err != nil {
			return i, fmt.Errorf("handle outbox event %d (%s on %s): %w", e.ID, e.Event.Kind, e.Event.Ref.Full(), err)
		}
		if err := d.outbox.Done(ctx, e.ID, d.clock.Now()); err != nil {
			return i, fmt.Errorf("mark outbox event %d done: %w", e.ID, err)
		}
	}
	return len(pending), nil
}
