package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The reasons of a rebuilt ant's first transition.
const (
	// ReasonRebuilt is the reason of an ant rebuilt from the provider
	// and git (spec ant-execution, "Session index recovery").
	ReasonRebuilt = "rebuilt from provider"
	// ReasonInterruptedByRestart is the reason of an ant whose active
	// run the daemon's restart ended (spec ant-execution, "Restart
	// resilience").
	ReasonInterruptedByRestart = "interrupted by restart"
)

// ClaimAdopter takes up a claim this instance holds on the provider into
// the local store, and parks it; Claiming implements it.
type ClaimAdopter interface {
	Adopt(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (ports.HeldClaim, bool, error)
	Park(ctx context.Context, item domain.Ref) error
}

var _ ClaimAdopter = (*Claiming)(nil)

// RecoverySource is one enabled repository entry, as session index
// recovery reads it: its code repository and clone, with the ant
// branches; its code host, with the ant PRs; and its tracker, with the
// items carrying the claim label.
type RecoverySource struct {
	// Repo is the code repository and Clone its local clone.
	Repo  domain.Repository
	Clone ports.Clone
	// Settings are the repository's effective settings, for the branch
	// prefix, the claim label, the base branch, the runner and the
	// budget.
	Settings domain.Effective
	CodeHost ports.CodeHostPort
	// Tracker reads TrackerRepo, the repository of the items: the code
	// repository on GitHub, the project on Azure DevOps.
	Tracker     ports.TrackerPort
	TrackerRepo domain.Repository
}

// SessionRecoveryDeps are the ports and services session index recovery
// uses.
type SessionRecoveryDeps struct {
	Store  ports.SessionIndex
	Claims ClaimAdopter
	Git    ports.SourceControl
	// Workspace recreates a missing worktree from the local or pushed
	// branch.
	Workspace *Workspace
	// Sources lists the enabled repository entries.
	Sources func(ctx context.Context) ([]RecoverySource, error)
	Clock   ports.Clock
}

// SessionRecovery rebuilds the session index records of the items this
// instance holds a task claim on, when they are missing, such as after
// the local store was deleted (spec ant-execution, "Session index
// recovery"; design D17). The daemon runs it at startup, before restart
// reconciliation.
type SessionRecovery struct {
	deps SessionRecoveryDeps
}

// NewSessionRecovery returns the recovery service.
func NewSessionRecovery(deps SessionRecoveryDeps) *SessionRecovery {
	return &SessionRecovery{deps: deps}
}

// RecoveryReport is what one recovery did.
type RecoveryReport struct {
	// Rebuilt lists the rebuilt ants, in source and item order.
	Rebuilt []ports.AntID
}

// Recover finds the items of every source that this instance may hold,
// from three places: the ant branches in the clone, the open ant PRs, and
// the items carrying the claim label. An item whose current ant is in the
// session index is left alone. For the others, the claims decide: only
// an item whose task claim this instance holds, live or parked, gets a
// record, and the claim is adopted into the local store. The record has
// the branch with this instance's commits, its open ant PR, the
// configured runner, a new budget and no runner session, so the next run
// starts a new session with the summary brief. Its worktree is recreated
// from the branch when it is missing.
//
// A failure on one source or item does not stop the others; their
// errors are returned joined.
func (r *SessionRecovery) Recover(ctx context.Context) (RecoveryReport, error) {
	sources, err := r.deps.Sources(ctx)
	if err != nil {
		return RecoveryReport{}, fmt.Errorf("listing the repositories: %w", err)
	}
	var (
		report RecoveryReport
		errs   []error
	)
	for _, src := range sources {
		rebuilt, err := r.recoverSource(ctx, src)
		report.Rebuilt = append(report.Rebuilt, rebuilt...)
		if err != nil {
			errs = append(errs, fmt.Errorf("recovering the ants of %s: %w", src.Repo, err))
		}
	}
	return report, errors.Join(errs...)
}

// candidate is an item this instance may hold, with its ant branches and
// open ant PRs.
type candidate struct {
	branches []string
	prs      []domain.PullRequest
}

func (r *SessionRecovery) recoverSource(ctx context.Context, src RecoverySource) ([]ports.AntID, error) {
	found, err := r.candidates(ctx, src)
	if err != nil {
		return nil, err
	}
	refs := make([]domain.Ref, 0, len(found))
	for ref := range found {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b domain.Ref) int { return strings.Compare(a.Full(), b.Full()) })
	var (
		rebuilt []ports.AntID
		errs    []error
	)
	for _, ref := range refs {
		id, err := r.recoverItem(ctx, src, ref, found[ref])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ref, err))
		}
		if id != 0 {
			rebuilt = append(rebuilt, id)
		}
	}
	return rebuilt, errors.Join(errs...)
}

