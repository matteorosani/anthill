package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// NoPriority is what the console shows for an item's priority until
// provider priority and priority labels are modelled.
const NoPriority = "-"

// Proposal is one entry of the proposal list (spec queen-console,
// "Viewing proposals").
type Proposal struct {
	Item  domain.Ref
	Title string
	// Repository is the item's target repository.
	Repository domain.Repository
	Type       domain.ItemType
	Priority   string
	CreatedAt  time.Time
	// Runner is the runner an acceptance without override would use.
	Runner domain.Runner
}

// ProposalList is the proposal list, with what kept it from being
// complete, such as a tracker that could not be read.
type ProposalList struct {
	Proposals []Proposal
	Warnings  []string
	// Now is the daemon's time, from which ages are computed.
	Now time.Time
}

// Proposals builds the proposal list on fresh provider data (spec
// task-eligibility, "Proposal list"): every candidate item that passes
// every prerequisite and that the queen has not hidden on this machine,
// oldest first.
func (c *Console) Proposals(ctx context.Context) (ProposalList, error) {
	out := ProposalList{Now: c.Now()}
	candidates, err := c.deps.Candidates(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out.Warnings = append(out.Warnings, err.Error())
	}
	hidden, err := c.hidden(ctx)
	if err != nil {
		return out, err
	}
	var items []domain.WorkItem
	byRef := map[domain.Ref]Proposal{}
	for _, cand := range candidates {
		item := cand.Item
		if h, ok := hidden[item.Ref]; ok && domain.ProposalHidden(item, h.Until, h.ItemHash, out.Now) {
			continue
		}
		target, result, err := c.deps.Eligibility.Evaluate(ctx, cand.Tracker, item, 0)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %v", ItemName(item.Ref), err))
			continue
		}
		if !result.Eligible() {
			continue
		}
		settings, _ := target.Settings.ForItem(item.Labels)
		items = append(items, item)
		byRef[item.Ref] = Proposal{
			Item: item.Ref, Title: item.Title, Repository: target.Repository, Type: item.Type,
			Priority: NoPriority, CreatedAt: item.CreatedAt, Runner: settings.Runner,
		}
	}
	domain.SortProposals(items)
	for _, item := range items {
		out.Proposals = append(out.Proposals, byRef[item.Ref])
	}
	return out, nil
}

// hidden is the queen's hidden proposals by item.
func (c *Console) hidden(ctx context.Context) (map[domain.Ref]ports.HiddenProposal, error) {
	list, err := c.deps.Store.Hidden(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the hidden proposals: %w", err)
	}
	out := make(map[domain.Ref]ports.HiddenProposal, len(list))
	for _, h := range list {
		out[h.Item] = h
	}
	return out, nil
}

// Explanation is the full prerequisite evaluation of one item (spec
// queen-console, "Why is an item not proposed").
type Explanation struct {
	Item    domain.Ref
	Title   string
	Results []domain.CheckResult
	// Hidden is set when the queen declined or snoozed the item and that
	// still hides it, with Until set for a snooze.
	Hidden bool
	Until  time.Time
}

// Explain evaluates every prerequisite of the item on fresh provider
// data, met and unmet.
func (c *Console) Explain(ctx context.Context, item domain.Ref) (Explanation, error) {
	wi, result, err := c.deps.Eligibility.Recheck(ctx, item, 0)
	if err != nil {
		return Explanation{}, err
	}
	out := Explanation{Item: item, Title: wi.Title, Results: result.Results}
	hidden, err := c.hidden(ctx)
	if err != nil {
		return out, err
	}
	if h, ok := hidden[item]; ok && domain.ProposalHidden(wi, h.Until, h.ItemHash, c.Now()) {
		out.Hidden, out.Until = true, h.Until
	}
	return out, nil
}

// Accept accepts a proposal with the queen's overrides (spec
// queen-console, "Accept with a longer budget") and returns the new ant.
// The acceptance un-hides the item.
func (c *Console) Accept(ctx context.Context, req AcceptRequest) (AntView, error) {
	id, err := c.deps.Ants.Accept(ctx, req)
	if err != nil {
		return AntView{}, err
	}
	if err := c.deps.Store.Unhide(ctx, req.Item); err != nil {
		return AntView{}, fmt.Errorf("un-hiding %s: %w", ItemName(req.Item), err)
	}
	rec, err := c.deps.Store.Ant(ctx, id)
	if err != nil {
		return AntView{}, fmt.Errorf("reading ant %d: %w", id, err)
	}
	return view(rec, c.Now()), nil
}

// Decline hides the item on this machine only, until its content changes
// or the queen un-hides it (spec queen-console, "Decline is local"). It
// writes nothing to the provider.
func (c *Console) Decline(ctx context.Context, item domain.Ref) error {
	tracker, err := c.deps.Tracker(ctx, item)
	if err != nil {
		return err
	}
	wi, _, err := tracker.Item(ctx, item)
	if err != nil {
		return fmt.Errorf("reading %s: %w", ItemName(item), err)
	}
	return c.deps.Store.Hide(ctx, ports.HiddenProposal{Item: item, HiddenAt: c.Now(), ItemHash: domain.ProposalHash(wi)})
}

// Snooze hides the item on this machine for d, and returns when it ends.
func (c *Console) Snooze(ctx context.Context, item domain.Ref, d time.Duration) (time.Time, error) {
	if d <= 0 {
		return time.Time{}, errors.New("the snooze duration must be positive")
	}
	now := c.Now()
	until := now.Add(d)
	return until, c.deps.Store.Hide(ctx, ports.HiddenProposal{Item: item, HiddenAt: now, Until: until})
}

// Unhide shows a declined or snoozed item again. It reports whether the
// item was hidden.
func (c *Console) Unhide(ctx context.Context, item domain.Ref) (bool, error) {
	hidden, err := c.hidden(ctx)
	if err != nil {
		return false, err
	}
	if _, ok := hidden[item]; !ok {
		return false, nil
	}
	return true, c.deps.Store.Unhide(ctx, item)
}
