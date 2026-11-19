package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RoundRunner runs one shepherd round (spec pr-shepherd, "Review
// rounds"): in a temporary detached worktree of the head commit, a review
// agent in a new session through the executor, its findings from
// submit_review checked against the open threads, the inline comments
// and the summary comment, whose marker carries domain.RoundFields, so
// that every instance counts the round.
//
// The shepherd calls Run from a goroutine it owns, with the review claim
// held and the round already stored, so Run simply returns once the
// round has ended. The shepherd records the end and asks the playbook
// again.
type RoundRunner interface {
	// Run returns once the round has ended. A round that fails still
	// returns the usage its agent reported.
	Run(ctx context.Context, r RoundStart) (RoundResult, error)
}

// RoundStart is what a round needs.
type RoundStart struct {
	// Round is the stored round: its ID, PR, head commit and whether it is
	// requested.
	Round ports.RoundRecord
	// PR is the PR as read before the round, whose HeadCommit is
	// Round.Head.
	PR domain.PullRequest
	// Settings are the repository's effective settings, and Runner the
	// review runner (Runners.SelectReview).
	Settings domain.Effective
	Runner   domain.Runner
	// Instance is this instance's id, for the markers of the round's
	// comments, as the review claim holds it.
	Instance string
	// Earlier are the PR's earlier rounds, whose open findings the review
	// agent gets as listed data only (spec pr-shepherd, "Second round
	// starts fresh").
	Earlier []domain.PRRound
}

// RoundResult is how a round ended.
type RoundResult struct {
	// Findings are the round's findings as posted.
	Findings []domain.Finding
	// Usage is what the review agent's runner reported.
	Usage ports.Usage
}

// ackMarkerID is the marker ID of the acknowledgement of the command in
// commentID, which the command service posts.
func ackMarkerID(commentID string) string {
	return "cmd-" + commentID
}

// startRound carries out a StartRound decision. A requested round first
// acknowledges each /anthill review it serves, so the command runs once
// across instances. The round is then stored, with the requests it
// serves (its effect), and run in the background with the review claim,
// which it releases once the decisions after it are done. The trigger
// ends here.
func (s *Shepherd) startRound(ctx context.Context, t *prTurn, d domain.StartRound) (bool, error) {
	runner, _, err := s.deps.Runners.SelectReview(ctx, t.ws.Settings)
	if err != nil {
		return false, err
	}
	if d.Requested {
		requests, err := s.store.RoundRequests(ctx, t.pr.Ref)
		if err != nil {
			return false, fmt.Errorf("reading the round requests of %s: %w", t.pr.Ref, err)
		}
		for _, r := range requests {
			text := fmt.Sprintf("Starting a requested review round on %s.", d.Head)
			if err := s.deps.Acks.Acknowledge(ctx, t.pr.Ref, r.CommentID, text); err != nil {
				return false, fmt.Errorf("acknowledging /anthill review in %s on %s: %w", r.CommentID, t.pr.Ref, err)
			}
		}
	}
	rec := ports.RoundRecord{PR: t.pr.Ref, Head: d.Head, Requested: d.Requested, StartedAt: s.clock.Now()}
	if rec.ID, err = s.store.BeginRound(ctx, rec); err != nil {
		return false, fmt.Errorf("starting a round on %s: %w", t.pr.Ref, err)
	}
	start := RoundStart{
		Round:    rec,
		PR:       t.pr,
		Settings: t.ws.Settings,
		Runner:   runner,
		Instance: t.held.Instance,
		Earlier:  t.snap.Rounds,
	}
	held := t.held
	t.handedOff = true
	s.setActive(t.pr.Ref, true)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.report(s.runRound(context.WithoutCancel(ctx), start, held))
	}()
	return false, nil
}

// runRound runs a round, records its end, and asks the playbook again
// under the round's review claim.
func (s *Shepherd) runRound(ctx context.Context, start RoundStart, held ports.HeldClaim) error {
	pr := start.Round.PR
	res, runErr := s.deps.Rounds.Run(ctx, start)
	if runErr != nil {
		runErr = fmt.Errorf("round %d on %s: %w", start.Round.ID, pr, runErr)
	}
	rec := start.Round
	rec.EndedAt = s.clock.Now()
	if err := s.deps.RoundEnds.EndRound(ctx, rec, res.Usage); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("recording the end of round %d: %w", rec.ID, err))
	}
	defer s.lockPR(pr)()
	s.setActive(pr, false)
	if runErr != nil {
		// Release the claim and leave the PR to the next trigger.
		if err := s.deps.Claims.Release(ctx, pr, domain.ClaimReview); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("releasing the review claim on %s: %w", pr, err))
		}
		return runErr
	}
	return s.decide(ctx, pr, &held)
}

// RoundLimitMarkerID is the marker ID of the round limit comment, so that
// it is posted once per PR whichever instance posts it.
const RoundLimitMarkerID = "round-limit"

// postRoundLimit carries out a PostRoundLimit decision. The comment is
// posted idempotently by its marker ID; its effect is the comment itself.
func (s *Shepherd) postRoundLimit(ctx context.Context, t *prTurn, d domain.PostRoundLimit) (bool, error) {
	_, err := t.host.AddComment(ctx, t.pr.Ref, domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: domain.RoundLimitText(d.Limit),
		Marker: domain.Marker{
			Version:  domain.MarkerVersion,
			ID:       RoundLimitMarkerID,
			Instance: t.held.Instance,
			Kind:     domain.CommentStatus,
			Ref:      t.pr.Ref.String(),
		},
	})
	if err != nil {
		return false, fmt.Errorf("posting the round limit comment on %s: %w", t.pr.Ref, err)
	}
	return true, nil
}

