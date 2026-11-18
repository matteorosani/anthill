package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Adopt takes up a claim that this instance holds on the provider but
// that is missing from the local store, such as after the local store was
// lost (spec ant-execution, "Session index recovery"). It reads the
// claims, and only when this instance's claim is the live or parked
// winner, stores it as held, with the comment, lease and parked flag the
// provider shows. It posts, edits and marks nothing. Whether Anthill
// assigned the developer is not recoverable, so the adopted claim says it
// did not, and its release never unassigns anyone.
//
// held is false when this instance does not hold the claim; nothing is
// stored then. A claim already in the local store is returned as it is.
func (c *Claiming) Adopt(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (h ports.HeldClaim, held bool, err error) {
	defer c.lock(ref, kind)()
	stored, err := c.store.HeldClaims(ctx)
	if err != nil {
		return ports.HeldClaim{}, false, fmt.Errorf("read the held claims: %w", err)
	}
	if i := slices.IndexFunc(stored, func(h ports.HeldClaim) bool { return h.Ref == ref && h.Kind == kind }); i >= 0 {
		return stored[i], true, nil
	}
	t, err := c.route(ctx, ref)
	if err != nil {
		return ports.HeldClaim{}, false, err
	}
	_, v, err := c.read(ctx, t, ref)
	if err != nil {
		return ports.HeldClaim{}, false, err
	}
	winner, found := domain.ResolveClaim(v, ref, kind)
	if !found || winner.Instance != t.Instance {
		return ports.HeldClaim{}, false, nil
	}
	h = ports.HeldClaim{
		Ref: ref, Kind: kind, Instance: t.Instance, CommentID: winner.CommentID, Label: t.Label,
		Parked: winner.Parked, Lease: winner.Lease, UpdatedAt: winner.UpdatedAt,
	}
	if err := c.store.SaveClaim(ctx, h); err != nil {
		return ports.HeldClaim{}, false, fmt.Errorf("store the claim on %s: %w", ref, err)
	}
	return h, true, nil
}
