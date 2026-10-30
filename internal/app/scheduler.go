package app

import (
	"context"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Scheduler polls each provider connection in its own loop, so that a
// failing or slow provider never holds up the others (spec
// work-item-sources, "Supported providers" and "Polling cadence").
type Scheduler struct {
	ingestion   *Ingestion
	config      *Configuration
	clock       ports.Clock
	notifier    ports.Notifier
	connections []Connection
}

// NewScheduler returns a scheduler for the given connections. The daemon
// builds one Connection per provider adapter.
func NewScheduler(ingestion *Ingestion, config *Configuration, clock ports.Clock, notifier ports.Notifier, connections ...Connection) *Scheduler {
	return &Scheduler{ingestion: ingestion, config: config, clock: clock, notifier: notifier, connections: connections}
}

// Run polls every connection until ctx is cancelled, then returns once
// every loop has ended.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, conn := range s.connections {
		wg.Go(func() { s.loop(ctx, conn) })
	}
	wg.Wait()
}

// loop polls one connection at once, then again one interval after each
// poll ends. The interval is read from the configuration before each
// wait, so a reload applies from the next poll. After a rate-limited
// poll it waits at least until the reported reset.
func (s *Scheduler) loop(ctx context.Context, conn Connection) {
	failing := false
	for {
		report, err := s.ingestion.Poll(ctx, conn)
		if ctx.Err() != nil {
			return
		}
		failed := err != nil || len(report.Failed) > 0
		if failed && !failing {
			s.notifyFailure(ctx, conn, report, err)
		}
		// A rate limit alone is a pause, not a failure: it neither raises
		// a notification nor ends a run of failures.
		if failed || report.Polled > 0 {
			failing = failed
		}

		wait := s.interval(conn.Name)
		if until := report.RateLimitedUntil; !until.IsZero() {
			wait = max(wait, until.Sub(s.clock.Now()))
		}
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(wait):
		}
	}
}

func (s *Scheduler) interval(connection string) time.Duration {
	if snap := s.config.Current(); snap != nil {
		return snap.Global.PollIntervalFor(connection)
	}
	return domain.DefaultPollInterval
}

// notifyFailure tells the queen that polling a connection started to fail
// (spec queen-console, "Notifications"). Later failures in a row raise no
// new notification.
func (s *Scheduler) notifyFailure(ctx context.Context, conn Connection, report PollReport, err error) {
	message := report.failure()
	if err != nil {
		message = err.Error()
	}
	// A notification that cannot be shown is not a reason to stop polling.
	_ = s.notifier.Notify(ctx, ports.Notification{
		Kind:  ports.NotifyPollFailed,
		Title: "Polling " + conn.Name + " failed",
		Body:  message,
	})
}
