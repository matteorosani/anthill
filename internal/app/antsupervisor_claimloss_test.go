package app_test

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Spec work-claiming, "Holder returns after expiry": the instance that
// lost the claim ends the active run and makes the ant abandoned with
// reason "claim lost", keeps its worktree, and leaves the claim to
// Claiming: it is neither parked nor released here. A repeated call does
// nothing more.
func TestClaimLostEndsTheActiveRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 1)
		id := e.startRunning(12, "sess-12")
		worktree := e.ant(id).Worktree
		x := e.execOf(ghRef(12))

		for range 2 {
			e.mustDo(func() error { return e.sup.ClaimLost(e.ctx, ghRef(12), domain.ClaimTask, app.ReasonClaimLost) })
		}
		rec := e.wantState(id, domain.AntAbandoned)
		if r := lastReason(rec); r != app.ReasonClaimLost {
			t.Errorf("reason = %q", r)
		}
		if got := x.Stops(); !slices.Equal(got, []time.Duration{app.StopGrace}) {
			t.Errorf("stops = %v, want one", got)
		}
		if !e.claimed(12) {
			t.Error("the claim was released; Claiming releases a lost claim")
		}
		if got := e.git.Worktrees(supClone); !slices.Contains(got, worktree) {
			t.Errorf("worktrees = %v, want %s kept", got, worktree)
		}
		ends := e.ends.all()
		if len(ends) != 1 || ends[0].run.Outcome != ports.OutcomeStopped || ends[0].run.Reason != app.ReasonClaimLost {
			t.Errorf("ends = %+v", ends)
		}
	})
}

// An ant between runs, a review claim and an item without an ant are
// left alone: Claiming makes the ant abandoned itself.
func TestClaimLostWithoutAnActiveRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newActionsEnv(t, 1)
		id := e.startRunning(12, "sess-12")
		e.end(ghRef(12), ports.ExecExited)
		e.wantState(id, domain.AntFailed)

		e.mustDo(func() error { return e.sup.ClaimLost(e.ctx, ghRef(12), domain.ClaimTask, app.ReasonClaimLost) })
		e.wantState(id, domain.AntFailed)
		e.mustDo(func() error { return e.sup.ClaimLost(e.ctx, ghRef(12), domain.ClaimReview, app.ReasonClaimLost) })
		e.mustDo(func() error { return e.sup.ClaimLost(e.ctx, ghRef(14), domain.ClaimTask, app.ReasonClaimLost) })
		e.wantState(id, domain.AntFailed)
	})
}
