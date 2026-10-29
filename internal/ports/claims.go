package ports

import (
	"context"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// ClaimPage is what ClaimStore.Claims reads for one item or PR.
type ClaimPage struct {
	// Comments holds at least every comment that carries a claim marker,
	// in provider creation order. domain.ResolveClaim finds the holder.
	Comments []domain.Comment
	// Revision is the item's provider revision, the precondition for Mark
	// on Azure DevOps. It is empty where the provider has none.
	Revision string
	// ServerTime is the provider's time when it answered, the now for
	// lease checks (design D3).
	ServerTime time.Time
}

// ClaimMark is what the winner of a claim adds (spec work-claiming,
// "Claim acquisition resolves races deterministically").
type ClaimMark struct {
	// Label is the claim label (default anthill:claimed).
	Label string
	// Assign adds the developer whose credentials Anthill uses as
	// assignee. It is set for task claims on tickets only.
	Assign bool
	// Revision, when set, is the revision read by Claims. Azure DevOps
	// sends it as a JSON-Patch test on /rev and a mismatch fails with
	// ErrConflict, which counts as a lost race (design D3).
	Revision string
}

// ClaimStore keeps claims on the provider (spec work-claiming, design D2
// and D3). It covers tickets (task claims) and PRs (review claims). It is
// separate from TrackerPort and CodeHostPort so that claims could later
// move to a shared store without changing the claim protocol, which the
// Claiming service runs over these calls with domain.ResolveClaim.
type ClaimStore interface {
	// Claims re-reads the claim comments of an item or PR.
	Claims(ctx context.Context, ref domain.Ref) (ClaimPage, error)
	// Post posts a claim comment.
	Post(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error)
	// Edit replaces a claim comment's text, to renew, park or release the
	// claim. Renewal moves the comment's UpdatedAt (spec work-claiming,
	// "Leases and renewal").
	Edit(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error)
	// Retract deletes the claim comment of a lost race.
	Retract(ctx context.Context, ref domain.Ref, commentID string) error
	// Mark adds the claim label and, when m.Assign is set, the assignee.
	// assigned is true only when Anthill made the assignment, that is the
	// developer was not already assigned.
	Mark(ctx context.Context, ref domain.Ref, m ClaimMark) (assigned bool, err error)
	// Unmark removes the claim label, and the assignee when unassign is
	// set, which callers do only if Mark reported that Anthill made the
	// assignment (spec work-claiming, "Release").
	Unmark(ctx context.Context, ref domain.Ref, label string, unassign bool) error
}
