package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// UsageLine is one run or shepherd round in a usage report (spec
// queen-console, "Usage reporting").
type UsageLine struct {
	// Round is set for a shepherd round, and Run for an ant run.
	Round ports.RoundID
	Run   ports.RunID
	// Item is the run's item, or the round's PR.
	Item      domain.Ref
	Kind      string
	StartedAt time.Time
	// Duration is zero, and Active set, while the run or round goes on.
	Duration time.Duration
	Active   bool
	// Tokens and CostUSD are nil when unknown.
	Tokens  *int64
	CostUSD *float64
}

// UsageReport is the usage of the runs and rounds of a day or an item,
// with totals. A run or round of unknown tokens or cost is left out of
// that total and counted apart, so the total says how much is missing.
// No limit is enforced.
type UsageReport struct {
	Lines         []UsageLine
	Duration      time.Duration
	Tokens        int64
	TokensUnknown int
	CostUSD       float64
	CostUnknown   int
}

func (r *UsageReport) add(l UsageLine) {
	r.Lines = append(r.Lines, l)
	r.Duration += l.Duration
	if l.Tokens != nil {
		r.Tokens += *l.Tokens
	} else {
		r.TokensUnknown++
	}
	if l.CostUSD != nil {
		r.CostUSD += *l.CostUSD
	} else {
		r.CostUnknown++
	}
}

// DayUsage reports the runs and rounds started on the day of date, from
// its midnight in date's location to the next.
func (c *Console) DayUsage(ctx context.Context, date time.Time) (UsageReport, error) {
	from := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	runs, rounds, err := c.deps.Store.Usage(ctx, from, from.AddDate(0, 0, 1))
	if err != nil {
		return UsageReport{}, fmt.Errorf("reading the usage: %w", err)
	}
	return c.report(ctx, runs, rounds, nil)
}

// ItemUsage reports every run of the item's ants and every round on their
// PRs.
func (c *Console) ItemUsage(ctx context.Context, item domain.Ref) (UsageReport, error) {
	if _, err := c.currentAnt(ctx, item); err != nil {
		return UsageReport{}, err
	}
	runs, rounds, err := c.deps.Store.Usage(ctx, time.Time{}, c.Now().Add(time.Hour))
	if err != nil {
		return UsageReport{}, fmt.Errorf("reading the usage: %w", err)
	}
	return c.report(ctx, runs, rounds, &item)
}

// report makes the report of runs and rounds, of the item's only when
// item is set, oldest first.
func (c *Console) report(ctx context.Context, runs []ports.RunRecord, rounds []ports.RoundRecord, item *domain.Ref) (UsageReport, error) {
	ants := map[ports.AntID]ports.AntRecord{}
	prs := map[domain.Ref]bool{}
	var lines []UsageLine
	for _, r := range runs {
		rec, ok := ants[r.Ant]
		if !ok {
			var err error
			if rec, err = c.deps.Store.Ant(ctx, r.Ant); err != nil && !errors.Is(err, ports.ErrNotFound) {
				return UsageReport{}, fmt.Errorf("reading ant %d: %w", r.Ant, err)
			}
			ants[r.Ant] = rec
		}
		if item != nil && rec.Item != *item {
			continue
		}
		if rec.PR != nil {
			prs[*rec.PR] = true
		}
		lines = append(lines, UsageLine{
			Run: r.ID, Item: rec.Item, Kind: string(r.Kind) + " run", StartedAt: r.StartedAt,
			Duration: r.Duration(), Active: r.EndedAt.IsZero(), Tokens: r.Usage.Tokens, CostUSD: r.Usage.CostUSD,
		})
	}
	if item != nil {
		// The item's PRs are those of all its ants, not only those with a
		// run in the report.
		recs, err := c.deps.Store.Ants(ctx)
		if err != nil {
			return UsageReport{}, fmt.Errorf("listing the ants: %w", err)
		}
		for _, rec := range recs {
			if rec.Item == *item && rec.PR != nil {
				prs[*rec.PR] = true
			}
		}
	}
	for _, r := range rounds {
		if item != nil && !prs[r.PR] {
			continue
		}
		kind := "round"
		if r.Requested {
			kind = "requested round"
		}
		l := UsageLine{Round: r.ID, Item: r.PR, Kind: kind, StartedAt: r.StartedAt, Active: r.EndedAt.IsZero(), Tokens: r.Usage.Tokens, CostUSD: r.Usage.CostUSD}
		if !l.Active {
			l.Duration = r.EndedAt.Sub(r.StartedAt)
		}
		lines = append(lines, l)
	}
	slices.SortStableFunc(lines, func(a, b UsageLine) int { return a.StartedAt.Compare(b.StartedAt) })
	var out UsageReport
	for _, l := range lines {
		out.add(l)
	}
	return out, nil
}
