package app

import (
	"context"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Ants lists the current ant of every item, oldest first: those that are
// not final, or all of them with all set (spec queen-console,
// "Monitoring ants").
func (c *Console) Ants(ctx context.Context, all bool) ([]AntView, error) {
	recs, err := c.deps.Store.Ants(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the ants: %w", err)
	}
	now := c.Now()
	out := []AntView{}
	for _, rec := range recs {
		if all || !rec.State().Final() {
			out = append(out, view(rec, now))
		}
	}
	return out, nil
}

// AntDetail is one ant with every transition and run (spec
// ant-execution, "Ant states": every transition is visible in the queen
// console).
type AntDetail struct {
	AntView
	History []domain.Transition
	Runs    []ports.RunRecord
}

// Ant shows the item's current ant in full.
func (c *Console) Ant(ctx context.Context, item domain.Ref) (AntDetail, error) {
	rec, err := c.currentAnt(ctx, item)
	if err != nil {
		return AntDetail{}, err
	}
	runs, err := c.deps.Store.Runs(ctx, rec.ID)
	if err != nil {
		return AntDetail{}, fmt.Errorf("reading the runs of ant %d: %w", rec.ID, err)
	}
	return AntDetail{AntView: view(rec, c.Now()), History: rec.History, Runs: runs}, nil
}

// NoActiveRunError is a follow of an ant that has no active run.
type NoActiveRunError struct {
	Item  domain.Ref
	State domain.AntState
}

func (e *NoActiveRunError) Error() string {
	return fmt.Sprintf("the ant for %s has no active run (it is %s)", ItemName(e.Item), e.State)
}

// Follow follows the activity of the item's active run until the run
// ends or the follower detaches (spec queen-console, "Follow an ant").
// Following and detaching never affect the run.
func (c *Console) Follow(ctx context.Context, item domain.Ref) (*Follower, AntView, error) {
	rec, err := c.currentAnt(ctx, item)
	if err != nil {
		return nil, AntView{}, err
	}
	// Follow first, then check: a run that ends in between still sends
	// its end to the follower.
	f := c.deps.Activity.Follow(rec.ID)
	if rec, err = c.deps.Store.Ant(ctx, rec.ID); err != nil {
		f.Detach()
		return nil, AntView{}, fmt.Errorf("reading ant %d: %w", rec.ID, err)
	}
	if !rec.State().HasActiveRun() {
		f.Detach()
		return nil, AntView{}, &NoActiveRunError{Item: item, State: rec.State()}
	}
	return f, view(rec, c.Now()), nil
}

// Stop stops the item's ant (spec ant-execution, "Stopping an ant").
func (c *Console) Stop(ctx context.Context, item domain.Ref) (AntView, error) {
	return c.act(ctx, item, func(id ports.AntID) error { return c.deps.Ants.Stop(ctx, id, "") })
}

// Abandon abandons the item's ant (spec ant-execution, "Abandoning an
// ant").
func (c *Console) Abandon(ctx context.Context, item domain.Ref) (AntView, error) {
	return c.act(ctx, item, func(id ports.AntID) error { return c.deps.Ants.Abandon(ctx, id, "") })
}

// Retry retries the item's failed or stopped ant.
func (c *Console) Retry(ctx context.Context, item domain.Ref) (AntView, error) {
	return c.act(ctx, item, func(id ports.AntID) error { return c.deps.Ants.Retry(ctx, id) })
}

// Continue continues the item's timed-out ant with a new budget; 0 is
// the ant's base budget (spec ant-timebox, "Queen continues a timed-out
// ant").
func (c *Console) Continue(ctx context.Context, item domain.Ref, budget time.Duration) (AntView, error) {
	return c.act(ctx, item, func(id ports.AntID) error { return c.deps.Ants.Continue(ctx, id, budget) })
}

// act carries out a queen action on the item's current ant and returns
// the ant after it.
func (c *Console) act(ctx context.Context, item domain.Ref, action func(ports.AntID) error) (AntView, error) {
	rec, err := c.currentAnt(ctx, item)
	if err != nil {
		return AntView{}, err
	}
	if err := action(rec.ID); err != nil {
		return AntView{}, onItem(item, err)
	}
	if rec, err = c.deps.Store.Ant(ctx, rec.ID); err != nil {
		return AntView{}, fmt.Errorf("reading ant %d: %w", rec.ID, err)
	}
	return view(rec, c.Now()), nil
}

// CleanupRefusedError is a clean up of an ant that is not final.
type CleanupRefusedError struct {
	Item  domain.Ref
	State domain.AntState
}

func (e *CleanupRefusedError) Error() string {
	return fmt.Sprintf("cannot clean up the ant for %s: it is %s; abandon it first", ItemName(e.Item), e.State)
}

// Cleanup removes the worktree and local branch of the item's final ant
// (spec branching-policy, "Worktree cleanup"). Unpushed commits or
// uncommitted changes need confirm, or it returns a
// *NeedsConfirmationError and removes nothing ("Unpushed work").
//
// A done ant counts as having its PR closed: the remote branch is kept,
// because whether the PR was merged is not recorded yet.
func (c *Console) Cleanup(ctx context.Context, item domain.Ref, confirm bool) (domain.CleanupPlan, error) {
	rec, err := c.currentAnt(ctx, item)
	if err != nil {
		return domain.CleanupPlan{}, err
	}
	if !rec.State().Final() {
		return domain.CleanupPlan{}, &CleanupRefusedError{Item: item, State: rec.State()}
	}
	if rec.Worktree == "" && rec.Branch == "" {
		return domain.CleanupPlan{}, nil
	}
	tw, err := c.deps.Route(ctx, item)
	if err != nil {
		return domain.CleanupPlan{}, fmt.Errorf("finding the clone of %s: %w", ItemName(item), err)
	}
	req := CleanupRequest{
		Clone: tw.Clone, Branch: rec.Branch, Worktree: rec.Worktree,
		Abandoned: rec.State() == domain.AntAbandoned, Requested: true, Confirmed: confirm,
	}
	if rec.State() == domain.AntDone {
		req.PR = domain.PRClosed
	}
	return c.deps.Workspace.Cleanup(ctx, req)
}
