package app_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
	"github.com/matteorosani/anthill/internal/testkit/storetest"
)

var usageStart = time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)

// usageFixture is a store with one ant and a recorder over it.
type usageFixture struct {
	t     *testing.T
	ctx   context.Context
	store *testkit.StateStore
	rec   *app.UsageRecorder
	ant   ports.AntID
	next  time.Time
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	ctx := context.Background()
	store := testkit.NewStateStore()
	ant, err := store.CreateAnt(ctx, storetest.Ant(12, domain.AntStarting, domain.AntRunning))
	if err != nil {
		t.Fatal(err)
	}
	return &usageFixture{t: t, ctx: ctx, store: store, rec: app.NewUsageRecorder(store), ant: ant, next: usageStart}
}

// run starts a run, ends it 40 minutes later through the recorder with
// the usage reported, and returns the stored run. session is the id the
// run reports; resumed is the session it resumed, or empty.
func (f *usageFixture) run(session, resumed string, outcome ports.RunOutcome, reported ports.Usage) ports.RunRecord {
	f.t.Helper()
	kind := ports.RunTask
	if resumed != "" {
		kind = ports.RunFollowUp
	}
	r := ports.RunRecord{Ant: f.ant, Kind: kind, SessionID: resumed, StartedAt: f.next}
	id, err := f.store.StartRun(f.ctx, r)
	if err != nil {
		f.t.Fatal(err)
	}
	r.ID, r.SessionID, r.Outcome, r.EndedAt = id, session, outcome, f.next.Add(40*time.Minute)
	f.next = f.next.Add(time.Hour)
	if err := f.rec.EndRun(f.ctx, r, reported, resumed); err != nil {
		f.t.Fatal(err)
	}
	runs, err := f.store.Runs(f.ctx, f.ant)
	if err != nil {
		f.t.Fatal(err)
	}
	got := runs[len(runs)-1]
	if got.ID != id {
		f.t.Fatalf("last run is %d, want %d", got.ID, id)
	}
	if got.Duration() != 40*time.Minute {
		f.t.Errorf("run %d lasted %v, want 40m", id, got.Duration())
	}
	return got
}

func tokens(n int64) *int64  { return &n }
func usd(v float64) *float64 { return &v }

func wantFloat(t *testing.T, what string, got, want *float64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %v, want unknown", what, *got)
	case want != nil && got == nil:
		t.Errorf("%s = unknown, want %v", what, *want)
	case want != nil && math.Abs(*got-*want) > 1e-9:
		t.Errorf("%s = %v, want %v", what, *got, *want)
	}
}

func wantTokens(t *testing.T, got, want *int64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("tokens = %d, want unknown", *got)
	case want != nil && got == nil:
		t.Errorf("tokens = unknown, want %d", *want)
	case want != nil && *got != *want:
		t.Errorf("tokens = %d, want %d", *got, *want)
	}
}

// Scenario "runner reports cost".
func TestUsageRunnerReportsCost(t *testing.T) {
	f := newUsageFixture(t)
	got := f.run("s1", "", ports.OutcomeComplete, ports.Usage{Tokens: tokens(1_200_000), CostUSD: usd(3.40)})
	wantTokens(t, got.Usage.Tokens, tokens(1_200_000))
	wantFloat(t, "cost", got.Usage.CostUSD, usd(3.40))
	wantFloat(t, "session total", got.SessionCostUSD, nil)
}

// Scenario "runner without usage data".
func TestUsageRunnerWithoutUsageData(t *testing.T) {
	f := newUsageFixture(t)
	got := f.run("s1", "", ports.OutcomeComplete, ports.Usage{})
	wantTokens(t, got.Usage.Tokens, nil)
	wantFloat(t, "cost", got.Usage.CostUSD, nil)
}

// Scenario "run stopped after a question": the tokens the runner's
// messages reported are kept, and the cost is unknown.
func TestUsageRunStoppedAfterAQuestion(t *testing.T) {
	f := newUsageFixture(t)
	got := f.run("s1", "", ports.OutcomeStopped, ports.Usage{Tokens: tokens(40_000), CostIsSessionTotal: true})
	wantTokens(t, got.Usage.Tokens, tokens(40_000))
	wantFloat(t, "cost", got.Usage.CostUSD, nil)
	wantFloat(t, "session total", got.SessionCostUSD, nil)
}

// A stopped run that did report a running total for its session has its
// cost computed from it as any other run (spec ant-execution, "Usage of a
// stopped run").
func TestUsageStoppedRunWithASessionTotal(t *testing.T) {
	f := newUsageFixture(t)
	f.run("s1", "", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.30), CostIsSessionTotal: true})
	got := f.run("s1", "s1", ports.OutcomeStopped, ports.Usage{Tokens: tokens(40_000), CostUSD: usd(0.45), CostIsSessionTotal: true})
	wantTokens(t, got.Usage.Tokens, tokens(40_000))
	wantFloat(t, "cost", got.Usage.CostUSD, usd(0.15))
	wantFloat(t, "session total", got.SessionCostUSD, usd(0.45))
}

// Scenario "resumed run with a cumulative cost".
func TestUsageResumedRunWithACumulativeCost(t *testing.T) {
	f := newUsageFixture(t)
	first := f.run("s1", "", ports.OutcomeFailed, ports.Usage{CostUSD: usd(0.30), CostIsSessionTotal: true})
	wantFloat(t, "first cost", first.Usage.CostUSD, usd(0.30))
	got := f.run("s1", "s1", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.50), CostIsSessionTotal: true})
	wantFloat(t, "cost", got.Usage.CostUSD, usd(0.20))
	wantFloat(t, "session total", got.SessionCostUSD, usd(0.50))
	if got.Usage.CostIsSessionTotal {
		t.Error("stored cost is marked as a session total")
	}
}

