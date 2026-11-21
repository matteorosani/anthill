package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/app"
)

// methodUsage is the control method of the usage report (spec
// queen-console, "Usage reporting").
const methodUsage = "usage.report"

// consoleStatus is the console's part of the status result.
type consoleStatus struct {
	Now        time.Time      `json:"now"`
	Providers  []providerJSON `json:"providers"`
	ActiveRuns []antJSON      `json:"active_runs"`
	Queued     []antJSON      `json:"queued"`
	Claims     []claimJSON    `json:"claims"`
	Rounds     []itemJSON     `json:"rounds"`
}

type providerJSON struct {
	Connection       string    `json:"connection"`
	LastSuccess      time.Time `json:"last_success,omitzero"`
	LastError        string    `json:"last_error,omitempty"`
	LastErrorAt      time.Time `json:"last_error_at,omitzero"`
	RateLimitedUntil time.Time `json:"rate_limited_until,omitzero"`
}

type claimJSON struct {
	Item    itemJSON  `json:"item"`
	Kind    string    `json:"kind"`
	Parked  bool      `json:"parked,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
}

func consoleStatusOf(r app.StatusReport) *consoleStatus {
	out := &consoleStatus{Now: r.Now, Providers: []providerJSON{}, ActiveRuns: []antJSON{}, Queued: []antJSON{}, Claims: []claimJSON{}, Rounds: []itemJSON{}}
	for _, p := range r.Providers {
		out.Providers = append(out.Providers, providerJSON{
			Connection: p.Connection, LastSuccess: p.LastSuccess, LastError: p.LastError, LastErrorAt: p.LastErrorAt, RateLimitedUntil: p.RateLimitedUntil,
		})
	}
	for _, a := range r.ActiveRuns {
		out.ActiveRuns = append(out.ActiveRuns, antOf(a))
	}
	for _, a := range r.Queued {
		out.Queued = append(out.Queued, antOf(a))
	}
	for _, c := range r.Claims {
		out.Claims = append(out.Claims, claimJSON{Item: itemOf(c.Ref), Kind: string(c.Kind), Parked: c.Parked, Expires: c.Expires})
	}
	for _, pr := range r.Rounds {
		out.Rounds = append(out.Rounds, itemOf(pr))
	}
	return out
}

// usageParams ask for the usage of one day, as YYYY-MM-DD in the
// daemon's time zone (today when empty), or of one item.
type usageParams struct {
	Date string `json:"date,omitempty"`
	Item string `json:"item,omitempty"`
}

type usageLineJSON struct {
	Item      itemJSON  `json:"item"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"started_at"`
	// Duration is in whole seconds; Active is set while it goes on.
	Duration int64    `json:"duration"`
	Active   bool     `json:"active,omitempty"`
	Tokens   *int64   `json:"tokens"`
	CostUSD  *float64 `json:"cost_usd"`
}

type usageResult struct {
	// Date is the day reported, empty for an item.
	Date          string          `json:"date,omitempty"`
	Lines         []usageLineJSON `json:"lines"`
	Duration      int64           `json:"duration"`
	Tokens        int64           `json:"tokens"`
	TokensUnknown int             `json:"tokens_unknown"`
	CostUSD       float64         `json:"cost_usd"`
	CostUnknown   int             `json:"cost_unknown"`
}

func handleUsage(ctx context.Context, c *app.Console, raw json.RawMessage, _ control.Stream) (any, error) {
	var p usageParams
	if len(raw) > 0 {
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
	}
	var (
		report app.UsageReport
		err    error
		out    usageResult
	)
	if p.Item != "" {
		item, rerr := resolve(c, p.Item)
		if rerr != nil {
			return nil, rerr
		}
		report, err = c.ItemUsage(ctx, item)
	} else {
		day := c.Now().In(time.Local)
		if p.Date != "" {
			if day, err = time.ParseInLocation(time.DateOnly, p.Date, time.Local); err != nil {
				return nil, &control.Error{Code: control.CodeInvalidParams, Message: "invalid date " + p.Date + ": use YYYY-MM-DD"}
			}
		}
		out.Date = day.Format(time.DateOnly)
		report, err = c.DayUsage(ctx, day)
	}
	if err != nil {
		return nil, err
	}
	out.Lines = []usageLineJSON{}
	for _, l := range report.Lines {
		out.Lines = append(out.Lines, usageLineJSON{
			Item: itemOf(l.Item), Kind: l.Kind, StartedAt: l.StartedAt, Duration: seconds(l.Duration), Active: l.Active,
			Tokens: l.Tokens, CostUSD: l.CostUSD,
		})
	}
	out.Duration, out.Tokens, out.TokensUnknown = seconds(report.Duration), report.Tokens, report.TokensUnknown
	out.CostUSD, out.CostUnknown = report.CostUSD, report.CostUnknown
	return out, nil
}
