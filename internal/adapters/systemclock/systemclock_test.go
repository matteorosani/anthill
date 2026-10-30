package systemclock_test

import (
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/systemclock"
)

func TestClock(t *testing.T) {
	var c systemclock.Clock
	before := time.Now()
	if now := c.Now(); now.Before(before) {
		t.Errorf("Now() = %v, before %v", now, before)
	}
	select {
	case <-c.After(time.Millisecond):
	case <-time.After(10 * time.Second):
		t.Fatal("After did not fire")
	}
}
