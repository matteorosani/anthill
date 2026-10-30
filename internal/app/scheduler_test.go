package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The scheduler tests run inside a synctest bubble: synctest.Wait returns
// once every goroutine of the test is blocked, here on the fake clock, so
// a test advances the clock and checks the result without sleeping. The
// bubble also fails the test if a goroutine is left running at its end.

// startScheduler runs a scheduler over the env's connections until the
// returned stop function is called; stop waits for Run to return.
func startScheduler(e *pollEnv, notifier ports.Notifier) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s := app.NewScheduler(e.ingestion, e.config, e.clock, notifier, e.conns...)
	go func() {
		s.Run(ctx)
		close(done)
	}()
	synctest.Wait()
	return func() {
		cancel()
		<-done
	}
}

// Scenario "One provider unavailable": Azure DevOps returns errors in a
// poll cycle while GitHub responds. GitHub items are still ingested in
// that cycle, and the Azure DevOps failure is recorded with the time of
// its last successful poll, for the queen console, and raised once.
func TestSchedulerOneProviderUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newPollEnv(t, domain.SettingValues{})
		notifier := &testkit.Notifier{}
		e.gh.PutItem(optedIn(ghRef(12), "Fix login timeout"))
		e.ado.PutItem(optedIn(adoRef(7), "Fix checkout"))
		stop := startScheduler(e, notifier)
		defer stop()
		if got := eventKinds(e.pending()); len(got) != 2 {
			t.Fatalf("first cycle events = %v", got)
		}

		// Next cycle: Azure DevOps fails, GitHub has a new comment.
		e.ado.Fail(testkit.OpItems, errors.New("503 Service Unavailable"))
		e.ado.Fail(testkit.OpPullRequests, errors.New("503 Service Unavailable"))
		e.gh.Comment(ghRef(12), "dev", "Also on Safari.")
		e.clock.Advance(domain.DefaultPollInterval)
		synctest.Wait()

		events := e.pending()
		if last := events[len(events)-1]; len(events) != 3 || last.Kind != domain.ChangeCommentAdded || last.Ref != ghRef(12) {
			t.Errorf("events = %+v, want the GitHub comment ingested", events)
		}
		statuses, _ := e.store.PollStatuses(context.Background())
		failedAt := t0.Add(domain.DefaultPollInterval)
		want := []ports.PollStatus{
			{Connection: "azure-devops/contoso", Cursor: statuses[0].Cursor, LastSuccess: t0, LastErrorAt: failedAt,
				LastError: "items of contoso/shop: 503 Service Unavailable; pull requests of contoso/shop/web: 503 Service Unavailable"},
			{Connection: "github", Cursor: statuses[1].Cursor, LastSuccess: failedAt},
		}
		if !reflect.DeepEqual(statuses, want) {
			t.Errorf("statuses =\n%+v\nwant\n%+v", statuses, want)
		}
		sent := notifier.Sent()
		if len(sent) != 1 || sent[0].Kind != ports.NotifyPollFailed || sent[0].Title != "Polling azure-devops/contoso failed" {
			t.Fatalf("notifications = %+v", sent)
		}

		// A failure in a row raises nothing new; a recovery clears the
		// error, and a new failure is raised again.
		e.clock.Advance(domain.DefaultPollInterval)
		synctest.Wait()
		if n := len(notifier.Sent()); n != 1 {
			t.Errorf("%d notifications after a second failure, want 1", n)
		}
		e.ado.Fail(testkit.OpItems, nil)
		e.ado.Fail(testkit.OpPullRequests, nil)
		e.clock.Advance(domain.DefaultPollInterval)
		synctest.Wait()
		if st := e.status("azure-devops/contoso"); st.LastError != "" || !st.LastSuccess.Equal(e.clock.Now()) {
			t.Errorf("status after recovery = %+v", st)
		}
		e.ado.FailNext(testkit.OpItems, errors.New("502 Bad Gateway"))
		e.clock.Advance(domain.DefaultPollInterval)
		synctest.Wait()
		if n := len(notifier.Sent()); n != 2 {
			t.Errorf("%d notifications after a new failure, want 2", n)
		}
	})
}

// Scenario "Rate limit exhausted": GitHub's limit is exhausted until
// 10:42. No GitHub request is sent before 10:42, the poll state shows the
// pause, no failure is raised, and polling goes on at 10:42.
func TestSchedulerRateLimitExhausted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newPollEnv(t, domain.SettingValues{})
		notifier := &testkit.Notifier{}
		reset := time.Date(2026, 10, 1, 10, 42, 0, 0, time.UTC)
		e.gh.RateLimit(reset)
		stop := startScheduler(e, notifier)
		defer stop()

		if st := e.status("github"); !st.RateLimitedUntil.Equal(reset) {
			t.Errorf("status = %+v, want paused until 10:42", st)
		}
		for e.clock.Now().Before(reset.Add(-time.Minute)) {
			e.clock.Advance(time.Minute)
			synctest.Wait()
		}
		if n := len(e.gh.Requests()); n != 1 {
			t.Errorf("%d GitHub requests before 10:42, want only the first", n)
		}
		if n := len(e.ado.Requests()); n < 6 {
			t.Errorf("%d Azure DevOps requests, want polling to go on", n)
		}
		e.clock.Set(reset)
		synctest.Wait()
		requests := e.gh.Requests()
		if len(requests) != 3 || !requests[1].At.Equal(reset) {
			t.Errorf("requests = %+v, want a poll at 10:42", requests)
		}
		if st := e.status("github"); !st.RateLimitedUntil.IsZero() || !st.LastSuccess.Equal(reset) {
			t.Errorf("status after the reset = %+v", st)
		}
		if n := len(notifier.Sent()); n != 0 {
			t.Errorf("notifications = %+v, want none for a rate limit", notifier.Sent())
		}
	})
}

// Each connection waits its own interval: provider-poll-intervals for
// Azure DevOps, poll-interval for GitHub.
func TestSchedulerIntervalPerConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newPollEnv(t, domain.SettingValues{
			PollInterval:          ptr(time.Minute),
			ProviderPollIntervals: map[string]time.Duration{"azure-devops/contoso": 5 * time.Minute},
		})
		stop := startScheduler(e, &testkit.Notifier{})
		defer stop()
		for range 5 {
			e.clock.Advance(time.Minute)
			synctest.Wait()
		}
		// Each poll reads items and PRs: two requests.
		if gh, ado := len(e.gh.Requests()), len(e.ado.Requests()); gh != 12 || ado != 4 {
			t.Errorf("requests: GitHub %d, Azure DevOps %d; want 12 and 4", gh, ado)
		}
	})
}

// Cancelling the context ends every loop, also one waiting for its next
// poll, and Run returns.
func TestSchedulerStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newPollEnv(t, domain.SettingValues{})
		stop := startScheduler(e, &testkit.Notifier{})
		if n := e.clock.Waiters(); n != 2 {
			t.Errorf("%d loops waiting, want 2", n)
		}
		stop()
		before := len(e.gh.Requests())
		e.clock.Advance(time.Hour)
		synctest.Wait()
		if n := len(e.gh.Requests()); n != before {
			t.Errorf("%d requests after stop, want %d", n, before)
		}
	})
}
