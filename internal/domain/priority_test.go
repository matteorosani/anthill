package domain_test

import (
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestPriorityLabel(t *testing.T) {
	priority := []string{"urgent", "high", "low"}
	tests := []struct {
		name      string
		labels    []string
		wantRank  int
		wantLabel string
	}{
		{"none", []string{"anthill", "bug"}, -1, ""},
		{"one", []string{"anthill", "low"}, 2, "low"},
		{"highest of several wins", []string{"low", "High", "anthill"}, 1, "high"},
		{"no labels", nil, -1, ""},
	}
	for _, tt := range tests {
		rank, label := domain.PriorityLabel(tt.labels, priority)
		if rank != tt.wantRank || label != tt.wantLabel {
			t.Errorf("%s: PriorityLabel = %d, %q, want %d, %q", tt.name, rank, label, tt.wantRank, tt.wantLabel)
		}
	}
	if rank, label := domain.PriorityLabel([]string{"urgent"}, nil); rank != -1 || label != "" {
		t.Errorf("no priority labels configured: PriorityLabel = %d, %q", rank, label)
	}
}

func TestShowPriority(t *testing.T) {
	priority := []string{"urgent", "high"}
	tests := []struct {
		name string
		item domain.WorkItem
		want string
	}{
		{"provider priority", domain.WorkItem{Priority: 2}, "P2"},
		{"provider priority wins over a label", domain.WorkItem{Priority: 3, Labels: []string{"urgent"}}, "P3"},
		{"label", domain.WorkItem{Labels: []string{"High"}}, "high"},
		{"neither", domain.WorkItem{Labels: []string{"anthill"}}, domain.NoPriority},
	}
	for _, tt := range tests {
		if got := domain.ShowPriority(tt.item, priority); got != tt.want {
			t.Errorf("%s: ShowPriority = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// Spec task-eligibility, "Proposal list": provider priority, then
// priority label, then age.
func TestSortProposalsByPriority(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	other := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}
	item := func(repo domain.Repository, n, priority int, age time.Duration, labels ...string) domain.WorkItem {
		return domain.WorkItem{
			Ref:      domain.Ref{Repo: repo, Kind: domain.KindItem, Number: n},
			Priority: priority, CreatedAt: t0.Add(-age), Labels: labels,
		}
	}
	items := []domain.WorkItem{
		item(ghRepo, 1, 0, 9*time.Hour),                   // oldest, no priority
		item(ghRepo, 2, 0, time.Hour, "low"),              // label low
		item(ghRepo, 3, 4, time.Minute),                   // provider P4
		item(ghRepo, 4, 1, time.Minute),                   // provider P1
		item(ghRepo, 5, 0, time.Minute, "urgent"),         // label urgent
		item(ghRepo, 6, 4, time.Hour),                     // provider P4, older than 3
		item(ghRepo, 7, 0, 2*time.Hour, "low", "anthill"), // label low, older than 2
		item(other, 8, 0, 10*time.Hour, "urgent"),         // urgent is not a priority label there
		item(other, 9, 0, 30*time.Second, "p0"),           // the top label there, younger than 5
		item(ghRepo, 10, 0, 5*time.Hour),                  // no priority, younger than 1
	}
	labels := func(i domain.WorkItem) []string {
		if i.Ref.Repo == other {
			return []string{"p0"}
		}
		return []string{"urgent", "high", "low"}
	}
	domain.SortProposals(items, labels)
	var got []int
	for _, i := range items {
		got = append(got, i.Ref.Number)
	}
	// P1, P4 by age, then label rank 0 (urgent here, p0 there) by age,
	// then low by age, then no priority by age.
	if want := []int{4, 6, 3, 5, 9, 7, 2, 8, 1, 10}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}
