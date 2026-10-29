package ports

import (
	"errors"
	"fmt"
	"time"
)

// Errors shared by the ports. Adapters return them, wrapped, so that
// application services can check for them with errors.Is whatever the
// technology behind the port.
var (
	// ErrNotFound: the item, PR, comment, branch or record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict: a write lost against a concurrent change, such as an
	// Azure DevOps revision precondition that failed (spec work-claiming,
	// "Azure DevOps optimistic concurrency") or a push rejected because
	// the remote branch moved (spec ant-tools, "Push tool").
	ErrConflict = errors.New("conflict")
	// ErrNoClone: a configured path does not hold a git clone of the
	// configured repository (spec anthill-configuration, "Repository
	// listed without a clone").
	ErrNoClone = errors.New("no git clone of the repository")
	// ErrHistoryRewritten: a push was rejected because the branch rewrote
	// commits that were already pushed (spec branching-policy, "History
	// rewriting").
	ErrHistoryRewritten = errors.New("published history rewritten")
	// ErrRateLimited: the provider refused the request until its rate
	// limit resets (spec work-item-sources, "Polling cadence"). Use
	// errors.As with *RateLimitError to read the reset time.
	ErrRateLimited = errors.New("rate limited")
	// ErrUnsupported: the adapter does not offer an optional capability,
	// such as live input to a run (design D6).
	ErrUnsupported = errors.New("not supported")
)

// RateLimitError says until when a provider refuses requests. It matches
// ErrRateLimited with errors.Is.
type RateLimitError struct {
	// Reset is the provider's reported reset time.
	Reset time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s until %s", ErrRateLimited, e.Reset.UTC().Format(time.RFC3339))
}

// Is makes errors.Is(err, ErrRateLimited) true for a *RateLimitError.
func (e *RateLimitError) Is(target error) bool {
	return target == ErrRateLimited
}
