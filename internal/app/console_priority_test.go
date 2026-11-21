package app_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
)

// Spec task-eligibility, "Proposal list": proposals are ordered by
// provider priority, then by the repository's priority labels, then by
// age, and show P<n>, the highest-ranked priority label or "-".
func TestConsoleProposalsByPriority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConsoleEnv(t)
		e.settings.PriorityLabels = []string{"urgent", "high"}
		// 12 is the youngest but has provider priority 3; 15 carries the
		// top priority label; 13 carries a lower one and is the oldest
		// of the rest; 14 has neither.
		e.gh.EditItem(ghRef(12), func(i *domain.WorkItem) { i.Priority = 3; i.CreatedAt = t0.Add(time.Hour) })
		e.gh.EditItem(ghRef(13), func(i *domain.WorkItem) {
			i.Labels = append(i.Labels, "High")
			i.CreatedAt = t0.Add(-2 * time.Hour)
		})
		e.gh.EditItem(ghRef(14), func(i *domain.WorkItem) { i.CreatedAt = t0.Add(-3 * time.Hour) })
		e.gh.EditItem(ghRef(15), func(i *domain.WorkItem) { i.Labels = append(i.Labels, "high", "urgent") })

		var list app.ProposalList
		e.mustDo(func() (err error) { list, err = e.console.Proposals(e.ctx); return err })
		var got []int
		var shown []string
		for _, p := range list.Proposals {
			got = append(got, p.Item.Number)
			shown = append(shown, p.Priority)
		}
		wantNumbers(t, "order", got, 12, 15, 13, 14)
		if want := []string{"P3", "urgent", "high", "-"}; len(shown) != 4 || shown[0] != want[0] || shown[1] != want[1] || shown[2] != want[2] || shown[3] != want[3] {
			t.Errorf("priorities = %q, want %q", shown, want)
		}
	})
}
