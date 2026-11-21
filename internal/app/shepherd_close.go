package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
)

// activate marks pr as having work in progress on this instance, a round
// or a fix pass, and returns the context that work runs with: it outlives
// the trigger's ctx, and StopPR cancels it.
func (s *Shepherd) activate(ctx context.Context, pr domain.Ref) context.Context {
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[pr] = true
	s.cancels[pr] = cancel
	return wctx
}

// ActivePRs lists, sorted, the PRs with a round or a fix pass in progress
// on this instance, so that PR closure checks whether they ended.
func (s *Shepherd) ActivePRs() []domain.Ref {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Ref, 0, len(s.active))
	for pr := range s.active {
		out = append(out, pr)
	}
	slices.SortFunc(out, func(a, b domain.Ref) int { return strings.Compare(a.Full(), b.Full()) })
	return out
}

// StopPR stops all shepherd activity on a PR that was merged or closed
// (spec pr-shepherd, "PR closure is human"): the round or fix pass in
// progress is ended, which releases the review claim once its agent has
// ended and pushes nothing, and the PR's pending /anthill review requests
// are dropped. Later triggers do nothing on the PR, since it has ended.
func (s *Shepherd) StopPR(ctx context.Context, pr domain.Ref) error {
	s.mu.Lock()
	if cancel, ok := s.cancels[pr]; ok {
		cancel()
	}
	s.mu.Unlock()
	requests, err := s.store.RoundRequests(ctx, pr)
	if err != nil {
		return fmt.Errorf("reading the round requests of %s: %w", pr, err)
	}
	var errs []error
	for _, r := range requests {
		if err := s.store.DropRoundRequest(ctx, pr, r.CommentID); err != nil {
			errs = append(errs, fmt.Errorf("dropping the round request %s on %s: %w", r.CommentID, pr, err))
		}
	}
	return errors.Join(errs...)
}
