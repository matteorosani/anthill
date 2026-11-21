package app_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The after-poll hook is called once after each poll of each connection,
// with that poll's report, before the wait for the next one.
func TestSchedulerAfterPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newPollEnv(t, domain.SettingValues{PollInterval: ptr(time.Minute)})
		var mu sync.Mutex
		polls := map[string]int{}
		s := app.NewScheduler(e.ingestion, e.config, e.clock, &testkit.Notifier{}, e.conns...)
		s.SetAfterPoll(func(_ context.Context, conn app.Connection, report app.PollReport) {
			mu.Lock()
			defer mu.Unlock()
			if report.Connection != conn.Name {
				t.Errorf("report of %s after a poll of %s", report.Connection, conn.Name)
			}
			polls[conn.Name]++
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			s.Run(ctx)
			close(done)
		}()
		synctest.Wait()
		e.clock.Advance(time.Minute)
		synctest.Wait()
		cancel()
		<-done
		mu.Lock()
		defer mu.Unlock()
		if len(polls) != 2 {
			t.Fatalf("hook called for %v, want both connections", polls)
		}
		for name, n := range polls {
			if n != 2 {
				t.Errorf("hook called %d times for %s, want 2", n, name)
			}
		}
	})
}
