package app

import (
	"context"
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
	_, result, err := e.Evaluate(ctx, tracker, item, own)
	return item, result, err
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
