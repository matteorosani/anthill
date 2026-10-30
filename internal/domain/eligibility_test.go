package domain_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

func ghItem(n int) domain.Ref {
	return domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: n}
}

func adoItem(n int) domain.Ref {
	return domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: n}
}

// eligibleItem returns issue #12 and a context in which it passes every
// prerequisite. Each test changes one thing.
func eligibleItem() (domain.WorkItem, domain.EligibilityContext) {
	item := domain.WorkItem{
		Ref:          ghItem(12),
		Title:        "Fix login timeout on Safari",
		Body:         "Login times out after 5 seconds on Safari.",
		State:        domain.StateOpen,
		Labels:       []string{"anthill", "bug"},
		Dependencies: []domain.Ref{ghItem(10)},
		Children:     []domain.Ref{ghItem(20)},
	}
	ctx := domain.EligibilityContext{
		States: map[domain.Ref]domain.ItemState{
			ghItem(10): domain.StateClosedCompleted,
			ghItem(20): domain.StateClosedCompleted,
		},
		Instance:            instance,
		TargetRepository:    "matteorosani/anthill",
		RepositoryAvailable: true,
	}
	return item, ctx
}

func TestEvaluateScenarios(t *testing.T) {
	tests := []struct {
		name   string
		change func(*domain.WorkItem, *domain.EligibilityContext)
		// unmet lists the unmet prerequisites, in spec order.
		unmet []domain.Prerequisite
		// reason must appear in the reason of the first unmet prerequisite.
		reason string
	}{
		{
			name:   "eligible item",
			change: func(*domain.WorkItem, *domain.EligibilityContext) {},
		},
		{
			name: "two prerequisites unmet: claimed by another machine and an open dependency",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) {
				c.ClaimHolder = "colleague@DESKTOP-7"
				c.States[ghItem(10)] = domain.StateOpen
			},
			unmet: []domain.Prerequisite{domain.PrereqDependencies, domain.PrereqNotClaimed},
		},
		{
			name:   "opt-in label removed",
			change: func(i *domain.WorkItem, _ *domain.EligibilityContext) { i.Labels = []string{"bug"} },
			unmet:  []domain.Prerequisite{domain.PrereqOptedIn},
			reason: "opt-in label anthill",
		},
		{
			name:   "opt-in label in another case",
			change: func(i *domain.WorkItem, _ *domain.EligibilityContext) { i.Labels = []string{"Anthill"} },
		},
		{
			name: "configured opt-in label",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				c.OptInLabel = "ai"
			},
			unmet:  []domain.Prerequisite{domain.PrereqOptedIn},
			reason: "opt-in label ai",
		},
		{
			name:   "closed item",
			change: func(i *domain.WorkItem, _ *domain.EligibilityContext) { i.State = domain.StateClosedCompleted },
			unmet:  []domain.Prerequisite{domain.PrereqOpen},
			reason: "closed-completed",
		},
		{
			name:   "dependency still open: the reason names #10",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.States[ghItem(10)] = domain.StateOpen },
			unmet:  []domain.Prerequisite{domain.PrereqDependencies},
			reason: "dependency #10 is open",
		},
		{
			name: "dependency abandoned",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) {
				c.States[ghItem(10)] = domain.StateClosedNotPlanned
			},
			unmet:  []domain.Prerequisite{domain.PrereqDependencies},
			reason: "dependency #10 closed as not planned — needs queen review",
		},
		{
			name:   "dependency Anthill cannot read",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { delete(c.States, ghItem(10)) },
			unmet:  []domain.Prerequisite{domain.PrereqDependencies},
			reason: "dependency not resolvable",
		},
		{
			name: "dependency written in the body is not a dependency",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				i.Body = "Depends on #10"
				i.Dependencies = nil
				c.States[ghItem(10)] = domain.StateOpen
			},
		},
		{
			name: "cross-provider dependency cannot be expressed",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				i.Dependencies = []domain.Ref{adoItem(10)}
				c.States[adoItem(10)] = domain.StateClosedCompleted
			},
			unmet:  []domain.Prerequisite{domain.PrereqDependencies},
			reason: "dependency not resolvable",
		},
		{
			name: "user story with open tasks: the reason names the open children",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				i.Ref = adoItem(1)
				i.Dependencies = nil
				i.Children = []domain.Ref{adoItem(2), adoItem(3)}
				c.States = map[domain.Ref]domain.ItemState{adoItem(2): domain.StateOpen, adoItem(3): domain.StateOpen}
			},
			unmet:  []domain.Prerequisite{domain.PrereqLeaf},
			reason: "open children: AB#2, AB#3",
		},
		{
			name:   "child Anthill cannot read",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { delete(c.States, ghItem(20)) },
			unmet:  []domain.Prerequisite{domain.PrereqLeaf},
			reason: "child #20 not resolvable",
		},
		{
			name: "children closed as not planned do not block the parent",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) {
				c.States[ghItem(20)] = domain.StateClosedNotPlanned
			},
		},
		{
			name: "labelled parent does not opt in children: the sub-issue has no label",
			change: func(i *domain.WorkItem, _ *domain.EligibilityContext) {
				p := ghItem(1)
				i.Parent = &p
				i.Labels = nil
			},
			unmet: []domain.Prerequisite{domain.PrereqOptedIn},
		},
		{
			name:   "claimed by a colleague: the reason names the claimant",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.ClaimHolder = "colleague@DESKTOP-7" },
			unmet:  []domain.Prerequisite{domain.PrereqNotClaimed},
			reason: "claimed by colleague@DESKTOP-7",
		},
		{
			name:   "claimed by this instance",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.ClaimHolder = instance },
		},
		{
			name: "repository not cloned",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) {
				c.TargetRepository = "contoso/api"
				c.RepositoryAvailable = false
			},
			unmet:  []domain.Prerequisite{domain.PrereqRepositoryAvailable},
			reason: "repository contoso/api not available on this machine",
		},
		{
			name:   "unmapped item",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.TargetRepository = "" },
			unmet:  []domain.Prerequisite{domain.PrereqRepositoryAvailable},
			reason: "not mapped",
		},
		{
			name:   "PR already open",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.OpenAntPR = true },
			unmet:  []domain.Prerequisite{domain.PrereqNoActiveWork},
			reason: "ant PR is open",
		},
		{
			name: "release does not hand over the PR: claim released, ant PR still open",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) {
				c.ClaimHolder = ""
				c.OpenAntPR = true
			},
			unmet: []domain.Prerequisite{domain.PrereqNoActiveWork},
		},
		{
			name:   "failed ant awaiting retry",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.LocalAnt = domain.AntFailed },
			unmet:  []domain.Prerequisite{domain.PrereqNoActiveWork},
			reason: "failed",
		},
		{
			name:   "abandoned ant leaves no active work",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.LocalAnt = domain.AntAbandoned },
		},
		{
			name:   "done ant leaves no active work",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.LocalAnt = domain.AntDone },
		},
		{
			name: "waiting for an answer: the reason links to the question",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) {
				c.PendingQuestion = &domain.Comment{ID: "77", URL: "https://github.com/matteorosani/anthill/issues/12#issuecomment-77"}
			},
			unmet:  []domain.Prerequisite{domain.PrereqNoPendingQuestion},
			reason: "https://github.com/matteorosani/anthill/issues/12#issuecomment-77",
		},
		{
			name:   "empty description: only a title",
			change: func(i *domain.WorkItem, _ *domain.EligibilityContext) { i.Body = "  \n " },
			unmet:  []domain.Prerequisite{domain.PrereqHasDescription},
		},
		{
			name:   "acceptance criteria required but missing",
			change: func(_ *domain.WorkItem, c *domain.EligibilityContext) { c.RequireAcceptanceCriteria = true },
			unmet:  []domain.Prerequisite{domain.PrereqHasDescription},
			reason: "acceptance criteria",
		},
		{
			name: "acceptance criteria heading with content",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				c.RequireAcceptanceCriteria = true
				i.Body = "Login fails.\n\n## Acceptance criteria\n\n- login works on Safari\n"
			},
		},
		{
			name: "acceptance criteria heading with a colon",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				c.RequireAcceptanceCriteria = true
				i.Body = "Login fails.\n\n### Acceptance Criteria:\n- login works\n"
			},
		},
		{
			name: "acceptance criteria as a bold line",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				c.RequireAcceptanceCriteria = true
				i.Body = "Login fails.\n**Acceptance Criteria:**\n**Given** Safari\n"
			},
		},
		{
			name: "empty acceptance criteria section",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				c.RequireAcceptanceCriteria = true
				i.Body = "Login fails.\n\n## Acceptance criteria\n\n## Notes\nnone\n"
			},
			unmet: []domain.Prerequisite{domain.PrereqHasDescription},
		},
		{
			name: "azure devops acceptance criteria field",
			change: func(i *domain.WorkItem, c *domain.EligibilityContext) {
				c.RequireAcceptanceCriteria = true
				i.AcceptanceCriteria = "Login works on Safari."
			},
		},
		{
			name:   "put on hold",
			change: func(i *domain.WorkItem, _ *domain.EligibilityContext) { i.Labels = append(i.Labels, "anthill:hold") },
			unmet:  []domain.Prerequisite{domain.PrereqNotExcluded},
			reason: "anthill:hold",
		},
		{
			name:   "hold label in another case",
			change: func(i *domain.WorkItem, _ *domain.EligibilityContext) { i.Labels = append(i.Labels, "Anthill:Hold") },
			unmet:  []domain.Prerequisite{domain.PrereqNotExcluded},
			reason: "anthill:hold",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, ctx := eligibleItem()
			tt.change(&item, &ctx)
			result := domain.Evaluate(item, ctx)

			var got []domain.Prerequisite
			for _, c := range result.Unmet() {
				got = append(got, c.Prerequisite)
			}
			if !slices.Equal(got, tt.unmet) {
				t.Fatalf("unmet = %v, want %v (results %+v)", got, tt.unmet, result.Unmet())
			}
			if result.Eligible() != (len(tt.unmet) == 0) {
				t.Errorf("Eligible() = %v with unmet %v", result.Eligible(), got)
			}
			if tt.reason != "" && !strings.Contains(result.Unmet()[0].Reason, tt.reason) {
				t.Errorf("reason = %q, want it to contain %q", result.Unmet()[0].Reason, tt.reason)
			}
		})
	}
}

