package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ClaimView is a claim this instance holds, as the status shows it.
type ClaimView struct {
	Ref  domain.Ref
	Kind domain.ClaimKind
	// Parked claims do not expire; Expires is the lease's end otherwise.
	Parked  bool
	Expires time.Time
}

// StatusReport is the console's part of the daemon's health (spec
// queen-console, "Health and status"): each provider connection's last
// successful poll, last error and rate limit, the ants with an active run
// and the queued ones, the held claims with their lease expiry, and the
// shepherd's active rounds.
type StatusReport struct {
	Now        time.Time
	Providers  []ports.PollStatus
	ActiveRuns []AntView
	Queued     []AntView
	Claims     []ClaimView
	Rounds     []domain.Ref
}

// Status reports the console's part of the daemon's health.
func (c *Console) Status(ctx context.Context) (StatusReport, error) {
	out := StatusReport{Now: c.Now(), ActiveRuns: []AntView{}, Queued: []AntView{}, Claims: []ClaimView{}, Rounds: []domain.Ref{}}
	polls, err := c.deps.Store.PollStatuses(ctx)
	if err != nil {
		return out, fmt.Errorf("reading the poll states: %w", err)
	}
	out.Providers = slices.SortedFunc(slices.Values(polls), func(a, b ports.PollStatus) int {
		return compareStrings(a.Connection, b.Connection)
	})
	recs, err := c.deps.Store.Ants(ctx)
	if err != nil {
		return out, fmt.Errorf("listing the ants: %w", err)
	}
	for _, rec := range recs {
		switch s := rec.State(); {
		case s.HasActiveRun():
			out.ActiveRuns = append(out.ActiveRuns, view(rec, out.Now))
		case s == domain.AntQueued:
			out.Queued = append(out.Queued, view(rec, out.Now))
		}
	}
	if c.deps.Claims != nil {
		held, err := c.deps.Claims.Held(ctx)
		if err != nil {
			return out, fmt.Errorf("listing the held claims: %w", err)
		}
		for _, h := range held {
			v := ClaimView{Ref: h.Ref, Kind: h.Kind, Parked: h.Parked}
			if !h.Parked {
				v.Expires = h.UpdatedAt.Add(h.Lease)
			}
			out.Claims = append(out.Claims, v)
		}
	}
	if c.deps.Rounds != nil {
		out.Rounds = append(out.Rounds, c.deps.Rounds.ActiveRounds()...)
	}
	return out, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
