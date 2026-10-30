package app_test

import (
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestHealthReportsUptime(t *testing.T) {
	start := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	clock := testkit.NewClock(start)
	health := app.NewHealth(clock, "1.2.3", 4242)
	clock.Advance(90 * time.Minute)
	want := app.DaemonStatus{Version: "1.2.3", PID: 4242, StartedAt: start, Uptime: 90 * time.Minute}
	if got := health.Status(); got != want {
		t.Errorf("Status() = %+v, want %+v", got, want)
	}
}
