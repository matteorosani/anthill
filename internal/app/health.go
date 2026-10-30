package app

import (
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// DaemonStatus is the daemon's health as the status command shows it
// (spec queen-console, "Health and status"). For now it holds what the
// daemon knows about itself; the providers, ants, claims and shepherd
// rounds join it with the services that own them.
type DaemonStatus struct {
	Version   string
	PID       int
	StartedAt time.Time
	Uptime    time.Duration
}

// Health reports the daemon's status.
type Health struct {
	clock   ports.Clock
	version string
	pid     int
	started time.Time
}

// NewHealth returns the health of a daemon with the given version and
// process id that starts now, by clock.
func NewHealth(clock ports.Clock, version string, pid int) *Health {
	return &Health{clock: clock, version: version, pid: pid, started: clock.Now()}
}

// Status returns the daemon's status, with its uptime by the clock.
func (h *Health) Status() DaemonStatus {
	return DaemonStatus{
		Version:   h.version,
		PID:       h.pid,
		StartedAt: h.started,
		Uptime:    h.clock.Now().Sub(h.started),
	}
}
