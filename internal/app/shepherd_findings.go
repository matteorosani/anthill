package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// FindingsRouter is what the shepherd asks of ant supervision to hand a
// round's findings to the ant; the AntSupervisor implements it.
type FindingsRouter interface {
	RouteChange(ctx context.Context, id ports.AntID, c domain.AntChange) error
	Trigger(ctx context.Context, id ports.AntID) error
}

var (
	_ FindingsRouter  = (*AntSupervisor)(nil)
	_ FindingsHandler = (*Shepherd)(nil)
)

// HandleFindings takes a shepherd finding, or a round's summary, on an
// ant PR of this instance from the change reactor (spec change-reaction,
// "Shepherd findings on ant PRs"). It only triggers the shepherd on the
// PR: the playbook decides the hand-over, which is the only way findings
// reach the ant.
func (s *Shepherd) HandleFindings(ctx context.Context, in ChangeInput) error {
	return s.Trigger(ctx, in.Event.Ref)
}

// handFindings carries out a HandFindings decision: the findings go to
// the ant of the PR as one change, the round's summary comment, which
// the ant's playbook delivers to an active run or stores for its next
// follow-up run (spec change-reaction, "Shepherd findings on ant PRs").
// Its effect is a status comment on the PR with the marker ID
// domain.HandedMarkerID, so that every instance sees the round as
// handed. A change routed twice, such as after a crash before the
// comment, is stored once, keyed by the summary comment.
func (s *Shepherd) handFindings(ctx context.Context, t *prTurn, d domain.HandFindings) (bool, error) {
	pr := t.pr
	if s.deps.Ants == nil {
		return false, fmt.Errorf("%s: no ant supervision to hand findings to", pr.Ref)
	}
	rec, err := s.store.AntByBranch(ctx, pr.SourceBranch)
	if err != nil {
		return false, fmt.Errorf("finding the ant of %s: %w", pr.Ref, err)
	}
	summary, ok := roundSummary(pr, d.Head)
	if !ok {
		return false, fmt.Errorf("%s: no summary comment of the round on %s", pr.Ref, d.Head)
	}
	change := domain.AntChange{
		Event: domain.ChangeEvent{
			Ref: pr.Ref, Kind: domain.ChangeCommentAdded, Actor: summary.Author, At: summary.CreatedAt,
			Origin: domain.OriginAnthill, CommentID: summary.ID, Body: summary.Body,
		},
		Findings: d.Findings,
	}
	if err := s.deps.Ants.RouteChange(ctx, rec.ID, change); err != nil {
		return false, fmt.Errorf("handing the findings of the round on %s to ant %d: %w", d.Head, rec.ID, err)
	}
	_, err = t.host.AddComment(ctx, pr.Ref, domain.AnthillComment{
		Role: domain.RoleAnt,
		Body: domain.HandedText(d.Head, len(d.Findings)),
		Marker: domain.Marker{
			Version:  domain.MarkerVersion,
			ID:       domain.HandedMarkerID(d.Head),
			Instance: s.deps.Instance(pr.Ref.Repo),
			Kind:     domain.CommentStatus,
			Ref:      pr.Ref.String(),
		},
	})
	if err != nil {
		return false, fmt.Errorf("recording the hand-over of the round on %s: %w", d.Head, err)
	}
	// The comment may not show at once in the next read of the PR.
	s.handed.add(pr.Ref, d.Head)
	if err := s.deps.Ants.Trigger(ctx, rec.ID); err != nil && !errors.Is(err, ports.ErrNotFound) {
		return false, fmt.Errorf("triggering ant %d after its findings: %w", rec.ID, err)
	}
	return true, nil
}

// roundSummary is the last summary comment of the round on head.
func roundSummary(pr domain.PullRequest, head string) (domain.Comment, bool) {
	for i := len(pr.Comments) - 1; i >= 0; i-- {
		if r, ok := domain.RoundOf(pr.Comments[i], nil); ok && r.Head == head {
			return pr.Comments[i], true
		}
	}
	return domain.Comment{}, false
}

// handedRounds is the set of rounds this instance handed to its ants,
// by PR and head commit. The handed comment on the PR is what counts;
// this only covers the time until the code host shows it.
type handedRounds struct {
	mu    sync.Mutex
	heads map[domain.Ref]map[string]bool
}

func (h *handedRounds) add(pr domain.Ref, head string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.heads == nil {
		h.heads = map[domain.Ref]map[string]bool{}
	}
	if h.heads[pr] == nil {
		h.heads[pr] = map[string]bool{}
	}
	h.heads[pr][head] = true
}

func (h *handedRounds) has(pr domain.Ref, head string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.heads[pr][head]
}
