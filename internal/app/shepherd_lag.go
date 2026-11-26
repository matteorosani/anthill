package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// A code host's reads can lag behind its writes (issue #192): right after
// the shepherd posts a comment, reading the PR may not show it yet. The
// playbook decides from the comments, so it would then see a decision's
// effect as missing and take the decision again: a second round on the
// same head commit, a round on the head a fix pass pushed, or the round
// limit or auto-fix unavailable comment once more. Each of these
// decisions is therefore followed by a bounded wait until the PR shows
// its comment (awaitPR), and has a fallback when it still does not:
//
//   - after a round posts its summary, runRound waits for the summary and
//     only then decides, within the round's review claim, so that the
//     decisions that need the round's findings, such as a fix pass, see
//     them. When the summary is still not visible, the round still
//     counts: Shepherd.rounds adds each round this instance stored as
//     posted whose summary the PR does not show yet, without its findings
//     (pendingRounds). Any decision on those findings, such as a fix pass
//     or a hand-off to the ant, is then taken on a later trigger, once
//     the summary is visible, possibly under a new review claim;
//   - after a fix pass pushes, runFixPass stores the pushed head
//     (ports.FixPushes), which the snapshot adds to its fix heads, and
//     waits for the pass's status comment before deciding;
//   - after the round limit or auto-fix unavailable comment, the handler
//     waits for it, and ends the trigger when it is still not visible,
//     instead of deciding again; the next trigger sees it.
//
// A hand-off of findings to the ant has its own cover (handedRounds).

// summaryWait is the longest the shepherd re-reads a PR for a comment it
// just posted (awaitPR). It is the claim re-read delay, which already
// absorbs the read-after-write lag for claims (design D3). It is far
// shorter than the claim's lease, which the daemon renews meanwhile.
const summaryWait = domain.DefaultClaimRereadDelay

// summaryPoll is the time between two re-reads in awaitPR.
const summaryPoll = time.Second

// roundKey is what tells this instance's rounds on a PR apart, as their
// summary markers carry it: the head commit and whether the round was
// requested. The marker has no round ID, so rounds with the same key are
// matched by count.
type roundKey struct {
	head      string
	requested bool
}

// ownSummaries counts, by roundKey, the round summaries on the PR that
// this instance posted, as markers by an author with write access say.
func ownSummaries(pr domain.PullRequest, self string, writers func(string) (bool, error)) (map[roundKey]int, error) {
	n := map[roundKey]int{}
	for _, c := range pr.Comments {
		m, ok := domain.FindMarker(c.Body)
		if !ok || m.Kind != domain.CommentSummary || m.RoundFields == nil || m.Instance != self {
			continue
		}
		if ok, err := writers(c.Author); err != nil {
			return nil, err
		} else if ok {
			n[roundKey{head: m.Head, requested: m.Requested}]++
		}
	}
	return n, nil
}

// pendingRounds returns the rounds in local, this instance's rounds on
// the PR oldest first, that the PR does not show yet: those in progress,
// and those stored as posted beyond the count of their summaries visible
// in own (ownSummaries). A round that ended without posting its summary
// is left out: it did not review its head commit.
func pendingRounds(local []ports.RoundRecord, own map[roundKey]int) []domain.PRRound {
	var out []domain.PRRound
	for _, r := range local {
		k := roundKey{head: r.Head, requested: r.Requested}
		switch {
		case r.EndedAt.IsZero():
		case !r.Posted:
			continue
		case own[k] > 0:
			own[k]--
			continue
		}
		out = append(out, domain.PRRound{Head: r.Head, Requested: r.Requested})
	}
	return out
}

// awaitPR re-reads pr until shown reports that the PR shows what the
// shepherd just posted, for at most summaryWait, and returns whether it
// does. It returns early, with false, when ctx ends, such as when the PR
// was merged or closed or the daemon stops.
func (s *Shepherd) awaitPR(ctx context.Context, pr domain.Ref, shown func(domain.PullRequest) (bool, error)) (bool, error) {
	host, err := s.deps.CodeHost(ctx, pr.Repo)
	if err != nil {
		return false, err
	}
	deadline := s.clock.Now().Add(summaryWait)
	for {
		p, _, err := host.PullRequest(ctx, pr)
		if err != nil {
			return false, fmt.Errorf("reading %s: %w", pr, err)
		}
		ok, err := shown(p)
		if err != nil || ok {
			return ok, err
		}
		left := deadline.Sub(s.clock.Now())
		if left <= 0 {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-s.clock.After(min(summaryPoll, left)):
		}
	}
}

// awaitSummary waits until the PR of rec, a round this instance just
// ended after posting its summary, shows every summary this instance
// stored as posted for the round's head commit (awaitPR). A summary still
// hidden afterwards is not an error: the round counts all the same.
func (s *Shepherd) awaitSummary(ctx context.Context, rec ports.RoundRecord) error {
	local, err := s.store.Rounds(ctx, rec.PR)
	if err != nil {
		return fmt.Errorf("reading the rounds of %s: %w", rec.PR, err)
	}
	k := roundKey{head: rec.Head, requested: rec.Requested}
	want := 0
	for _, r := range local {
		if r.Posted && (roundKey{head: r.Head, requested: r.Requested}) == k {
			want++
		}
	}
	self := s.deps.Instance(rec.PR.Repo)
	writers := s.writers(ctx, rec.PR.Repo)
	_, err = s.awaitPR(ctx, rec.PR, func(pr domain.PullRequest) (bool, error) {
		own, err := ownSummaries(pr, self, writers)
		return own[k] >= want, err
	})
	return err
}

// awaitMarker waits until the PR shows a comment whose marker ID is id
// (awaitPR) and returns whether it does.
func (s *Shepherd) awaitMarker(ctx context.Context, pr domain.Ref, id string) (bool, error) {
	return s.awaitPR(ctx, pr, func(p domain.PullRequest) (bool, error) {
		for _, c := range prComments(p) {
			if m, ok := domain.FindMarker(c.Body); ok && m.ID == id {
				return true, nil
			}
		}
		return false, nil
	})
}

// fixPushes adds to snap the heads this instance's fix passes pushed to
// the PR, as the store has them, to the fix heads the PR's comments give.
func (s *Shepherd) fixPushes(ctx context.Context, pr domain.Ref, snap *domain.PRSnapshot) error {
	heads, err := s.store.FixPushes(ctx, pr)
	if err != nil {
		return fmt.Errorf("reading the fix pushes of %s: %w", pr, err)
	}
	for _, h := range heads {
		if !slices.Contains(snap.FixHeads, h) {
			snap.FixHeads = append(snap.FixHeads, h)
		}
	}
	return nil
}
