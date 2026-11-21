package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ErrNoFixPasses is returned for a fix pass on a shepherd without a
// FixPassRunner.
var ErrNoFixPasses = errors.New("the shepherd has no fix pass runner")

// startFixPass carries out a StartFixPass decision (spec pr-shepherd,
// "Fix agent pass"): the pass runs in the background within the review
// claim the decision runs under, the round's own when it follows a
// round, and the playbook is asked again under that claim when it ends.
// Its effect, recorded by the pass on the PR: each finding is resolved
// or reclassified needs-developer, and a push is recorded as a fix
// head. The trigger ends here.
func (s *Shepherd) startFixPass(ctx context.Context, t *prTurn, d domain.StartFixPass) (bool, error) {
	if s.deps.Fixes == nil {
		return false, ErrNoFixPasses
	}
	runner, _, err := s.deps.Runners.SelectReview(ctx, t.ws.Settings)
	if err != nil {
		return false, err
	}
	round, err := s.localRound(ctx, t.pr.Ref, d.Head)
	if err != nil {
		return false, err
	}
	start := FixStart{PR: t.pr, Pass: d, Round: round, Workspace: t.ws, Runner: runner, Instance: t.held.Instance}
	held := t.held
	t.handedOff = true
	fctx := s.activate(ctx, t.pr.Ref)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.report(s.runFixPass(fctx, start, held))
	}()
	return false, nil
}

// runFixPass runs a fix pass and asks the playbook again under its
// review claim.
func (s *Shepherd) runFixPass(ctx context.Context, start FixStart, held ports.HeldClaim) error {
	pr := start.PR.Ref
	_, runErr := s.deps.Fixes.Run(ctx, start)
	// StopPR ends a pass when its PR was merged or closed (spec
	// pr-shepherd, "PR closure is human").
	if ctx.Err() != nil {
		runErr = nil
	}
	ctx = context.WithoutCancel(ctx)
	defer s.lockPR(pr)()
	s.setActive(pr, false)
	if runErr != nil {
		runErr = fmt.Errorf("fix pass on %s at %s: %w", pr, start.Pass.Head, runErr)
		if err := s.deps.Claims.Release(ctx, pr, domain.ClaimReview); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("releasing the review claim on %s: %w", pr, err))
		}
		return runErr
	}
	return s.decide(ctx, pr, &held)
}

// localRound returns this instance's last round on head, or 0.
func (s *Shepherd) localRound(ctx context.Context, pr domain.Ref, head string) (ports.RoundID, error) {
	rounds, err := s.store.Rounds(ctx, pr)
	if err != nil {
		return 0, fmt.Errorf("reading the rounds of %s: %w", pr, err)
	}
	for i := len(rounds) - 1; i >= 0; i-- {
		if rounds[i].Head == head {
			return rounds[i].ID, nil
		}
	}
	return 0, nil
}
