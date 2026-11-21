package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Evaluate evaluates every prerequisite of an item already read from
// tracker (spec task-eligibility): it reads the item's dependencies and
// children and its claim, and looks up this machine's ant for it. own is
// as for Recheck. It returns the item's target as well, for the settings
// the proposal list shows. Recheck reads the item fresh first; the
// proposal list evaluates the items a tracker listed.
func (e *Eligibility) Evaluate(ctx context.Context, tracker ports.TrackerPort, item domain.WorkItem, own ports.AntID) (_ ItemTarget, result domain.EligibilityResult, err error) {
	ctx, end := e.telemetry.StartEligibility(ctx, item.Ref)
	defer func() { end(result, err) }()
	return e.evaluate(ctx, tracker, item, own)
}

func (e *Eligibility) evaluate(ctx context.Context, tracker ports.TrackerPort, item domain.WorkItem, own ports.AntID) (ItemTarget, domain.EligibilityResult, error) {
	ref := item.Ref
	target, err := e.target(ctx, item)
	if err != nil {
		return target, domain.EligibilityResult{}, fmt.Errorf("mapping %s to its repository: %w", ref, err)
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
			return target, domain.EligibilityResult{}, fmt.Errorf("reading %s, linked from %s: %w", link, ref, err)
		}
	}
	if ec.ClaimHolder, ec.Instance, err = e.claims.Holder(ctx, ref, domain.ClaimTask); err != nil {
		return target, domain.EligibilityResult{}, err
	}
	rec, err := e.ants.CurrentAnt(ctx, ref)
	switch {
	case err == nil:
		if rec.ID != own && !rec.State().Final() {
			ec.LocalAnt = rec.State()
			ec.OpenAntPR = rec.PR != nil
		}
	case !errors.Is(err, ports.ErrNotFound):
		return target, domain.EligibilityResult{}, fmt.Errorf("reading the ant of %s: %w", ref, err)
	}
	ec.PendingQuestion = pendingQuestion(item.Comments)
	return target, domain.Evaluate(item, ec), nil
}

// Candidate is an item a tracker listed for the proposal list, with the
// tracker that listed it.
type Candidate struct {
	Item    domain.WorkItem
	Tracker ports.TrackerPort
}

// CandidateSource lists the items that may be proposed: the opted-in
// open items of the enabled repositories (spec task-eligibility,
// "Proposal list"). It returns what it could read, and an error naming
// each tracker repository it could not.
type CandidateSource func(ctx context.Context) ([]Candidate, error)

// TrackerCandidates is the candidate source over the configured
// repositories: it reads every opted-in candidate of each tracker
// repository of an enabled entry once (TrackerPort.Items with an empty
// cursor), and keeps the open items that carry the opt-in label of one of
// the repository's entries. tracker finds the tracker of a tracker
// repository.
func TrackerCandidates(config *Configuration, tracker func(ctx context.Context, repo domain.Repository) (ports.TrackerPort, error)) CandidateSource {
	return func(ctx context.Context) ([]Candidate, error) {
		snap := config.Current()
		if snap == nil {
			return nil, errors.New("the configuration is not loaded")
		}
		var repos []domain.Repository
		optIn := map[domain.Repository][]string{}
		for _, rc := range snap.Repositories {
			if !rc.Enabled() {
				continue
			}
			t := domain.TrackerRepository(rc.Repository)
			if !slices.Contains(repos, t) {
				repos = append(repos, t)
			}
			optIn[t] = append(optIn[t], rc.Settings.OptInLabel)
		}
		var out []Candidate
		var errs []error
		for _, repo := range repos {
			port, err := tracker(ctx, repo)
			if err == nil {
				var page ports.ItemPage
				if page, err = port.Items(ctx, repo, ""); err == nil {
					for _, item := range page.Items {
						if item.State == domain.StateOpen && slices.ContainsFunc(optIn[repo], func(l string) bool { return domain.HasLabel(item.Labels, l) }) {
							out = append(out, Candidate{Item: item, Tracker: port})
						}
					}
					continue
				}
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			errs = append(errs, fmt.Errorf("listing the items of %s: %w", repo, err))
		}
		return out, errors.Join(errs...)
	}
}