func TestEvaluateReportsEveryPrerequisiteInOrder(t *testing.T) {
	item, ctx := eligibleItem()
	result := domain.Evaluate(item, ctx)
	want := []domain.Prerequisite{
		domain.PrereqOptedIn, domain.PrereqOpen, domain.PrereqDependencies, domain.PrereqLeaf,
		domain.PrereqNotClaimed, domain.PrereqRepositoryAvailable, domain.PrereqNoActiveWork,
		domain.PrereqNoPendingQuestion, domain.PrereqHasDescription, domain.PrereqNotExcluded,
	}
	var got []domain.Prerequisite
	for _, r := range result.Results {
		got = append(got, r.Prerequisite)
		if !r.Met || r.Reason != "" {
			t.Errorf("%s: met %v, reason %q; want met with no reason", r.Prerequisite, r.Met, r.Reason)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("prerequisites = %v, want %v", got, want)
	}
}

func TestEvaluateListsEveryUnmetPrerequisite(t *testing.T) {
	item := domain.WorkItem{Ref: ghItem(12), State: domain.StateClosedCompleted, Labels: []string{"anthill:hold"}}
	result := domain.Evaluate(item, domain.EligibilityContext{ClaimHolder: "other@PC", OpenAntPR: true, PendingQuestion: &domain.Comment{}})
	if got, want := len(result.Unmet()), 8; got != want {
		t.Errorf("unmet count = %d, want %d: %+v", got, want, result.Unmet())
	}
}

func TestCapacityFullDoesNotAffectEligibility(t *testing.T) {
	// Capacity is not an input of eligibility: the item stays eligible, and
	// accepting it while capacity is full creates a queued ant.
	item, ctx := eligibleItem()
	if !domain.Evaluate(item, ctx).Eligible() {
		t.Fatal("item is not eligible")
	}
	at := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	ant, err := domain.NewAnt(item.Ref, domain.AntQueued, time.Hour, domain.DefaultBudgetPolicy(), at, "accepted while capacity is full")
	if err != nil || ant.State() != domain.AntQueued {
		t.Fatalf("NewAnt = %v, %v; want a queued ant", ant, err)
	}
}

func TestBecameIneligibleInTheMeantime(t *testing.T) {
	item, ctx := eligibleItem()
	item.State = domain.StateClosedCompleted
	if got, want := domain.Evaluate(item, ctx).IneligibleMessage(), "no longer eligible: open"; got != want {
		t.Errorf("IneligibleMessage() = %q, want %q", got, want)
	}
	item.State = domain.StateOpen
	if got := domain.Evaluate(item, ctx).IneligibleMessage(); got != "" {
		t.Errorf("IneligibleMessage() of an eligible item = %q, want empty", got)
	}
}

func TestHasLabelIgnoresCase(t *testing.T) {
	labels := []string{"Anthill", "bug"}
	if !domain.HasLabel(labels, "anthill") || !domain.HasLabel(labels, "BUG") || domain.HasLabel(labels, "anthill:hold") {
		t.Errorf("HasLabel(%v) is case-sensitive or matches too much", labels)
	}
}
