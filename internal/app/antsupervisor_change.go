package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The routing of actionable changes (spec change-reaction): the change
// reactor hands each actionable change to RouteChange, and the playbook
// decides whether it is delivered to the active run or stored for the
// ant's next run. The follow-up run or proposal for the stored changes
// comes at the next trigger, so that the changes found in one poll go to
// one run.

// Holds reports whether an item carries its repository's hold label
// (spec change-reaction, "Hold freezes the ant"). ItemHolds implements
// it.
type Holds interface {
	OnHold(ctx context.Context, item domain.Ref) (bool, error)
}

// ReasonOnHold is why a queen action is refused, or a run not started,
// while the item carries the hold label.
const ReasonOnHold = "item is on hold"

// routing holds the changes of one RouteChange call that are not routed
// yet. Each routing decision's handler takes the first one off, which is
// the decision's effect on the snapshot.
type routing struct {
	changes []domain.AntChange
}

// RouteChange routes one actionable change of the ant's item or ant PR
// (spec change-reaction, "Delivery to an active run", "Changes while the
// ant is between runs"): the playbook delivers it to an active run that
// has not finished, stores it otherwise, and drops it for a final ant.
// It carries out only the routing: the run or the proposal due for the
// stored changes comes at the next Trigger. c.ID is the outbox event the
// change came from, so a repeated event is stored once.
func (s *AntSupervisor) RouteChange(ctx context.Context, id ports.AntID, c domain.AntChange) error {
	defer s.lockAnt(id)()
	return s.run(ctx, id, &routing{changes: []domain.AntChange{c}})
}

// next takes the first change off, once the decision for it has its
// effect.
func (r *routing) next() {
	if r != nil && len(r.changes) > 0 {
		r.changes = r.changes[1:]
	}
}

// deliverChange carries out a DeliverChange decision: the change goes to
// the active run as a message, by the means its runner supports (design
// D5). While the item is on hold, or when the run has ended meanwhile,
// it is stored instead, as for an ant between runs.
func (s *AntSupervisor) deliverChange(ctx context.Context, t *antTurn, d domain.DeliverChange) (bool, error) {
	held, err := s.onHold(ctx, t.rec.Item)
	if err != nil {
		return false, err
	}
	if !held && s.deps.Messages != nil {
		_, err := s.deps.Messages.Deliver(ctx, t.rec.ID, domain.RenderChangeMessage(d.Change))
		switch {
		case err == nil:
			t.route.next()
			return true, nil
		case !errors.Is(err, ErrNoActiveRun):
			return false, fmt.Errorf("delivering a change to the run of %s: %w", t.rec.Item, err)
		}
	}
	return s.storeChange(ctx, t, domain.StoreChange{Change: d.Change})
}

// storeChange carries out a StoreChange decision: the change is kept for
// the ant's next run, a declined follow-up proposal is cleared so that
// the stored changes can be proposed again, and the queen is told.
func (s *AntSupervisor) storeChange(ctx context.Context, t *antTurn, d domain.StoreChange) (bool, error) {
	rec, c := t.rec, d.Change
	err := s.store.StoreChange(ctx, ports.StoredChange{Ant: rec.ID, Source: ports.OutboxID(c.ID), Key: findingsKey(c), Event: c.Event, Findings: c.Findings, StoredAt: s.clock.Now()})
	if err != nil {
		return false, fmt.Errorf("storing a change for ant %d: %w", rec.ID, err)
	}
	if rec.FollowUpProposal == domain.ProposalDeclined {
		rec.FollowUpProposal = domain.ProposalNone
		if err := s.store.SaveAnt(ctx, rec); err != nil {
			return false, fmt.Errorf("storing ant %d: %w", rec.ID, err)
		}
	}
	t.route.next()
	switch {
	case d.FollowUpLimitReached:
		s.notifyAnt(ctx, domain.NotifyFollowUpLimit, rec.Item, "Follow-up limit reached",
			"a change arrived, but the PR has had its follow-up runs; it is kept for the queen: "+domain.DescribeChange(c.Event))
	case rec.State() != domain.AntSucceeded:
		// A succeeded ant's change brings a follow-up run or proposal,
		// which the queen is told about instead.
		s.notifyAnt(ctx, domain.NotifyChangeStored, rec.Item, "Change stored",
			"kept for the ant's next run: "+domain.DescribeChange(c.Event))
	}
	return true, nil
}

