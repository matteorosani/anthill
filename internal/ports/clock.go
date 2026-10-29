package ports

import "time"

// Clock gives the local time to application services, for what is timed
// on this machine: budgets, poll intervals and claim renewal (design D1).
// The domain never reads a clock; services pass it the time.
//
// Leases are not timed with Clock: they use the provider's server time
// from each read, such as ItemPage.ServerTime (design D3).
type Clock interface {
	Now() time.Time
	// After returns a channel that receives the time once d has passed.
	After(d time.Duration) <-chan time.Time
}
