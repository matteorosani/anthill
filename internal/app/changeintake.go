package app

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ChangeInput is one change event as the change reactor's intake hands it on
// to command and answer handling (agent-conversation, change-reaction).
type ChangeInput struct {
	// Outbox is the change's outbox id, the idempotency key when an event is
	// delivered again after a restart.
	Outbox ports.OutboxID
	// Event is the change itself.
	Event domain.ChangeEvent
	// Item is the ticket: the event's ref, or the item of the ant PR the
	// event is on.
	Item domain.Ref
	// Ant is this instance's current ant for Item when it is not final, nil
	// when there is none.
	Ant *ports.AntRecord
	// Responsible reports whether this instance holds Item's task claim, live
	// or parked.
	Responsible bool
}

// CommandResult is what ConversationIntake.HandleCommands did with a change.
type CommandResult struct {
	// Handled is true when the comment held `/anthill` lines. The intake
	// then stops, unless Route is also true.
	Handled bool
	// Route asks the intake to route the change as an actionable change for
	// Item's ant, as for `/anthill fix` on a thread of an ant PR.
	Route bool
}

// ConversationIntake is the part of the change reactor's intake that handles
// commands and answers. The intake calls HandleCommands first, then
// HandleAnswer, then classifies relevance and routes the change.
type ConversationIntake interface {
	// HandleCommands handles the `/anthill` lines of a human comment. It is
	// called before the responsibility filter.
	HandleCommands(ctx context.Context, in ChangeInput) (CommandResult, error)
	// HandleAnswer reports whether the change answers a pending question of
	// in.Ant, or already answered it. When it returns true, the answer is
	// stored, the question is marked answered and the ant is triggered, and
	// the intake does not route the change. It is called only when
	// in.Responsible is true.
	HandleAnswer(ctx context.Context, in ChangeInput) (bool, error)
}
