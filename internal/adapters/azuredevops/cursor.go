package azuredevops

import (
	"encoding/json"
	"fmt"
	"time"
)

// itemCursor is what a work item poll hands to the next one, stored by
// the caller as an opaque string (ports.PollState).
type itemCursor struct {
	// Since is the latest System.ChangedDate seen. The next poll asks for
	// items changed since Since minus overlap, so that a change saved
	// just before Since but visible only later is not missed.
	Since time.Time `json:"since"`
	// Revs holds the System.Rev of the items changed within the overlap
	// before Since. The next poll reads them again and drops those whose
	// revision did not move.
	Revs map[int]int `json:"revs,omitempty"`
}

// overlap is how far back each incremental poll reaches before the
// cursor (design D7).
const overlap = 2 * time.Minute

// prCursor is what a pull request poll hands to the next one.
type prCursor struct {
	// PRs maps each active PR's ID to the fingerprint of its content, so
	// that only changed PRs are returned.
	PRs map[int]string `json:"prs,omitempty"`
}

func parseCursor[T any](s string) (T, error) {
	var c T
	if s == "" {
		return c, nil
	}
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return c, fmt.Errorf("azure devops: invalid poll cursor: %w", err)
	}
	return c, nil
}

func cursorString(c any) string {
	data, _ := json.Marshal(c) // A cursor always marshals.
	return string(data)
}
