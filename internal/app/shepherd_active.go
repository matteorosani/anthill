package app

import (
	"cmp"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
)

// ActiveRounds lists the PRs with a round in progress on this instance,
// for the queen console's status (spec queen-console, "Health and
// status").
func (s *Shepherd) ActiveRounds() []domain.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Ref, 0, len(s.active))
	for pr := range s.active {
		out = append(out, pr)
	}
	slices.SortFunc(out, func(a, b domain.Ref) int {
		if c := cmp.Compare(a.Repo.String(), b.Repo.String()); c != 0 {
			return c
		}
		return cmp.Compare(a.Number, b.Number)
	})
	return out
}
