// Package systemclock is the ports.Clock of the running daemon: the
// machine's own clock.
package systemclock

import (
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// Clock reads the machine's clock.
type Clock struct{}

var _ ports.Clock = Clock{}

// Now returns the current local time.
func (Clock) Now() time.Time {
	return time.Now()
}

// After returns a channel that receives the time once d has passed.
func (Clock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}
