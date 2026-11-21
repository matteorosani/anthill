package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

var _ ports.ClaimLossHandler = (*AntSupervisor)(nil)

// ClaimLost ends the active run of the ant whose task claim this instance
// lost or a developer force-released (spec work-claiming, "Holder returns
// after expiry" and "Manual override"). The ant becomes abandoned with
// the reason given, its open extension request is closed and its
// worktree is kept. Unlike Abandon, the claim is neither parked nor
// released: Claiming, which calls this, marks it released and tells the
// queen.
//
// The ant is made abandoned here, under its lock and before its run is
// stopped, so that the end of the run is never taken for an exit without
// finishing. An ant without an active run is left to Claiming, which
// makes it abandoned. Calls for other claim kinds, for items without an
// ant, or for final ants do nothing, so a repeated call is harmless.
func (s *AntSupervisor) ClaimLost(ctx context.Context, ref domain.Ref, kind domain.ClaimKind, reason string) error {
	if kind != domain.ClaimTask || ref.Kind != domain.KindItem {
		return nil
	}
	if reason == "" {
		reason = ReasonClaimLost
	}
	cur, err := s.store.CurrentAnt(ctx, ref)
	if errors.Is(err, ports.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the ant of %s: %w", ref, err)
	}
	unlock := s.lockAnt(cur.ID)
	rec, err := s.store.Ant(ctx, cur.ID)
	if err != nil {
		unlock()
		return fmt.Errorf("reading ant %d: %w", cur.ID, err)
	}
	if !rec.State().HasActiveRun() {
		unlock()
		return nil
	}
	exec := s.endingRun(rec.Item, ports.OutcomeStopped, reason)
	closed := rec.Budget.Open
	rec.Budget.Open = nil
	if _, err := s.transition(ctx, rec, domain.AntAbandoned, reason); err != nil {
		unlock()
		return err
	}
	err = s.closeExtension(ctx, rec.ID, closed, "closed: the claim was lost")
	unlock()
	s.stopRun(exec)
	return err
}
