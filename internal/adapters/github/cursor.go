package github

import (
	"encoding/json"
	"fmt"
	"time"
)

// cursor is what a poll hands to the next one, stored by the caller as
// an opaque string (ports.PollState).
type cursor struct {
	// Since is the latest updated_at seen. GitHub's since filter is
	// inclusive, so the item updated at exactly Since comes back once
	// more after a change; the caller's hashes absorb it.
	Since time.Time `json:"since"`
	// ETag is the entity tag of the last answer to the same URL, sent as
	// If-None-Match. Empty when the URL changed.
	ETag string `json:"etag,omitempty"`
}

func parseCursor(s string) (cursor, error) {
	var c cursor
	if s == "" {
		return c, nil
	}
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return cursor{}, fmt.Errorf("github: invalid poll cursor: %w", err)
	}
	return c, nil
}

func (c cursor) String() string {
	data, _ := json.Marshal(c) // A cursor always marshals.
	return string(data)
}
