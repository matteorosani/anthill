package testkit

import (
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// Clock is a ports.Clock whose time moves only when a test calls Advance
// or Set. Channels from After fire, in deadline order, once the time
// reaches their deadline.
type Clock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []clockWaiter
}

type clockWaiter struct {
	at time.Time
	ch chan time.Time
}

var _ ports.Clock = (*Clock)(nil)

// NewClock returns a clock set to now.
func NewClock(now time.Time) *Clock {
	return &Clock{now: now}
}

// Now returns the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After returns a channel that receives the time once the clock has moved
// d forward. A d of zero or less fires at once.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, clockWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

// Waiters is the number of After channels that have not fired yet, so a
// test can tell when a service is waiting before advancing.
func (c *Clock) Waiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set(c.now.Add(d))
}

// Set moves the clock to t, which must not be before its time.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.Before(c.now) {
		panic("testkit.Clock: time cannot go back")
	}
	c.set(t)
}

func (c *Clock) set(t time.Time) {
	c.now = t
	var left []clockWaiter
	// Fire in deadline order; the channels are buffered, so this never
	// blocks.
	for len(c.waiters) > 0 {
		next := 0
		for i, w := range c.waiters {
			if w.at.Before(c.waiters[next].at) {
				next = i
			}
		}
		w := c.waiters[next]
		c.waiters = append(c.waiters[:next], c.waiters[next+1:]...)
		if w.at.After(t) {
			left = append(left, w)
			continue
		}
		w.ch <- w.at
	}
	c.waiters = left
}
