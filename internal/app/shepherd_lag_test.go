package app_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
)

// readLag is the code host's read-after-write lag: shorter than the claim
// re-read delay, which absorbs it for claims (design D3), but long enough
// that the summary of a round is still hidden when the shepherd decides
// right after the round.
const readLag = domain.DefaultClaimRereadDelay - time.Second

// Issue #192: when the code host's reads lag behind its writes, the
// summary of the round this instance just posted is not visible yet. The
// shepherd still starts exactly one round per head commit.
func TestShepherdOneRoundPerHeadUnderReadLag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		e.gh.ReadLag(readLag)
		a := e.instance(instanceA, "mrosani", nil)
		oneRoundPerHead(a)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(pr)
		e.settle()
		equalHeads(t, "round under lag", a.reviewed(pr), "c1")
		a.noClaimsHeld()

		a.trigger(pr)
		equalHeads(t, "next trigger under lag", a.reviewed(pr), "c1")

		e.clock.Advance(readLag)
		a.trigger(pr)
		equalHeads(t, "summary visible", a.reviewed(pr), "c1")

		e.push(pr, "c2")
		a.trigger(pr)
		e.settle()
		equalHeads(t, "after a push", a.reviewed(pr), "c1", "c2")
		if n := len(a.rounds.Starts()); n != 2 {
			t.Errorf("review agents = %d, want 2", n)
		}
	})
}

// oneRoundPerHead makes a second round of the instance on the same head
// commit fail before it posts anything, so that a test of the bug ends
// with an error instead of starting rounds without end.
func oneRoundPerHead(in *shepherdInstance) {
	seen := map[string]bool{}
	in.rounds.result = func(r app.RoundStart) (app.RoundResult, error) {
		key := fmt.Sprintf("%s@%s requested=%t", r.Round.PR, r.Round.Head, r.Round.Requested)
		if seen[key] {
			return app.RoundResult{}, fmt.Errorf("second round on %s", key)
		}
		seen[key] = true
		return app.RoundResult{}, nil
	}
}

// Issue #192: a summary still hidden after the shepherd re-read the PR
// for it still counts, because the round is stored as posted. Rounds
// stored as posted count by their head commit until the PR shows them.
func TestShepherdHiddenSummaryCounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		oneRoundPerHead(a)
		a.rounds.hidden = true
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(pr)
		a.trigger(pr)
		e.clock.Advance(time.Hour)
		a.trigger(pr)
		equalHeads(t, "summary never visible", a.reviewed(pr), "c1")
		a.noClaimsHeld()

		e.push(pr, "c2")
		a.trigger(pr)
		equalHeads(t, "after a push", a.reviewed(pr), "c1", "c2")
	})
}

// Issue #192: a round that ended without posting its summary, such as
// after its agent crashed, did not review its head commit, so the next
// trigger starts a round on it again.
func TestShepherdRoundNotPostedDoesNotCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		e.gh.ReadLag(readLag)
		a := e.instance(instanceA, "mrosani", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)
		crash := true
		a.rounds.result = func(app.RoundStart) (app.RoundResult, error) {
			if crash {
				crash = false
				return app.RoundResult{}, errors.New("agent crashed")
			}
			return app.RoundResult{}, nil
		}

		a.trigger(pr)
		if errs := a.takeErrors(); len(errs) != 1 {
			t.Fatalf("errors = %v, want the crash", errs)
		}
		a.trigger(pr)
		e.settle()
		a.trigger(pr)
		equalHeads(t, "after a crash", a.reviewed(pr), "c1", "c1")
		rounds, _ := a.store.Rounds(e.ctx, pr)
		if len(rounds) != 2 || rounds[0].Posted || !rounds[1].Posted {
			t.Errorf("rounds = %+v, want the crashed round not posted and the next one posted", rounds)
		}
	})
}

