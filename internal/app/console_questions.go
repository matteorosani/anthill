package app

import (
	"context"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// QuestionView is a pending question as the console lists it (spec
// queen-console, "Controlling ants").
type QuestionView struct {
	Item domain.Ref
	// On is the ticket or PR the question was posted on.
	On      domain.Ref
	Text    string
	AskedAt time.Time
	// Stale is set once the question was reported stale (spec
	// agent-conversation, "Question timeout").
	Stale bool
}

// Questions lists the pending questions of this machine's ants, oldest
// first.
func (c *Console) Questions(ctx context.Context) ([]QuestionView, error) {
	pending, err := c.deps.Store.PendingQuestions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the pending questions: %w", err)
	}
	out := []QuestionView{}
	for _, q := range pending {
		rec, err := c.deps.Store.Ant(ctx, q.Ant)
		if err != nil {
			return nil, fmt.Errorf("reading ant %d: %w", q.Ant, err)
		}
		if rec.State() != domain.AntAwaitingInput {
			continue
		}
		out = append(out, QuestionView{Item: rec.Item, On: q.On, Text: q.Text, AskedAt: q.AskedAt, Stale: !q.StaleAt.IsZero()})
	}
	return out, nil
}

// Answer answers the pending question of the item's ant with the queen's
// text (spec queen-console, "Answer from the console"), and returns the
// comment posted.
func (c *Console) Answer(ctx context.Context, item domain.Ref, text string) (domain.Comment, error) {
	rec, err := c.currentAnt(ctx, item)
	if err != nil {
		return domain.Comment{}, err
	}
	return c.deps.Answers.AnswerFromConsole(ctx, rec.ID, text)
}

// ExtensionView is an open extension request as the console lists it.
type ExtensionView struct {
	Item    domain.Ref
	Request domain.ExtensionRequest
	// Remaining is the ant's time left at the console's now.
	Remaining time.Duration
}

// Extensions lists the extension requests waiting for the queen.
func (c *Console) Extensions(ctx context.Context) ([]ExtensionView, error) {
	pending, err := c.deps.Store.PendingExtensions(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the extension requests: %w", err)
	}
	now := c.Now()
	out := []ExtensionView{}
	for _, x := range pending {
		rec, err := c.deps.Store.Ant(ctx, x.Ant)
		if err != nil {
			return nil, fmt.Errorf("reading ant %d: %w", x.Ant, err)
		}
		out = append(out, ExtensionView{Item: rec.Item, Request: x.Request, Remaining: rec.Budget.Remaining(now)})
	}
	return out, nil
}

// ExtensionDecision is the queen's decision on an extension request.
type ExtensionDecision string

// The decisions: approve the minutes asked, grant fewer, or deny.
const (
	ExtensionApprove ExtensionDecision = "approve"
	ExtensionReduce  ExtensionDecision = "reduce"
	ExtensionDeny    ExtensionDecision = "deny"
)

// NoOpenExtensionError is a decision on an ant without an open extension
// request.
type NoOpenExtensionError struct {
	Item domain.Ref
}

func (e *NoOpenExtensionError) Error() string {
	return "the ant for " + ItemName(e.Item) + " has no open extension request"
}

// DecideExtension applies the queen's decision on the open extension
// request of the item's ant (spec ant-timebox, "Extension requests"):
// approve grants the minutes asked, reduce grants minutes, fewer than
// asked, and deny grants none.
func (c *Console) DecideExtension(ctx context.Context, item domain.Ref, d ExtensionDecision, minutes int) (domain.ExtensionRequest, error) {
	rec, err := c.currentAnt(ctx, item)
	if err != nil {
		return domain.ExtensionRequest{}, err
	}
	open := rec.Budget.Open
	if open == nil {
		return domain.ExtensionRequest{}, &NoOpenExtensionError{Item: item}
	}
	switch d {
	case ExtensionApprove:
		minutes = open.Minutes
	case ExtensionReduce:
		if minutes <= 0 || minutes >= open.Minutes {
			return domain.ExtensionRequest{}, fmt.Errorf("a reduced extension must be between 1 and %d minutes, fewer than the %d asked", open.Minutes-1, open.Minutes)
		}
	case ExtensionDeny:
		minutes = 0
	default:
		return domain.ExtensionRequest{}, fmt.Errorf("unknown extension decision %q", d)
	}
	return c.deps.Ants.DecideExtension(ctx, rec.ID, minutes)
}

// FollowUpView is a follow-up proposal waiting for the queen (spec
// change-reaction, "Follow-ups start automatically by default").
type FollowUpView struct {
	Item domain.Ref
	PR   *domain.Ref
	// Changes are the stored changes the follow-up run would get.
	Changes []domain.ChangeEvent
}

// FollowUps lists the follow-up proposals waiting for the queen.
func (c *Console) FollowUps(ctx context.Context) ([]FollowUpView, error) {
	recs, err := c.deps.Store.Ants(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the ants: %w", err)
	}
	out := []FollowUpView{}
	for _, rec := range recs {
		if rec.FollowUpProposal != domain.ProposalPending || rec.State().Final() {
			continue
		}
		stored, err := c.deps.Store.StoredChanges(ctx, rec.ID)
		if err != nil {
			return nil, fmt.Errorf("reading the stored changes of ant %d: %w", rec.ID, err)
		}
		v := FollowUpView{Item: rec.Item, PR: rec.PR}
		for _, s := range stored {
			v.Changes = append(v.Changes, s.Event)
		}
		out = append(out, v)
	}
	return out, nil
}

// NoFollowUpProposalError is a decision on an ant without a pending
// follow-up proposal.
type NoFollowUpProposalError struct {
	Item domain.Ref
}

func (e *NoFollowUpProposalError) Error() string {
	return "the ant for " + ItemName(e.Item) + " has no follow-up proposal"
}

// AcceptFollowUp accepts the follow-up proposal of the item's ant; its
// run starts, or the ant is queued while capacity is full.
func (c *Console) AcceptFollowUp(ctx context.Context, item domain.Ref) (AntView, error) {
	return c.followUp(ctx, item, c.deps.Ants.AcceptFollowUp)
}

// DeclineFollowUp declines the follow-up proposal of the item's ant; its
// stored changes are kept for the next one.
func (c *Console) DeclineFollowUp(ctx context.Context, item domain.Ref) (AntView, error) {
	return c.followUp(ctx, item, c.deps.Ants.DeclineFollowUp)
}

func (c *Console) followUp(ctx context.Context, item domain.Ref, decide func(context.Context, ports.AntID) error) (AntView, error) {
	return c.act(ctx, item, func(id ports.AntID) error {
		rec, err := c.deps.Store.Ant(ctx, id)
		if err != nil {
			return fmt.Errorf("reading ant %d: %w", id, err)
		}
		if rec.FollowUpProposal != domain.ProposalPending {
			return &NoFollowUpProposalError{Item: item}
		}
		return decide(ctx, id)
	})
}