// candidates gathers the items of a source from its ant branches, its
// open ant PRs and its items with the claim label.
func (r *SessionRecovery) candidates(ctx context.Context, src RecoverySource) (map[domain.Ref]*candidate, error) {
	prefix := src.Settings.BranchPrefix
	found := map[domain.Ref]*candidate{}
	get := func(ref domain.Ref) *candidate {
		c, ok := found[ref]
		if !ok {
			c = &candidate{}
			found[ref] = c
		}
		return c
	}
	if err := r.deps.Git.Fetch(ctx, src.Clone); err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}
	branches, err := r.deps.Git.Branches(ctx, src.Clone, prefix)
	if err != nil {
		return nil, fmt.Errorf("listing the ant branches: %w", err)
	}
	for _, b := range branches {
		if ref, ok := domain.BranchItem(prefix, b, src.Repo); ok {
			get(ref).branches = append(get(ref).branches, b)
		}
	}
	prs, err := src.CodeHost.PullRequests(ctx, src.Repo, "")
	if err != nil {
		return nil, fmt.Errorf("listing the open PRs: %w", err)
	}
	for _, pr := range prs.PullRequests {
		if ref, ok := domain.BranchItem(prefix, pr.SourceBranch, src.Repo); ok {
			c := get(ref)
			c.prs = append(c.prs, pr)
			if !slices.Contains(c.branches, pr.SourceBranch) {
				c.branches = append(c.branches, pr.SourceBranch)
			}
		}
	}
	items, err := src.Tracker.Items(ctx, src.TrackerRepo, "")
	if err != nil {
		return nil, fmt.Errorf("listing the items: %w", err)
	}
	for _, it := range items.Items {
		if it.Ref.Kind == domain.KindItem && slices.ContainsFunc(it.Labels, func(l string) bool { return strings.EqualFold(l, src.Settings.ClaimLabel) }) {
			get(it.Ref)
		}
	}
	return found, nil
}

