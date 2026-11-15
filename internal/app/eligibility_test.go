package app_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

type eligibilityEnv struct {
	gh    *testkit.Provider
	clock *testkit.Clock
	store *testkit.StateStore
	elig  *app.Eligibility
}

func newEligibilityEnv(available bool) *eligibilityEnv {
	clock := testkit.NewClock(t0)
	gh := testkit.NewProvider(clock)
	gh.AddWriter(shop, "mrosani", "lbianchi")
	gh.PutItem(domain.WorkItem{Ref: ghRef(12), Title: "Fix login", Body: "Steps.", State: domain.StateOpen, Labels: []string{"anthill"}})
	store := testkit.NewStateStore()
	claiming := app.NewClaiming(func(context.Context, domain.Ref) (app.ClaimTarget, error) {
		return app.ClaimTarget{Provider: gh.Client("mrosani"), Instance: instanceA, Label: domain.DefaultClaimLabel}, nil
	}, store, clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
	tracker := func(context.Context, domain.Ref) (ports.TrackerPort, error) { return gh.Client("mrosani"), nil }
	target := func(context.Context, domain.WorkItem) (app.ItemTarget, error) {
		return app.ItemTarget{Repository: shop, Available: available, Settings: domain.Defaults()}, nil
	}
	return &eligibilityEnv{gh: gh, clock: clock, store: store, elig: app.NewEligibility(tracker, claiming, target, store)}
}

func (e *eligibilityEnv) unmet(t *testing.T, own ports.AntID) []domain.Prerequisite {
	t.Helper()
	_, result, err := e.elig.Recheck(context.Background(), ghRef(12), own)
	if err != nil {
		t.Fatalf("Recheck() = %v", err)
	}
	var out []domain.Prerequisite
	for _, c := range result.Unmet() {
		out = append(out, c.Prerequisite)
	}
	return out
}

func TestRecheckOnFreshData(t *testing.T) {
	queuedAnt := func(e *eligibilityEnv) ports.AntID {
		ant, _ := domain.NewAnt(ghRef(12), domain.AntQueued, time.Hour, domain.DefaultBudgetPolicy(), t0, "capacity full")
		id, _ := e.store.CreateAnt(context.Background(), ports.AntRecord{Item: ghRef(12), Base: time.Hour, Budget: ant.Budget, History: ant.History()})
		return id
	}
	tests := []struct {
		name      string
		available bool
		setup     func(*eligibilityEnv) ports.AntID
		want      []domain.Prerequisite
	}{
		{name: "eligible", available: true, setup: func(*eligibilityEnv) ports.AntID { return 0 }},
		{
			name: "closed a minute earlier", available: true,
			setup: func(e *eligibilityEnv) ports.AntID {
				e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.State = domain.StateClosedCompleted })
				return 0
			},
			want: []domain.Prerequisite{domain.PrereqOpen},
		},
		{
			name: "claimed by another instance", available: true,
			setup: func(e *eligibilityEnv) ports.AntID {
				claim := domain.Claim{Kind: domain.ClaimTask, Instance: instanceB, Ref: ghRef(12).String(), Lease: domain.DefaultLease}
				e.gh.Comment(ghRef(12), "lbianchi", claim.Comment(t0.Add(domain.DefaultLease)).String())
				return 0
			},
			want: []domain.Prerequisite{domain.PrereqNotClaimed},
		},
		{
			name: "open dependency read fresh", available: true,
			setup: func(e *eligibilityEnv) ports.AntID {
				e.gh.PutItem(domain.WorkItem{Ref: ghRef(11), Title: "Blocker", State: domain.StateOpen})
				e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.Dependencies = []domain.Ref{ghRef(11)} })
				return 0
			},
			want: []domain.Prerequisite{domain.PrereqDependencies},
		},
		{
			name: "repository not cloned here", available: false,
			setup: func(*eligibilityEnv) ports.AntID { return 0 },
			want:  []domain.Prerequisite{domain.PrereqRepositoryAvailable},
		},
		{
			name: "another local ant", available: true,
			setup: func(e *eligibilityEnv) ports.AntID { queuedAnt(e); return 0 },
			want:  []domain.Prerequisite{domain.PrereqNoActiveWork},
		},
		{
			name: "the ant being started does not count", available: true,
			setup: queuedAnt,
		},
		{
			name: "unanswered question", available: true,
			setup: func(e *eligibilityEnv) ports.AntID {
				q := domain.AnthillComment{Role: domain.RoleAnt, Body: "Which endpoint?", Marker: domain.Marker{Version: domain.MarkerVersion, Instance: instanceB, Kind: domain.CommentQuestion, Ref: ghRef(12).String()}}
				e.gh.Comment(ghRef(12), "lbianchi", q.String())
				return 0
			},
			want: []domain.Prerequisite{domain.PrereqNoPendingQuestion},
		},
		{
			name: "question answered by a human", available: true,
			setup: func(e *eligibilityEnv) ports.AntID {
				q := domain.AnthillComment{Role: domain.RoleAnt, Body: "Which endpoint?", Marker: domain.Marker{Version: domain.MarkerVersion, Instance: instanceB, Kind: domain.CommentQuestion, Ref: ghRef(12).String()}}
				e.gh.Comment(ghRef(12), "lbianchi", q.String())
				e.gh.Comment(ghRef(12), "ada", "The v2 one.")
				return 0
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEligibilityEnv(tc.available)
			own := tc.setup(e)
			if got := e.unmet(t, own); !slices.Equal(got, tc.want) {
				t.Errorf("unmet = %v, want %v", got, tc.want)
			}
		})
	}
}
