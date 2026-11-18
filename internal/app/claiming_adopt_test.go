package app_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// Task 14.8: after the local store is lost, the parked claim this
// instance holds on the provider is taken up again as it is: no post, no
// edit, no mark, and Assigned is not claimed (spec ant-execution,
// "Session index recovery").
func TestAdoptClaimAfterStoreLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		before := a.mustAcquire(ghRef(12), domain.ClaimTask)
		if err := a.claiming.Park(context.Background(), ghRef(12)); err != nil {
			t.Fatal(err)
		}
		a.store = testkit.NewStateStore()
		a = e.restart(a, "mrosani")
		posts, edits, marks := e.ops(testkit.OpPost, "mrosani"), e.ops(testkit.OpEdit, "mrosani"), e.ops(testkit.OpMark, "mrosani")

		h, held, err := a.claiming.Adopt(context.Background(), ghRef(12), domain.ClaimTask)
		if err != nil || !held {
			t.Fatalf("Adopt() = %+v, %v, %v", h, held, err)
		}
		if h.CommentID != before.CommentID || !h.Parked || h.Assigned || h.Instance != instanceA || h.Label != domain.DefaultClaimLabel || h.Lease != domain.DefaultLease {
			t.Errorf("adopted %+v", h)
		}
		if got := a.held(); len(got) != 1 || got[0] != h {
			t.Errorf("held claims %+v", got)
		}
		if e.ops(testkit.OpPost, "mrosani") != posts || e.ops(testkit.OpEdit, "mrosani") != edits || e.ops(testkit.OpMark, "mrosani") != marks {
			t.Error("Adopt wrote to the provider")
		}

		// Adopting again returns the stored claim.
		if again, held, err := a.claiming.Adopt(context.Background(), ghRef(12), domain.ClaimTask); err != nil || !held || again != h {
			t.Errorf("second Adopt() = %+v, %v, %v", again, held, err)
		}
	})
}

// Task 14.8: a claim held by another instance, or no claim at all, is not
// adopted.
func TestAdoptClaimNotHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		if _, held, err := a.claiming.Adopt(context.Background(), ghRef(12), domain.ClaimTask); err != nil || held {
			t.Errorf("Adopt() of an unclaimed item = %v, %v", held, err)
		}
		b.mustAcquire(ghRef(12), domain.ClaimTask)
		if _, held, err := a.claiming.Adopt(context.Background(), ghRef(12), domain.ClaimTask); err != nil || held {
			t.Errorf("Adopt() of B's claim = %v, %v", held, err)
		}
		if got := a.held(); len(got) != 0 {
			t.Errorf("A holds %+v", got)
		}
	})
}