// proposeFollowUp carries out a ProposeFollowUp decision: the queen is
// offered a follow-up run for the stored changes, which starts once the
// queen accepts (spec change-reaction, "Auto-follow-up disabled"). Nothing is
// proposed while the item is on hold.
func (s *AntSupervisor) proposeFollowUp(ctx context.Context, t *antTurn, d domain.ProposeFollowUp) (bool, error) {
	held, err := s.onHold(ctx, t.rec.Item)
	if err != nil || held {
		return false, err
	}
	rec := t.rec
	rec.FollowUpProposal = domain.ProposalPending
	if err := s.store.SaveAnt(ctx, rec); err != nil {
		return false, fmt.Errorf("storing ant %d: %w", rec.ID, err)
	}
	s.notifyAnt(ctx, domain.NotifyFollowUpProposed, rec.Item, "Follow-up proposed", describeChanges(d.Changes))
	return true, nil
}

// AcceptFollowUp accepts the ant's pending follow-up proposal: its run
// starts, or waits for capacity like any due run.
func (s *AntSupervisor) AcceptFollowUp(ctx context.Context, id ports.AntID) error {
	return s.answerProposal(ctx, id, "accept the follow-up of", domain.ProposalAccepted)
}

// DeclineFollowUp declines the ant's pending follow-up proposal. The
// stored changes are kept, and the next actionable change brings a new
// proposal for all of them.
func (s *AntSupervisor) DeclineFollowUp(ctx context.Context, id ports.AntID) error {
	return s.answerProposal(ctx, id, "decline the follow-up of", domain.ProposalDeclined)
}

func (s *AntSupervisor) answerProposal(ctx context.Context, id ports.AntID, action string, to domain.ProposalState) error {
	defer s.lockAnt(id)()
	rec, err := s.store.Ant(ctx, id)
	if err != nil {
		return fmt.Errorf("reading ant %d: %w", id, err)
	}
	if rec.FollowUpProposal != domain.ProposalPending || rec.State() != domain.AntSucceeded {
		return &ActionRefusedError{Ant: id, Action: action, State: rec.State(), Reason: "no follow-up proposal is pending"}
	}
	if to == domain.ProposalAccepted {
		if err := s.refuseHeld(ctx, rec, action); err != nil {
			return err
		}
	}
	rec.FollowUpProposal = to
	if err := s.store.SaveAnt(ctx, rec); err != nil {
		return fmt.Errorf("storing ant %d: %w", id, err)
	}
	return s.decide(ctx, id)
}

// Withdraw ends the ant's work because its item lost the opt-in label or
// was closed (spec change-reaction, "Withdrawal of opt-in"): any active
// run is ended, the worktree kept, the task claim released, the ant
// becomes abandoned with the reason (domain.Withdrawal), and the queen is told. An open ant
// PR is left to the developers. A final ant is left alone.
func (s *AntSupervisor) Withdraw(ctx context.Context, id ports.AntID, reason string) error {
	err := s.Abandon(ctx, id, reason)
	var refused *ActionRefusedError
	if errors.As(err, &refused) {
		return nil
	}
	rec, rerr := s.store.Ant(ctx, id)
	if rerr != nil {
		return errors.Join(err, fmt.Errorf("reading ant %d: %w", id, rerr))
	}
	s.notifyAnt(ctx, domain.NotifyClaimLost, rec.Item, "Ant abandoned", reason+": the claim is released and the worktree kept")
	return err
}

// refuseHeld refuses a queen action that would start a run while the
// item is on hold (spec change-reaction, "Retry while held").
func (s *AntSupervisor) refuseHeld(ctx context.Context, rec ports.AntRecord, action string) error {
	held, err := s.onHold(ctx, rec.Item)
	if err != nil {
		return err
	}
	if held {
		return &ActionRefusedError{Ant: rec.ID, Action: action, State: rec.State(), Reason: ReasonOnHold}
	}
	return nil
}

// onHold reports whether the item carries the hold label. Without Holds
// no item is.
func (s *AntSupervisor) onHold(ctx context.Context, item domain.Ref) (bool, error) {
	if s.deps.Holds == nil {
		return false, nil
	}
	held, err := s.deps.Holds.OnHold(ctx, item)
	if err != nil {
		return false, fmt.Errorf("checking the hold label of %s: %w", item, err)
	}
	return held, nil
}

// describeChanges describes the changes of a follow-up run or proposal
// for the queen.
func describeChanges(changes []domain.AntChange) string {
	switch len(changes) {
	case 0:
		return "no change"
	case 1:
		return domain.DescribeChange(changes[0].Event)
	}
	return fmt.Sprintf("%s, and %d more", domain.DescribeChange(changes[0].Event), len(changes)-1)
}

// findingsKey is the key of a change that hands a round's findings to
// the ant, so that it is stored once: the round's summary comment. Other
// changes have none and are stored once by their outbox event.
func findingsKey(c domain.AntChange) string {
	if len(c.Findings) == 0 {
		return ""
	}
	return "findings:" + c.Event.Ref.Full() + ":" + c.Event.CommentID
}
