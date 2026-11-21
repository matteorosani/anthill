package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ClosedPRAnts is the part of ant supervision that PR closure uses;
// AntSupervisor implements it.
type ClosedPRAnts interface {
	PRClosed(ctx context.Context, id ports.AntID, pr domain.PullRequest) error
}

// ClosedPRShepherd is the part of the shepherd that PR closure uses;
// Shepherd implements it.
type ClosedPRShepherd interface {
	ActivePRs() []domain.Ref
	StopPR(ctx context.Context, pr domain.Ref) error
}

var (
	_ ClosedPRAnts     = (*AntSupervisor)(nil)
	_ ClosedPRShepherd = (*Shepherd)(nil)
)

// PRClosureDeps are the ports and services PR closure uses.
type PRClosureDeps struct {
	Store    ports.SessionIndex
	CodeHost CodeHostRoute
	Ants     ClosedPRAnts
	Shepherd ClosedPRShepherd
}

// PRClosure notices the PRs that developers merged or closed (spec
// pr-shepherd, "PR closure is human"). A poll lists only open PRs, so a
// PR that ended just leaves the list: after each poll, the daemon calls
// Check, which reads afresh the PRs this instance has work on.
type PRClosure struct {
	deps PRClosureDeps
}

// NewPRClosure returns PR closure.
func NewPRClosure(deps PRClosureDeps) *PRClosure {
	return &PRClosure{deps: deps}
}

// Check reads each PR this instance has work on, once: the PRs of its
// ants that are not final, and those with a round or fix pass in
// progress. For each one merged or closed, the shepherd stops all
// activity on it, and its ant on this instance becomes done, with its
// task claim released and its worktree cleaned up. A PR that cannot be
// read is reported and left to the next check, and the others are
// still checked.
func (c *PRClosure) Check(ctx context.Context) error {
	ants, err := c.deps.Store.Ants(ctx)
	if err != nil {
		return fmt.Errorf("listing the ants: %w", err)
	}
	owner := map[domain.Ref]ports.AntID{}
	var prs []domain.Ref
	for _, a := range ants {
		if a.PR == nil || a.State().Final() {
			continue
		}
		if _, seen := owner[*a.PR]; !seen {
			prs = append(prs, *a.PR)
		}
		owner[*a.PR] = a.ID
	}
	for _, pr := range c.deps.Shepherd.ActivePRs() {
		if _, seen := owner[pr]; !seen {
			owner[pr] = 0
			prs = append(prs, pr)
		}
	}
	var errs []error
	for _, ref := range prs {
		if err := c.check(ctx, ref, owner[ref]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// check handles one PR, and its ant when ant is not 0.
func (c *PRClosure) check(ctx context.Context, ref domain.Ref, ant ports.AntID) error {
	host, err := c.deps.CodeHost(ctx, ref.Repo)
	if err != nil {
		return err
	}
	pr, _, err := host.PullRequest(ctx, ref)
	if err != nil {
		return fmt.Errorf("reading %s: %w", ref, err)
	}
	if !pr.State.Ended() {
		return nil
	}
	errs := []error{c.deps.Shepherd.StopPR(ctx, ref)}
	if ant != 0 {
		errs = append(errs, c.deps.Ants.PRClosed(ctx, ant, pr))
	}
	return errors.Join(errs...)
}