// Issue #192: under read lag, automatic and requested rounds on the same
// head commit each count once, so the round limit holds.
func TestShepherdRequestedAndAutomaticRoundsUnderReadLag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		e.gh.ReadLag(readLag)
		a := e.instance(instanceA, "mrosani", func(s *domain.Effective) { s.RoundLimit = 1 })
		oneRoundPerHead(a)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(pr)
		if err := a.request(pr, "r1"); err != nil {
			t.Fatal(err)
		}
		e.settle()
		a.trigger(pr)
		equalHeads(t, "automatic and requested", a.reviewed(pr), "c1", "c1R")

		e.push(pr, "c2")
		a.trigger(pr)
		e.clock.Advance(time.Hour)
		a.trigger(pr)
		equalHeads(t, "limit reached", a.reviewed(pr), "c1", "c1R")
		if n := e.limitComments(pr); n != 1 {
			t.Errorf("%d limit comments, want 1", n)
		}
		a.noClaimsHeld()
	})
}

// Issue #192 and spec pr-shepherd, "Fix agent pass": under read lag, the
// shepherd re-reads the PR until the round's summary is visible, so the
// fix pass runs after the round within the round's review claim.
func TestShepherdFixPassWithinTheClaimUnderReadLag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		e.gh.ReadLag(readLag)
		in := newFixShepherd(e, nil, unusedImportFinding)
		pr := e.putPR(shop, 34, "feature/login", func(p *domain.PullRequest) { p.Labels = []string{domain.LabelAutofix} })

		in.trigger(pr)
		if got := in.fixes.Starts(); len(got) != 1 {
			t.Fatalf("%d fix passes after the round's trigger, want 1; rounds on %v", len(got), in.rounds.Heads())
		}
		e.clock.Advance(time.Hour)
		in.trigger(pr)
		if got := in.rounds.Heads(); !slices.Equal(got, []string{"c1"}) {
			t.Errorf("rounds on %v, want c1", got)
		}
		p, _ := e.gh.PullRequest(pr)
		claims := 0
		for _, c := range p.Comments {
			if m, ok := domain.FindMarker(c.Body); ok && m.Kind == domain.CommentClaim {
				claims++
			}
		}
		if claims != 1 {
			t.Errorf("%d review claims, want 1: the round's", claims)
		}
	})
}

// Issue #192 and spec pr-shepherd, "Automatic fixes": the head a fix pass
// pushed starts no round, even when the PR never shows the pass's status
// comment, because the shepherd stores the push.
func TestShepherdFixPushStartsNoRoundWhileHidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		in := newFixShepherd(e, nil, unusedImportFinding)
		in.fixes.hidden = true
		pr := e.putPR(shop, 34, "feature/login", func(p *domain.PullRequest) { p.Labels = []string{domain.LabelAutofix} })

		in.trigger(pr)
		e.clock.Advance(time.Hour)
		in.trigger(pr)
		if got := in.rounds.Heads(); !slices.Equal(got, []string{"c1"}) {
			t.Errorf("rounds on %v, want c1 only", got)
		}
		if got := in.fixes.Starts(); len(got) != 1 {
			t.Errorf("%d fix passes, want 1", len(got))
		}
		if got, err := in.store.FixPushes(e.ctx, pr); err != nil || !slices.Equal(got, []string{fixedHead}) {
			t.Errorf("fix pushes = %v, %v, want %s", got, err, fixedHead)
		}
	})
}

// Issue #192 and spec pr-shepherd, "No verification command": under read
// lag, the comment that auto-fix is not possible is posted once, and the
// trigger does not decide it again.
func TestShepherdAutofixUnavailableUnderReadLag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		e.gh.ReadLag(readLag)
		in := newFixShepherd(e, func(s *domain.Effective) { s.VerificationCommand = "" }, unusedImportFinding)
		pr := e.putPR(shop, 34, "feature/login", func(p *domain.PullRequest) { p.Labels = []string{domain.LabelAutofix} })

		in.trigger(pr)
		e.clock.Advance(time.Hour)
		in.trigger(pr)
		want := app.AutofixUnavailableMarkerID(domain.AutofixNoVerification)
		if got := e.statuses(pr); !slices.Equal(got, []string{want}) {
			t.Errorf("status comments = %v, want %s once", got, want)
		}
		if got := in.rounds.Heads(); !slices.Equal(got, []string{"c1"}) {
			t.Errorf("rounds on %v, want c1", got)
		}
	})
}
