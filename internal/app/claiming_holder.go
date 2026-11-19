package app

import (
	"cmp"
	"context"
	"slices"

	"github.com/matteorosani/anthill/internal/domain"
)

// HolderAt returns the instance that held the live claim of the kind on
// ref when the comment was posted, or "" when none did, and this
// instance's id on ref's provider. Only the claim comments and release
// commands posted before the comment count, a release written since is
// taken back, and leases are judged at the comment's creation time. So
// every instance finds the same holder for one comment whenever it
// reads, even after that very comment released the claim (/anthill
// release). The commands use it to find the instance responsible for a
// command (spec agent-conversation, "Each command is executed once").
func (c *Claiming) HolderAt(ctx context.Context, ref domain.Ref, kind domain.ClaimKind, comment domain.Comment) (holder, instance string, err error) {
	t, err := c.route(ctx, ref)
	if err != nil {
		return "", "", err
	}
	_, v, err := c.read(ctx, t, ref)
	if err != nil {
		return "", "", err
	}
	v.Comments = domain.UnreleaseAfter(domain.CommentsBefore(v.Comments, comment), comment.CreatedAt)
	v.Now = comment.CreatedAt
	// The labels at that time are unknown. The claim label is taken as
	// present, so that a parked claim whose label a release removed
	// since still counts as held then.
	v.Labels = append(slices.Clone(v.Labels), cmp.Or(v.ClaimLabel, domain.DefaultClaimLabel))
	if winner, found := domain.ResolveClaim(v, ref, kind); found {
		holder = winner.Instance
	}
	return holder, t.Instance, nil
}

// Instance is this instance's id on the provider of ref, which the
// markers of its comments carry.
func (c *Claiming) Instance(ctx context.Context, ref domain.Ref) (string, error) {
	t, err := c.route(ctx, ref)
	if err != nil {
		return "", err
	}
	return t.Instance, nil
}
