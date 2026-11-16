package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// AcceptRequest is the queen accepting a proposal (spec queen-console,
// "Accepting and declining"). The zero overrides keep what the item's
// labels and the configuration say.
type AcceptRequest struct {
	Item domain.Ref
	// Runner overrides the runner.
	Runner domain.Runner
	// BudgetMinutes overrides the base budget (spec ant-timebox, "Base
	// budget").
	BudgetMinutes int
	// BaseBranch overrides the base branch.
	BaseBranch string
}

// IneligibleError is an accepted item that failed the re-check on fresh
// provider data (spec task-eligibility, "Re-check at acceptance"). Its
// message lists the unmet prerequisites, such as "no longer eligible:
// open".
type IneligibleError struct {
	Item   domain.Ref
	Result domain.EligibilityResult
}

func (e *IneligibleError) Error() string {
	return e.Result.IneligibleMessage()
}

// ReasonAccepted is the reason of a new ant's first transition when it
// starts at once.
const ReasonAccepted = "accepted"

// Accept creates the ant for an accepted item (spec ant-execution,
// "Starting an ant on acceptance"). It re-checks eligibility on fresh
// data and selects the runner; either failing refuses the acceptance and
// nothing is created. Then, when capacity allows, it acquires the task
// claim, creates the branch and worktree, and starts the first run with
// the task brief through the executor. A step that fails undoes the
// steps already done; a lost claim race is a *ClaimTakenError naming the
// holder. When capacity is full, the ant is created queued without a
// claim, and the rest happens when a slot frees (spec ant-execution,
// "Queued for capacity").
func (s *AntSupervisor) Accept(ctx context.Context, req AcceptRequest) (ports.AntID, error) {
	defer s.lockItem(req.Item)()
	tw, err := s.deps.Route(ctx, req.Item)
	if err != nil {
		return 0, fmt.Errorf("finding the workspace of %s: %w", req.Item, err)
	}
	o := acceptOverrides{Runner: req.Runner, BaseBranch: req.BaseBranch}
	c, err := s.check(ctx, req.Item, 0, tw, o)
	if err != nil {
		return 0, err
	}
	base := c.settings.Budget
	if req.BudgetMinutes > 0 {
		base = time.Duration(req.BudgetMinutes) * time.Minute
	}
	policy := c.settings.BudgetPolicy()

	if err := s.reserve(req.Item, tw); err != nil {
		if !errors.Is(err, errCapacityFull) {
			return 0, err
		}
		ant, err := domain.NewAnt(req.Item, domain.AntQueued, base, policy, s.clock.Now(), ReasonCapacityFull)
		if err != nil {
			return 0, err
		}
		// The overrides are kept with the queued ant for its start.
		rec := ports.AntRecord{Item: req.Item, Base: base, Budget: ant.Budget, History: ant.History(), Runner: string(c.runner), BaseBranch: req.BaseBranch}
		return s.store.CreateAnt(ctx, rec)
	}

	p, err := s.claimAndPrepare(ctx, c, tw)
	if err != nil {
		s.unreserve(req.Item)
		return 0, err
	}
	ant, err := domain.NewAnt(req.Item, domain.AntStarting, base, policy, s.clock.Now(), ReasonAccepted)
	if err != nil {
		s.unreserve(req.Item)
		return 0, errors.Join(err, p.undo(ctx))
	}
	rec := ports.AntRecord{
		Item: req.Item, Base: base, Budget: ant.Budget, History: ant.History(), Runner: string(c.runner),
		Worktree: p.ws.Worktree, Branch: p.ws.Branch, BaseBranch: p.ws.Base,
	}
	rec.ID, err = s.store.CreateAnt(ctx, rec)
	if err != nil {
		s.unreserve(req.Item)
		return 0, errors.Join(fmt.Errorf("storing the ant of %s: %w", req.Item, err), p.undo(ctx))
	}
	defer s.lockAnt(rec.ID)()
	if err := s.launch(ctx, rec, tw, p.ws, domain.StartRun{Kind: domain.RoleTaskRun, Brief: domain.BriefTask}); err != nil {
		s.unreserve(req.Item)
		return rec.ID, s.startFailed(ctx, rec, true, p.undo, err)
	}
	return rec.ID, nil
}