// snapshot builds what the playbook decides a PR from (design D22).
func (s *Shepherd) snapshot(ctx context.Context, pr domain.PullRequest, ws ToolWorkspace) (domain.PRSnapshot, error) {
	settings := ws.Settings
	antPR := domain.IsAntPR(pr, settings.BranchPrefix)
	snap := domain.PRSnapshot{
		Head:       pr.HeadCommit,
		Draft:      pr.Draft,
		AntPR:      antPR,
		InScope:    domain.InScope(settings.ShepherdScope, pr, antPR),
		Autofix:    !antPR && domain.HasLabel(pr.Labels, domain.LabelAutofix),
		RoundLimit: settings.RoundLimit,
		// Whether a fork's branch takes Anthill's push is not known yet,
		// so no fix pass is decided (spec pr-shepherd, "Fork branch not
		// pushable").
		Pushable:            false,
		VerificationCommand: settings.VerificationCommand != "",
		LimitPosted:         limitPosted(pr),
	}
	if antPR {
		rec, err := s.store.AntByBranch(ctx, pr.SourceBranch)
		switch {
		case err == nil:
			snap.OwnAnt = !rec.State().Final()
		case !errors.Is(err, ports.ErrNotFound):
			return domain.PRSnapshot{}, fmt.Errorf("finding the ant of %s: %w", pr.Ref, err)
		}
	}
	writers := s.writers(ctx, pr.Ref.Repo)
	var err error
	if snap.RoundRequested, err = s.pendingRequests(ctx, pr, writers); err != nil {
		return domain.PRSnapshot{}, err
	}
	if snap.Rounds, err = s.rounds(ctx, pr, writers); err != nil {
		return domain.PRSnapshot{}, err
	}
	return snap, nil
}

// writers returns a check of write access to repo that asks the provider
// once per author.
func (s *Shepherd) writers(ctx context.Context, repo domain.Repository) func(login string) (bool, error) {
	seen := map[string]bool{}
	return func(login string) (bool, error) {
		if ok, found := seen[login]; found {
			return ok, nil
		}
		ok, err := s.deps.WriteAccess(ctx, repo, login)
		if err != nil {
			return false, fmt.Errorf("checking the write access of %s on %s: %w", login, repo, err)
		}
		seen[login] = ok
		return ok, nil
	}
}

// pendingRequests reports whether a /anthill review on the PR waits for
// a round. A request that another instance acknowledged was served by
// that instance's round (spec agent-conversation, "Commands run once"),
// so it is dropped. An acknowledgement of this instance's own does not
// drop it: the round it announced may not have been stored, such as
// after a crash, and serving it again is the round's effect.
func (s *Shepherd) pendingRequests(ctx context.Context, pr domain.PullRequest, writers func(string) (bool, error)) (bool, error) {
	requests, err := s.store.RoundRequests(ctx, pr.Ref)
	if err != nil {
		return false, fmt.Errorf("reading the round requests of %s: %w", pr.Ref, err)
	}
	self := s.deps.Instance(pr.Ref.Repo)
	pending := false
	for _, r := range requests {
		acked, err := ackedElsewhere(pr, r.CommentID, self, writers)
		if err != nil {
			return false, err
		}
		if !acked {
			pending = true
			continue
		}
		if err := s.store.DropRoundRequest(ctx, pr.Ref, r.CommentID); err != nil {
			return false, fmt.Errorf("dropping the round request %s on %s: %w", r.CommentID, pr.Ref, err)
		}
	}
	return pending, nil
}

// ackedElsewhere reports whether another instance acknowledged the
// command in commentID on the PR.
func ackedElsewhere(pr domain.PullRequest, commentID, self string, writers func(string) (bool, error)) (bool, error) {
	id := ackMarkerID(commentID)
	for _, c := range prComments(pr) {
		m, ok := domain.FindMarker(c.Body)
		if !ok || m.Kind != domain.CommentReply || m.ID != id || m.Instance == self {
			continue
		}
		if ok, err := writers(c.Author); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// rounds lists the PR's rounds, oldest first: those whose summary
// comment, by an author with write access, is on the PR, whichever
// instance ran them, then this instance's rounds in progress.
func (s *Shepherd) rounds(ctx context.Context, pr domain.PullRequest, writers func(string) (bool, error)) ([]domain.PRRound, error) {
	var out []domain.PRRound
	for _, c := range pr.Comments {
		r, ok := domain.RoundOf(c)
		if !ok {
			continue
		}
		if ok, err := writers(c.Author); err != nil {
			return nil, err
		} else if ok {
			out = append(out, r)
		}
	}
	local, err := s.store.Rounds(ctx, pr.Ref)
	if err != nil {
		return nil, fmt.Errorf("reading the rounds of %s: %w", pr.Ref, err)
	}
	for _, r := range local {
		if r.EndedAt.IsZero() {
			out = append(out, domain.PRRound{Head: r.Head, Requested: r.Requested})
		}
	}
	return out, nil
}

// prComments lists the PR's comments and the comments of its review
// threads.
func prComments(pr domain.PullRequest) []domain.Comment {
	out := pr.Comments
	for _, t := range pr.Threads {
		out = append(out[:len(out):len(out)], t.Comments...)
	}
	return out
}

// limitPosted reports whether the round limit comment is on the PR.
func limitPosted(pr domain.PullRequest) bool {
	for _, c := range pr.Comments {
		if m, ok := domain.FindMarker(c.Body); ok && m.Kind == domain.CommentStatus && m.ID == RoundLimitMarkerID {
			return true
		}
	}
	return false
}
