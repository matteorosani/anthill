package testkit_test

import (
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/testkit"
)

func TestClock(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := testkit.NewClock(t0)
	if !c.Now().Equal(t0) {
		t.Fatalf("Now() = %v", c.Now())
	}
	select {
	case <-c.After(0):
	default:
		t.Error("After(0) did not fire at once")
	}
	late := c.After(10 * time.Minute)
	early := c.After(5 * time.Minute)
	if c.Waiters() != 2 {
		t.Errorf("Waiters() = %d", c.Waiters())
	}
	c.Advance(4 * time.Minute)
	select {
	case <-early:
		t.Error("fired before its deadline")
	default:
	}
	c.Advance(time.Minute)
	if got := <-early; !got.Equal(t0.Add(5 * time.Minute)) {
		t.Errorf("early fired with %v", got)
	}
	c.Set(t0.Add(time.Hour))
	if got := <-late; !got.Equal(t0.Add(10 * time.Minute)) {
		t.Errorf("late fired with %v", got)
	}
	if c.Waiters() != 0 || !c.Now().Equal(t0.Add(time.Hour)) {
		t.Errorf("Waiters() = %d, Now() = %v", c.Waiters(), c.Now())
	}
	defer func() {
		if recover() == nil {
			t.Error("Set into the past did not panic")
		}
	}()
	c.Set(t0)
}

func TestClockConcurrentUse(t *testing.T) {
	c := testkit.NewClock(time.Unix(0, 0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			<-c.After(time.Second)
		}
	}()
	for c.Now().Before(time.Unix(1000, 0)) {
		c.Advance(time.Second)
	}
	// Whatever After calls are still waiting fire on the next advances.
	for {
		select {
		case <-done:
			return
		default:
			c.Advance(time.Second)
		}
	}
}