// recoverItem rebuilds the record of one item, when it is missing and
// this instance holds the item's task claim. It returns the new ant, or
// 0 when there was nothing to rebuild.
func (r *SessionRecovery) recoverItem(ctx context.Context, src RecoverySource, ref domain.Ref, c *candidate) (ports.AntID, error) {
	switch _, err := r.deps.Store.CurrentAnt(ctx, ref); {
	case err == nil:
		return 0, nil
	case !errors.Is(err, ports.ErrNotFound):
		return 0, fmt.Errorf("reading the ant: %w", err)
	}
	held, ok, err := r.deps.Claims.Adopt(ctx, ref, domain.ClaimTask)
	if err != nil || !ok {
		return 0, err
	}
	owner := domain.BranchOwner{Item: ref.String(), Instance: held.Instance}
	branch, pr, base, err := r.ownBranch(ctx, src, owner, c)
	if err != nil {
		return 0, err
	}
	state, reason := rebuiltState(held, pr)
	settings := src.Settings
	rec := ports.AntRecord{
		Item:       ref,
		Base:       settings.Budget,
		Budget:     domain.NewBudget(settings.Budget, settings.BudgetPolicy()),
		History:    []domain.Transition{{To: state, At: r.deps.Clock.Now(), Reason: reason}},
		Runner:     string(settings.Runner),
		Branch:     branch,
		BaseBranch: base,
	}
	if pr != nil {
		rec.PR = &pr.Ref
	}
	var errs []error
	if branch != "" {
		settings.BaseBranch = base
		ws, err := r.deps.Workspace.Prepare(ctx, PrepareRequest{
			Clone: src.Clone, Repo: src.Repo, Item: ref, Settings: settings,
			Trailers: domain.NewTrailers(ref, settings.Runner, held.Instance),
			Branch:   branch,
		})
		if err != nil {
			// The record is kept without a worktree: the next run
			// recreates it.
			errs = append(errs, fmt.Errorf("recreating the worktree of %s: %w", branch, err))
		}
		rec.Worktree = ws.Worktree
	}
	if rec.ID, err = r.deps.Store.CreateAnt(ctx, rec); err != nil {
		return 0, errors.Join(append(errs, fmt.Errorf("storing the rebuilt ant: %w", err))...)
	}
	if !held.Parked {
		// The ant is between runs now, so its claim is parked (spec
		// work-claiming, "Long-lived task claims").
		if err := r.deps.Claims.Park(ctx, ref); err != nil {
			errs = append(errs, fmt.Errorf("parking the claim: %w", err))
		}
	}
	return rec.ID, errors.Join(errs...)
}

// ownBranch picks the ant branch that carries this instance's commits
// for the item, trying the branches of open ant PRs first, and returns it
// with its open PR, if any, and the base branch: the PR's target, or else
// the configured or default base branch. The branch is "" when none
// carries the instance's commits.
func (r *SessionRecovery) ownBranch(ctx context.Context, src RecoverySource, owner domain.BranchOwner, c *candidate) (string, *domain.PullRequest, string, error) {
	base, err := r.deps.Workspace.base(ctx, src.Clone, src.Settings.BaseBranch)
	if err != nil {
		return "", nil, "", err
	}
	type option struct {
		branch string
		pr     *domain.PullRequest
		base   string
	}
	var options []option
	for i := range c.prs {
		pr := &c.prs[i]
		options = append(options, option{pr.SourceBranch, pr, pr.TargetBranch})
	}
	for _, b := range c.branches {
		options = append(options, option{b, nil, base})
	}
	for _, o := range options {
		info, err := r.deps.Git.Branch(ctx, src.Clone, o.branch, o.base)
		if errors.Is(err, ports.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", nil, "", fmt.Errorf("looking up branch %s: %w", o.branch, err)
		}
		if !slices.Contains(info.Owners, owner) {
			continue
		}
		if o.pr == nil {
			// A PR found for another branch name of the same item does
			// not belong to this one.
			for i := range c.prs {
				if c.prs[i].SourceBranch == o.branch {
					o.pr, o.base = &c.prs[i], c.prs[i].TargetBranch
				}
			}
		}
		return o.branch, o.pr, o.base, nil
	}
	return "", nil, base, nil
}

// rebuiltState is the state of a rebuilt ant. A parked claim means the
// ant was between runs: with its open PR it had succeeded, without one
// it is failed, so the queen can retry or abandon it. A live claim means
// a run was active when the record was lost: the run was interrupted.
func rebuiltState(held ports.HeldClaim, pr *domain.PullRequest) (domain.AntState, string) {
	switch {
	case !held.Parked:
		return domain.AntFailed, ReasonInterruptedByRestart
	case pr != nil:
		return domain.AntSucceeded, ReasonRebuilt
	}
	return domain.AntFailed, ReasonRebuilt
}

// rebuiltAnt reports whether session index recovery made the record: its
// first transition is to a between-runs state other than queued, which
// only recovery does. Its earlier runs are not in the run log, but its
// next run is not its first: it reuses the claim, the branch and the
// worktree, and gets the summary brief.
func rebuiltAnt(rec ports.AntRecord) bool {
	if len(rec.History) == 0 {
		return false
	}
	first := rec.History[0].To
	return first.BetweenRuns() && first != domain.AntQueued
}