// acceptOverrides are the queen's overrides that the steps after the
// re-check use.
type acceptOverrides struct {
	Runner     domain.Runner
	BaseBranch string
}

// checked is an item that passed the re-check, with its settings and
// runner.
type checked struct {
	item     domain.WorkItem
	settings domain.Effective
	runner   domain.Runner
}

// check re-checks the item's eligibility on fresh data, ignoring the ant
// own that is being started, and selects its runner. Both come before
// any claim (spec ant-execution, "Runner not installed").
func (s *AntSupervisor) check(ctx context.Context, item domain.Ref, own ports.AntID, tw ToolWorkspace, o acceptOverrides) (checked, error) {
	wi, result, err := s.deps.Eligibility.Recheck(ctx, item, own)
	if err != nil {
		return checked{}, fmt.Errorf("re-checking %s: %w", item, err)
	}
	if !result.Eligible() {
		return checked{}, &IneligibleError{Item: item, Result: result}
	}
	settings, _ := tw.Settings.ForItem(wi.Labels)
	if o.Runner != "" {
		settings.Runner = o.Runner
	}
	if o.BaseBranch != "" {
		settings.BaseBranch = o.BaseBranch
	}
	runner, _, err := s.deps.Runners.Select(ctx, settings)
	if err != nil {
		return checked{}, err
	}
	return checked{item: wi, settings: settings, runner: runner}, nil
}

// prepared is an item claimed by this instance with its branch and
// worktree, and how to undo both.
type prepared struct {
	runner domain.Runner
	ws     AntWorkspace
	undo   func(context.Context) error
}

// claimAndPrepare acquires the task claim and creates the branch and the
// worktree, in this order (spec ant-execution, "Starting an ant on
// acceptance"). A lost claim race creates nothing; a failure to prepare
// the worktree releases the claim.
func (s *AntSupervisor) claimAndPrepare(ctx context.Context, c checked, tw ToolWorkspace) (prepared, error) {
	item := c.item.Ref
	held, err := s.deps.Claims.Acquire(ctx, item, domain.ClaimTask)
	if err != nil {
		return prepared{}, err
	}
	release := func(ctx context.Context) error {
		if err := s.deps.Claims.Release(ctx, item, domain.ClaimTask); err != nil {
			return fmt.Errorf("releasing the claim on %s: %w", item, err)
		}
		return nil
	}
	ws, err := s.deps.Workspace.Prepare(ctx, PrepareRequest{
		Clone: tw.Clone, Repo: tw.Repo, Item: item, Title: c.item.Title, Settings: c.settings,
		Trailers: domain.NewTrailers(item, c.runner, held.Instance),
	})
	if err != nil {
		return prepared{}, errors.Join(fmt.Errorf("preparing the worktree of %s: %w", item, err), release(ctx))
	}
	undo := func(ctx context.Context) error {
		return errors.Join(s.deps.Workspace.Discard(ctx, tw.Clone, ws), release(ctx))
	}
	return prepared{runner: c.runner, ws: ws, undo: undo}, nil
}

// acquire does the acceptance steps for an ant queued at acceptance,
// with the overrides kept on its record.
func (s *AntSupervisor) acquire(ctx context.Context, rec ports.AntRecord, tw ToolWorkspace) (prepared, error) {
	c, err := s.check(ctx, rec.Item, rec.ID, tw, acceptOverrides{Runner: domain.Runner(rec.Runner), BaseBranch: rec.BaseBranch})
	if err != nil {
		return prepared{}, err
	}
	return s.claimAndPrepare(ctx, c, tw)
}

// refusesStart reports whether err means the item cannot start now, as
// opposed to a failure to reach a provider or the store: it is no longer
// eligible, its runner is not available, another instance holds it, or
// its base branch is missing.
func refusesStart(err error) bool {
	var (
		ineligible *IneligibleError
		runner     *RunnerNotAvailableError
		taken      *ClaimTakenError
		base       *domain.BaseBranchMissingError
	)
	return errors.As(err, &ineligible) || errors.As(err, &runner) || errors.As(err, &taken) || errors.As(err, &base)
}
