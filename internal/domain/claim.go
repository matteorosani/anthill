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
	// ReleasedBy is the developer whose /anthill release released the
	// claim, and LabelRemoved is set when a parked claim lost its claim
	// label. Both are found by ClaimView, not read from the marker.
	ReleasedBy   string
	LabelRemoved bool
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

// DefaultClaimRereadDelay is how long an instance waits after posting its
// claim comment before it re-reads the claims, to absorb the provider's
// read-after-write lag (design D3).
const DefaultClaimRereadDelay = 3 * time.Second

// ClaimView is what one read of an item or PR tells about its claims.
type ClaimView struct {
	// Comments holds the claim comments and the comments with an
	// /anthill release command, in any order.
	Comments []Comment
	// Labels are the item's or PR's current labels. ClaimLabel is the
	// claim label, DefaultClaimLabel when empty.
	Labels     []string
	ClaimLabel string
	// Writers are the users with write access to the repository. Only
	// their claims and release commands count, so a copied marker or a
	// command from an outside user changes nothing.
	Writers UserSet
	// Now is the provider's time of the read. See Claim.Status.
	Now time.Time
}

// claims returns the claims of the given kind on ref by users with write
// access, with the force-release rules applied (spec work-claiming,
// "Manual override"):
//   - an /anthill release by a user with write access, created after the
//     claim comment in provider order, releases the claim;
//   - a parked claim on an item or PR without the claim label is
//     released. A claim is parked only after its holder added the label,
//     so a missing label means a developer removed it. A live claim
//     without the label is left to its holder (see CheckClaim), because
//     the label is missing between the re-read and the mark.
func (v ClaimView) claims(ref Ref, kind ClaimKind) []Claim {
	var out []Claim
	for _, comment := range v.Comments {
		c, ok := ParseClaim(comment)
		if !ok || c.Kind != kind || c.Ref != ref.String() || !v.Writers.Contains(c.Author) {
			continue
		}
		if by, ok := v.releasedBy(c); ok {
			c.Released, c.ReleasedBy = true, by
		} else if c.Parked && !c.Released && !HasLabel(v.Labels, orDefault(v.ClaimLabel, DefaultClaimLabel)) {
			c.Released, c.LabelRemoved = true, true
		}
		out = append(out, c)
	}
	return out
}

// releasedBy finds the earliest /anthill release that a user with write
// access posted after the claim comment, and returns its author.
func (v ClaimView) releasedBy(c Claim) (string, bool) {
	var first *Comment
	for i, comment := range v.Comments {
		if !IsReleaseCommand(comment) || !v.Writers.Contains(comment.Author) {
			continue
		}
		if compareComments(comment, c.CreatedAt, c.CommentID) <= 0 {
			continue
		}
		if first == nil || compareComments(comment, first.CreatedAt, first.ID) < 0 {
			first = &v.Comments[i]
		}
	}
	if first == nil {
		return "", false
	}
	return first.Author, true
}

// compareComments orders a comment against another comment's creation
// time and id, in provider order.
func compareComments(c Comment, created time.Time, id string) int {
	if r := c.CreatedAt.Compare(created); r != 0 {
		return r
	}
	return compareCommentIDs(c.ID, id)
}

// IsReleaseCommand reports whether a human comment holds /anthill
// release. Comments with a marker are Anthill's and never hold commands.
func IsReleaseCommand(c Comment) bool {
	if _, ok := FindMarker(c.Body); ok {
		return false
	}
	return slices.Contains(ParseCommands(c.Body), CmdRelease)
}

// IsClaimComment reports whether a comment matters to claims: it carries
// a claim marker or holds /anthill release. Claim stores return these
// comments (see ClaimView).
func IsClaimComment(c Comment) bool {
	if m, ok := FindMarker(c.Body); ok {
		return m.Kind == CommentClaim
	}
	return IsReleaseCommand(c)
}

// ResolveClaim finds who holds the claim of the given kind on ref. Only
// claims by authors with write access count, and force-released claims
// do not (see ClaimView). Among the claims that hold at v.Now, the
// earliest by provider creation time wins, with the comment id as
// tie-breaker. It returns false when no claim holds.
func ResolveClaim(v ClaimView, ref Ref, kind ClaimKind) (Claim, bool) {
	var holding []Claim
	for _, c := range v.claims(ref, kind) {
		if c.Holds(v.Now) {
			holding = append(holding, c)
		}
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

// ClaimLoss is why a holder lost its claim.
type ClaimLoss string

// The causes of a lost claim.
const (
	// LossTaken: another claim wins, such as one posted after the
	// holder's lease expired.
	LossTaken ClaimLoss = "taken"
	// LossExpired: the lease ran out and no other claim holds.
	LossExpired ClaimLoss = "expired"
	// LossForceReleased: a developer commented /anthill release.
	LossForceReleased ClaimLoss = "force-released"
	// LossLabelRemoved: a developer removed the claim label.
	LossLabelRemoved ClaimLoss = "label-removed"
	// LossCommentGone: the claim comment was deleted, or marked released
	// by someone else.
	LossCommentGone ClaimLoss = "comment-gone"
)

// ClaimCheck is what a holder learns about its claim from one read.
type ClaimCheck struct {
	// Own is the holder's claim as read. It is zero for LossCommentGone.
	Own Claim
	// Lost is empty while the holder still holds the claim.
	Lost ClaimLoss
	// Winner is another instance's claim that holds instead, whatever
	// the cause of the loss, or zero when none holds.
	Winner Claim
}

// Held reports whether the holder still holds its claim.
func (c ClaimCheck) Held() bool {
	return c.Lost == ""
}

// OtherHolds reports whether another claim holds, so that the claim
// label and the assignee are no longer the holder's to remove.
func (c ClaimCheck) OtherHolds() bool {
	return c.Winner.CommentID != ""
}

// Reason tells the queen why the claim was lost, such as "claimed by
// lbianchi@DESKTOP-7". It is "" while the claim holds.
func (c ClaimCheck) Reason() string {
	switch c.Lost {
	case LossTaken:
		return "claimed by " + c.Winner.Instance
	case LossExpired:
		return "lease expired"
	case LossForceReleased:
		return "released by " + c.Own.ReleasedBy
	case LossLabelRemoved:
		return "claim label removed"
	case LossCommentGone:
		return "claim comment removed"
	}
	return ""
}

// CheckClaim tells the holder of the claim comment commentID on ref
// whether it still holds the claim (spec work-claiming, "Leases and
// renewal" and "Manual override"). It is for a claim that its holder has
// marked, so a missing claim label always means a developer removed it,
// whether the claim is live or parked.
func CheckClaim(v ClaimView, ref Ref, kind ClaimKind, instance, commentID string) ClaimCheck {
	var check ClaimCheck
	winner, found := ResolveClaim(v, ref, kind)
	if found && winner.CommentID != commentID {
		check.Winner = winner
	}
	claims := v.claims(ref, kind)
	i := slices.IndexFunc(claims, func(c Claim) bool { return c.CommentID == commentID })
	if i < 0 || claims[i].Instance != instance {
		check.Lost = LossCommentGone
		return check
	}
	check.Own = claims[i]
	switch {
	case check.Own.ReleasedBy != "":
		check.Lost = LossForceReleased
	case check.Own.Released && !check.Own.LabelRemoved:
		check.Lost = LossCommentGone
	case !HasLabel(v.Labels, orDefault(v.ClaimLabel, DefaultClaimLabel)):
		check.Lost = LossLabelRemoved
	case check.OtherHolds():
		check.Lost = LossTaken
	case !found:
		check.Lost = LossExpired
	}
	return check
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