// Scenario "new session with a cumulative cost": an earlier session's
// total does not count.
func TestUsageNewSessionWithACumulativeCost(t *testing.T) {
	f := newUsageFixture(t)
	f.run("s1", "", ports.OutcomeFailed, ports.Usage{CostUSD: usd(0.30), CostIsSessionTotal: true})
	got := f.run("s2", "", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.40), CostIsSessionTotal: true})
	wantFloat(t, "cost", got.Usage.CostUSD, usd(0.40))
}

// Scenario "total lower than the last one".
func TestUsageTotalLowerThanTheLastOne(t *testing.T) {
	f := newUsageFixture(t)
	f.run("s1", "", ports.OutcomeFailed, ports.Usage{CostUSD: usd(0.30), CostIsSessionTotal: true})
	got := f.run("s1", "s1", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.25), CostIsSessionTotal: true})
	wantFloat(t, "cost", got.Usage.CostUSD, nil)
	wantFloat(t, "session total", got.SessionCostUSD, usd(0.25))
}

// The last running total is the latest one any earlier run of the
// session reported: a run in between that reported none is skipped.
func TestUsageLastTotalSkipsARunWithoutOne(t *testing.T) {
	f := newUsageFixture(t)
	f.run("s1", "", ports.OutcomeFailed, ports.Usage{CostUSD: usd(0.30), CostIsSessionTotal: true})
	stopped := f.run("s1", "s1", ports.OutcomeStopped, ports.Usage{Tokens: tokens(1_000), CostIsSessionTotal: true})
	wantFloat(t, "stopped run's cost", stopped.Usage.CostUSD, nil)
	got := f.run("s1", "s1", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.50), CostIsSessionTotal: true})
	wantFloat(t, "cost", got.Usage.CostUSD, usd(0.20))
}

// A resumed session with no total in the log has an unknown last total.
func TestUsageResumedSessionWithoutAnEarlierTotal(t *testing.T) {
	f := newUsageFixture(t)
	f.run("s1", "", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.30), CostIsSessionTotal: true})
	got := f.run("s9", "s9", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.50), CostIsSessionTotal: true})
	wantFloat(t, "cost", got.Usage.CostUSD, nil)
	wantFloat(t, "session total", got.SessionCostUSD, usd(0.50))
}

// Earlier runs are found by the session that was resumed, even when the
// runner reports a new id for the resumed run.
func TestUsageFindsEarlierRunsByTheResumedSession(t *testing.T) {
	f := newUsageFixture(t)
	f.run("s1", "", ports.OutcomeFailed, ports.Usage{CostUSD: usd(0.30), CostIsSessionTotal: true})
	got := f.run("s1-fork", "s1", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.50), CostIsSessionTotal: true})
	wantFloat(t, "cost", got.Usage.CostUSD, usd(0.20))
	if got.SessionID != "s1-fork" {
		t.Errorf("session = %q, want the reported one", got.SessionID)
	}
}

// A runner whose cost is the run's own keeps it as reported, resumed or
// not.
func TestUsageCostOfTheRunAlone(t *testing.T) {
	f := newUsageFixture(t)
	f.run("s1", "", ports.OutcomeFailed, ports.Usage{CostUSD: usd(0.30)})
	got := f.run("s1", "s1", ports.OutcomeComplete, ports.Usage{CostUSD: usd(0.10)})
	wantFloat(t, "cost", got.Usage.CostUSD, usd(0.10))
	wantFloat(t, "session total", got.SessionCostUSD, nil)
}

func TestUsageEndRunOfAMissingRun(t *testing.T) {
	f := newUsageFixture(t)
	err := f.rec.EndRun(f.ctx, ports.RunRecord{ID: 99, Ant: f.ant}, ports.Usage{}, "")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("EndRun of a missing run = %v, want not found", err)
	}
}

func TestUsageEndRound(t *testing.T) {
	tests := []struct {
		name     string
		reported ports.Usage
		want     ports.Usage
	}{
		{"session total is the round's cost", ports.Usage{Tokens: tokens(9_000), CostUSD: usd(0.40), CostIsSessionTotal: true}, ports.Usage{Tokens: tokens(9_000), CostUSD: usd(0.40)}},
		{"cost of the round alone", ports.Usage{CostUSD: usd(0.40)}, ports.Usage{CostUSD: usd(0.40)}},
		{"no usage data", ports.Usage{}, ports.Usage{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := testkit.NewStateStore()
			pr := storetest.PR(7)
			r := ports.RoundRecord{PR: pr, Head: "abc", StartedAt: usageStart}
			id, err := store.StartRound(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			r.ID, r.EndedAt = id, usageStart.Add(10*time.Minute)
			if err := app.NewUsageRecorder(store).EndRound(ctx, r, tc.reported); err != nil {
				t.Fatal(err)
			}
			rounds, err := store.Rounds(ctx, pr)
			if err != nil {
				t.Fatal(err)
			}
			got := rounds[0]
			wantTokens(t, got.Usage.Tokens, tc.want.Tokens)
			wantFloat(t, "cost", got.Usage.CostUSD, tc.want.CostUSD)
			if got.Usage.CostIsSessionTotal {
				t.Error("stored cost is marked as a session total")
			}
			if got.EndedAt != r.EndedAt {
				t.Errorf("EndedAt = %v, want %v", got.EndedAt, r.EndedAt)
			}
		})
	}
}
