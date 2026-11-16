package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ItemTarget is where an item maps to on this machine (spec
// work-item-sources, "Repository mapping"), and the settings its
// prerequisites use.
type ItemTarget struct {
	// Repository is the target repository, or the zero repository when
	// the item is unmapped.
	Repository domain.Repository
	// Available tells whether the repository is configured, enabled and
	// cloned on this machine.
	Available bool
	// Settings are the repository's effective settings, or the global
	// ones for an unmapped item: the opt-in and hold labels and
	// require-acceptance-criteria.
	Settings domain.Effective
}

// TargetRoute maps an item to its target repository. The daemon builds
// it from the configuration and the code hosts.
type TargetRoute func(ctx context.Context, item domain.WorkItem) (ItemTarget, error)

// ClaimHolders finds who holds the live claim on an item or PR; Claiming
// implements it.
type ClaimHolders interface {
	// Holder returns the instance holding the live claim of the kind on
	// ref, or "" when none does, and this instance's own id there.
	Holder(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (holder, instance string, err error)
}

// Eligibility evaluates an item's prerequisites on fresh provider data
// (spec task-eligibility). Acceptance uses it for the re-check; the
// proposal list can use it as well.
type Eligibility struct {
	tracker TrackerRoute
	claims  ClaimHolders
	target  TargetRoute
	ants    ports.SessionIndex
}

// NewEligibility returns the eligibility service.
func NewEligibility(tracker TrackerRoute, claims ClaimHolders, target TargetRoute, ants ports.SessionIndex) *Eligibility {
	return &Eligibility{tracker: tracker, claims: claims, target: target, ants: ants}
}

// Recheck reads the item fresh, with its dependencies and children and
// its claims, and evaluates every prerequisite (spec task-eligibility,
// "Re-check at acceptance"). own is the ant being started for the item,
// such as one queued at acceptance, which does not count as work on it;
// 0 means none. A dependency or child that cannot be read is left out,
// so it counts as not resolvable.
func (e *Eligibility) Recheck(ctx context.Context, ref domain.Ref, own ports.AntID) (domain.WorkItem, domain.EligibilityResult, error) {
	tracker, err := e.tracker(ctx, ref)
	if err != nil {
		return domain.WorkItem{}, domain.EligibilityResult{}, err
	}
	item, _, err := tracker.Item(ctx, ref)
	if err != nil {
		return domain.WorkItem{}, domain.EligibilityResult{}, fmt.Errorf("reading %s: %w", ref, err)
	}
	target, err := e.target(ctx, item)
	if err != nil {
		return item, domain.EligibilityResult{}, fmt.Errorf("mapping %s to its repository: %w", ref, err)
	}
	ec := domain.EligibilityContext{
		OptInLabel:                target.Settings.OptInLabel,
		HoldLabel:                 target.Settings.HoldLabel,
		RequireAcceptanceCriteria: target.Settings.RequireAcceptanceCriteria,
		RepositoryAvailable:       target.Available,
		States:                    map[domain.Ref]domain.ItemState{},
	}
	if target.Repository != (domain.Repository{}) {
		ec.TargetRepository = target.Repository.String()
	}
	for _, link := range append(append([]domain.Ref(nil), item.Dependencies...), item.Children...) {
		linked, _, err := tracker.Item(ctx, link)
		switch {
		case err == nil:
			ec.States[link] = linked.State
		case !errors.Is(err, ports.ErrNotFound):
			return item, domain.EligibilityResult{}, fmt.Errorf("reading %s, linked from %s: %w", link, ref, err)
		}
	}
	if ec.ClaimHolder, ec.Instance, err = e.claims.Holder(ctx, ref, domain.ClaimTask); err != nil {
		return item, domain.EligibilityResult{}, err
	}
	rec, err := e.ants.CurrentAnt(ctx, ref)
	switch {
	case err == nil:
		if rec.ID != own && !rec.State().Final() {
			ec.LocalAnt = rec.State()
			ec.OpenAntPR = rec.PR != nil
		}
	case !errors.Is(err, ports.ErrNotFound):
		return item, domain.EligibilityResult{}, fmt.Errorf("reading the ant of %s: %w", ref, err)
	}
	ec.PendingQuestion = pendingQuestion(item.Comments)
	return item, domain.Evaluate(item, ec), nil
}

// pendingQuestion is the latest question comment of an ant on the item
// when no human commented after it, or nil.
func pendingQuestion(comments []domain.Comment) *domain.Comment {
	var pending *domain.Comment
	for i, c := range comments {
		m, ok := domain.FindMarker(c.Body)
		switch {
		case ok && m.Kind == domain.CommentQuestion:
			pending = &comments[i]
		case !ok:
			pending = nil
		}
	}
	return pending
}
