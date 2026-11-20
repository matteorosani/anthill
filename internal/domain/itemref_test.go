package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestResolveItemRef(t *testing.T) {
	other := domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "other"}
	both := []domain.Repository{ghRepo, adoCodeRepo}
	gh12 := domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 12}
	ado12 := domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: 12}
	tests := []struct {
		text  string
		repos []domain.Repository
		want  domain.Ref
	}{
		{"#12", []domain.Repository{ghRepo}, gh12},
		{"12", []domain.Repository{ghRepo}, gh12},
		{"gh-12", both, gh12},
		{"ado-12", both, ado12},
		{"AB#12", both, ado12},
		{"ab#12", both, ado12},
		{"matteorosani/anthill#12", []domain.Repository{ghRepo, other}, gh12},
		{"MatteoRosani/Anthill#12", []domain.Repository{ghRepo, other}, gh12},
		{"contoso/shop#12", both, ado12},
		{"contoso/shop/web#12", both, ado12},
		// Two entries of one project are one tracker repository.
		{"#12", []domain.Repository{adoCodeRepo, {Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "api"}}, ado12},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, err := domain.ResolveItemRef(tt.text, tt.repos)
			if err != nil || got != tt.want {
				t.Errorf("ResolveItemRef(%q) = %v, %v, want %v", tt.text, got, err, tt.want)
			}
		})
	}
}

func TestResolveItemRefRefuses(t *testing.T) {
	other := domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "other"}
	tests := []struct {
		text, want string
		repos      []domain.Repository
	}{
		{"#12", "it could be in matteorosani/anthill or matteorosani/other; write <repository>#12", []domain.Repository{ghRepo, other}},
		{"ado-12", "no repository of that provider is configured", []domain.Repository{ghRepo}},
		{"elsewhere/repo#12", "repository elsewhere/repo is not configured", []domain.Repository{ghRepo}},
		{"#012", "expected an item number", []domain.Repository{ghRepo}},
		{"#0", "expected an item number", []domain.Repository{ghRepo}},
		{"twelve", "expected an item number", []domain.Repository{ghRepo}},
		{"#12", "no repository", nil},
	}
	for _, tt := range tests {
		_, err := domain.ResolveItemRef(tt.text, tt.repos)
		if !errors.Is(err, domain.ErrInvalidRef) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ResolveItemRef(%q) = %v, want %q", tt.text, err, tt.want)
		}
	}
}

// Spec task-eligibility, "Declined item changes": a description edit
// changes the hash; labels and comments do not.
func TestProposalHash(t *testing.T) {
	item := domain.WorkItem{Title: "Fix login", Body: "Steps.", Labels: []string{"anthill"}}
	h := domain.ProposalHash(item)
	same := item
	same.Labels = []string{"anthill", "anthill:claimed"}
	same.Comments = []domain.Comment{{ID: "1", Body: "me too"}}
	if domain.ProposalHash(same) != h {
		t.Error("labels or comments changed the hash")
	}
	for name, edit := range map[string]func(*domain.WorkItem){
		"title":    func(i *domain.WorkItem) { i.Title = "Fix logout" },
		"body":     func(i *domain.WorkItem) { i.Body = "Steps to reproduce." },
		"criteria": func(i *domain.WorkItem) { i.AcceptanceCriteria = "Works." },
		"moved":    func(i *domain.WorkItem) { i.Title, i.Body = "Fix login Steps.", "" },
	} {
		changed := item
		edit(&changed)
		if domain.ProposalHash(changed) == h {
			t.Errorf("a %s edit kept the hash", name)
		}
	}
}

func TestProposalHidden(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	item := domain.WorkItem{Title: "Fix login", Body: "Steps."}
	h := domain.ProposalHash(item)
	edited := item
	edited.Body = "More steps."
	tests := []struct {
		name  string
		item  domain.WorkItem
		until time.Time
		hash  string
		want  bool
	}{
		{"declined, unchanged", item, time.Time{}, h, true},
		{"declined, then edited", edited, time.Time{}, h, false},
		{"snoozed", item, now.Add(time.Hour), "", true},
		{"snooze over", item, now, "", false},
		{"snoozed and edited", edited, now.Add(time.Hour), h, true},
	}
	for _, tt := range tests {
		if got := domain.ProposalHidden(tt.item, tt.until, tt.hash, now); got != tt.want {
			t.Errorf("%s: hidden = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSortProposals(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	item := func(n int, created time.Time) domain.WorkItem {
		return domain.WorkItem{Ref: domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: n}, CreatedAt: created}
	}
	items := []domain.WorkItem{item(3, t0.Add(time.Hour)), item(9, t0), item(1, t0.Add(time.Hour))}
	domain.SortProposals(items)
	var got []int
	for _, i := range items {
		got = append(got, i.Ref.Number)
	}
	if want := []int{9, 1, 3}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("order = %v, want %v", got, want)
	}
}
