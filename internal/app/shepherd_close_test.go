package app_test

import (
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Task 16.7: a PR merged during a round stops the shepherd: the round
// is ended, its review claim released, pending requests dropped, and no
// round starts on the PR again (spec pr-shepherd, "PR closure is human").
func TestShepherdStopsOnAMergedPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)
		release := make(chan struct{})
		a.rounds.result = func(app.RoundStart) (app.RoundResult, error) {
			<-release
			// A round ended by its context returns its error.
			return app.RoundResult{}, errors.New("review agent stopped")
		}

		a.trigger(pr)
		if got := a.shepherd.ActivePRs(); !slices.Equal(got, []domain.Ref{pr}) {
			t.Fatalf("active PRs = %v", got)
		}
		if err := a.store.RequestRound(e.ctx, ports.RoundRequest{PR: pr, CommentID: "c-review", At: e.clock.Now()}); err != nil {
			t.Fatal(err)
		}

		e.gh.EditPullRequest(pr, func(p *domain.PullRequest) { p.State = domain.PRMerged })
		if err := e.run(func() error { return a.shepherd.StopPR(e.ctx, pr) }); err != nil {
			t.Fatal(err)
		}
		close(release)
		e.settle()

		if got := a.shepherd.ActivePRs(); len(got) != 0 {
			t.Errorf("active PRs = %v", got)
		}
		a.noClaimsHeld()
		if reqs, _ := a.store.RoundRequests(e.ctx, pr); len(reqs) != 0 {
			t.Errorf("pending requests = %+v", reqs)
		}
		if errs := a.takeErrors(); len(errs) != 0 {
			t.Errorf("errors = %v", errs)
		}

		// The merged PR gets no more rounds, even after a push.
		e.push(pr, "c2")
		a.trigger(pr)
		if n := len(a.rounds.Starts()); n != 1 {
			t.Errorf("%d rounds, want 1", n)
		}
	})
}
