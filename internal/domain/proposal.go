package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// ProposalHash is the content hash kept with a declined proposal (spec
// task-eligibility, "Proposal list"): the item's title, description and
// acceptance criteria. A declined item is proposed again once the hash
// changes, such as after its description is edited ("Declined item
// changes"). Labels, claims and comments are left out, so that they do not
// bring a declined item back.
func ProposalHash(item WorkItem) string {
	h := sha256.New()
	for _, part := range []string{item.Title, item.Body, item.AcceptanceCriteria} {
		// Each part is written with its length, so that moving text
		// from one part to the next changes the hash.
		h.Write([]byte(strconv.Itoa(len(part)) + ":" + part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ProposalHidden tells whether a hidden entry still hides the item at
// now (spec queen-console, "Accepting and declining"): a snooze until its
// end, and a decline until the item's content hash differs from the hash
// kept when it was declined.
func ProposalHidden(item WorkItem, until time.Time, declinedHash string, now time.Time) bool {
	if !until.IsZero() {
		return now.Before(until)
	}
	return declinedHash == ProposalHash(item)
}
