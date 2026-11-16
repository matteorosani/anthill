package domain_test

import (
	"math"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func usd(v float64) *float64 { return &v }

func TestRunCost(t *testing.T) {
	tests := []struct {
		name         string
		reported     *float64
		sessionTotal bool
		last         *float64
		newSession   bool
		want         *float64
	}{
		{"cost of the run alone", usd(3.40), false, nil, false, usd(3.40)},
		{"cost of the run alone ignores the last total", usd(3.40), false, usd(5), false, usd(3.40)},
		{"no cost reported", nil, false, nil, true, nil},
		{"no running total reported", nil, true, usd(0.30), false, nil},
		// Scenario "resumed run with a cumulative cost".
		{"resumed run with a cumulative cost", usd(0.50), true, usd(0.30), false, usd(0.20)},
		// Scenario "new session with a cumulative cost".
		{"new session with a cumulative cost", usd(0.40), true, nil, true, usd(0.40)},
		{"new session ignores the last total", usd(0.40), true, usd(0.30), true, usd(0.40)},
		// Scenario "total lower than the last one".
		{"total lower than the last one", usd(0.25), true, usd(0.30), false, nil},
		{"last total unknown", usd(0.50), true, nil, false, nil},
		{"same total as the last one", usd(0.30), true, usd(0.30), false, usd(0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.RunCost(tc.reported, tc.sessionTotal, tc.last, tc.newSession)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("RunCost = %v, want unknown", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("RunCost = unknown, want %v", *tc.want)
			case tc.want != nil && math.Abs(*got-*tc.want) > 1e-9:
				t.Fatalf("RunCost = %v, want %v", *got, *tc.want)
			}
		})
	}
}

// RunCost returns a new value, so a caller that changes the result does
// not change the reported cost.
func TestRunCostCopies(t *testing.T) {
	reported := usd(0.40)
	got := domain.RunCost(reported, true, nil, true)
	*got = 1
	if *reported != 0.40 {
		t.Fatalf("reported changed to %v", *reported)
	}
}
