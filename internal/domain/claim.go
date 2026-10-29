package domain

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// DefaultLease is how long a claim stays valid without renewal (spec
// anthill-configuration).
const DefaultLease = 15 * time.Minute

// ClaimStatus is where a claim stands at a given moment (glossary).
type ClaimStatus string

// The claim statuses. Live and parked claims hold the work; expired and
// released claims count as no claim.
const (
	ClaimLive     ClaimStatus = "live"
	ClaimParked   ClaimStatus = "parked"
	ClaimExpired  ClaimStatus = "expired"
	ClaimReleased ClaimStatus = "released"
)

// Claim is one claim comment read back from a provider (spec
// work-claiming).
type Claim struct {
	Kind     ClaimKind
	Instance string
	// Ref is the short form of the item or PR the claim is for.
	Ref       string
	CommentID string
	Author    string
	// CreatedAt and UpdatedAt are the provider's timestamps of the claim
	// comment. Renewing edits the comment, which moves UpdatedAt.
	CreatedAt time.Time
	UpdatedAt time.Time
	Lease     time.Duration
	Parked    bool
	Released  bool
}

// ParseClaim reads a claim from a comment. It returns false when the
// comment has no valid claim marker.
func ParseClaim(c Comment) (Claim, bool) {
	m, ok := FindMarker(c.Body)
	if !ok || m.Kind != CommentClaim {
		return Claim{}, false
	}
	return Claim{
		Kind:      m.Claim,
		Instance:  m.Instance,
		Ref:       m.Ref,
		CommentID: c.ID,
		Author:    c.Author,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
		Lease:     time.Duration(m.LeaseMinutes) * time.Minute,
		Parked:    m.Parked,
		Released:  m.Released,
	}, true
}

// ExpiresAt is when the lease runs out: the provider's last update of the
// claim comment plus the lease in its marker. A parked claim does not
// expire.
func (c Claim) ExpiresAt() time.Time {
	return c.UpdatedAt.Add(c.Lease)
}

// Status tells where the claim stands at now. Callers must take now from
// the provider's server time (for example the Date header of the poll
// response), never from the local clock, so that clock skew between
// machines does not matter (design D3).
func (c Claim) Status(now time.Time) ClaimStatus {
	switch {
	case c.Released:
		return ClaimReleased
	case c.Parked:
		return ClaimParked
	case !now.Before(c.ExpiresAt()):
		return ClaimExpired
	}
	return ClaimLive
}

// Holds reports whether the claim is live or parked at now. Expired and
// released claims count as no claim. See Status for where now comes from.
func (c Claim) Holds(now time.Time) bool {
	s := c.Status(now)
	return s == ClaimLive || s == ClaimParked
}

// ResolveClaim finds who holds the claim of the given kind on ref among
// the comments of that item or PR. Only claims by authors with write
// access count, so a copied marker from an outside user is a human
// comment. Among the claims that hold at now, the earliest by provider
// creation time wins, with the comment id as tie-breaker. It returns false
// when no claim holds. See Claim.Status for where now comes from.
func ResolveClaim(comments []Comment, ref Ref, kind ClaimKind, writers UserSet, now time.Time) (Claim, bool) {
	var holding []Claim
	for _, comment := range comments {
		c, ok := ParseClaim(comment)
		if !ok || c.Kind != kind || c.Ref != ref.String() || !writers.Contains(c.Author) || !c.Holds(now) {
			continue
		}
		holding = append(holding, c)
	}
	if len(holding) == 0 {
		return Claim{}, false
	}
	return slices.MinFunc(holding, compareClaims), true
}

// compareClaims orders claims by creation time, then by comment id.
func compareClaims(a, b Claim) int {
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return compareCommentIDs(a.CommentID, b.CommentID)
}

// compareCommentIDs compares ids as numbers when both are numbers, as
// they are on both providers, and as strings otherwise.
func compareCommentIDs(a, b string) int {
	na, errA := strconv.ParseUint(a, 10, 64)
	nb, errB := strconv.ParseUint(b, 10, 64)
	if errA == nil && errB == nil {
		return cmp.Compare(na, nb)
	}
	return cmp.Compare(a, b)
}

// ClaimLost reports whether instance, which believed it held a claim, has
// lost it: no claim holds any more, or another instance's claim won. The
// holder then ends its work with reason "claim lost" (spec work-claiming,
// "Holder returns after expiry").
func ClaimLost(winner Claim, found bool, instance string) bool {
	return !found || winner.Instance != instance
}

// Marker returns the marker of the claim comment.
func (c Claim) Marker() Marker {
	minutes := int(c.Lease / time.Minute)
	return Marker{
		Version:  MarkerVersion,
		Instance: c.Instance,
		Kind:     CommentClaim,
		Ref:      c.Ref,
		ClaimFields: &ClaimFields{
			Claim:        c.Kind,
			LeaseMinutes: minutes,
			Parked:       c.Parked,
			Released:     c.Released,
		},
	}
}

// Comment renders the claim comment. until is shown to humans only, and is
// the poster's best estimate of when the lease ends; the lease itself
// always runs from the provider's timestamp.
func (c Claim) Comment(until time.Time) AnthillComment {
	role := RoleAnt
	if c.Kind == ClaimReview {
		role = RoleShepherd
	}
	var body string
	switch {
	case c.Released:
		body = fmt.Sprintf("Released by %s.", c.Instance)
	case c.Parked:
		body = fmt.Sprintf("Claimed by %s (parked while the ant is between runs).", c.Instance)
	default:
		body = fmt.Sprintf("Claimed by %s until %s UTC", c.Instance, until.UTC().Format("15:04"))
	}
	return AnthillComment{Role: role, Body: body, Marker: c.Marker()}
}
