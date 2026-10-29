package github_test

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// at1042 is when the rate limit of the tests resets.
var at1042 = time.Date(2026, 10, 29, 10, 42, 0, 0, time.UTC)

func resetHeader(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// rateLimit returns the reset time of a rate-limit error, or fails.
func rateLimit(t *testing.T, err error) time.Time {
	t.Helper()
	if !errors.Is(err, ports.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var rl *ports.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want a *ports.RateLimitError", err)
	}
	return rl.Reset
}

// Task 7.6 and spec work-item-sources, "Rate limit exhausted": GitHub
// answers that the limit is exhausted until 10:42, and no request is
// sent before 10:42.
func TestRateLimitExhausted(t *testing.T) {
	e := newEnv(t)
	issues := "/repos/matteorosani/anthill/issues"
	limited := true
	e.srv.handle("GET "+issues, func(w http.ResponseWriter, r *http.Request) {
		if limited {
			respond(t, http.StatusForbidden, "rate_limited.json",
				"X-RateLimit-Limit", "5000", "X-RateLimit-Remaining", "0",
				"X-RateLimit-Reset", resetHeader(at1042), "X-RateLimit-Resource", "core")(w, r)
			return
		}
		respond(t, http.StatusOK, "issues_since.json", "X-RateLimit-Remaining", "4999")(w, r)
	})
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12.json")

	_, err := e.a.Items(ctx(t), repo, "")
	if reset := rateLimit(t, err); !reset.Equal(at1042) {
		t.Fatalf("reset = %v, want 10:42", reset)
	}
	e.noToken(t, err)

	for _, now := range []time.Time{serverDate, at1042.Add(-time.Second)} {
		e.clock.Set(now)
		_, err := e.a.Items(ctx(t), repo, "")
		if reset := rateLimit(t, err); !reset.Equal(at1042) {
			t.Errorf("at %v: reset = %v", now, reset)
		}
		// Other calls on the same limit are held too.
		_, _, err = e.a.Item(ctx(t), gh12)
		rateLimit(t, err)
	}
	if n := len(e.srv.sent()); n != 1 {
		t.Fatalf("%d requests sent before 10:42, want only the first", n)
	}

	limited = false
	e.clock.Set(at1042)
	if _, err := e.a.Items(ctx(t), repo, ""); err != nil {
		t.Fatalf("at 10:42: %v", err)
	}
	if n := e.srv.count("GET", issues); n != 2 {
		t.Errorf("issue list requests = %d, want 2", n)
	}
}

// Task 7.6: a 2xx answer that used the last request of the window holds
// the next request until the reset.
func TestRateLimitLastRequest(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12.json",
		"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetHeader(at1042))
	if _, _, err := e.a.Item(ctx(t), gh12); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.a.Item(ctx(t), gh12)
	if reset := rateLimit(t, err); !reset.Equal(at1042) {
		t.Errorf("reset = %v", reset)
	}
	if n := len(e.srv.sent()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// Task 7.6: secondary limits pause for Retry-After, or one minute when
// GitHub gives none.
func TestSecondaryRateLimits(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		file    string
		headers []string
		want    time.Duration
	}{
		{"429 with Retry-After", http.StatusTooManyRequests, "secondary_rate_limited.json", []string{"Retry-After", "120"}, 2 * time.Minute},
		{"403 with Retry-After", http.StatusForbidden, "secondary_rate_limited.json", []string{"Retry-After", "30"}, 30 * time.Second},
		{"403 without Retry-After", http.StatusForbidden, "secondary_rate_limited.json", nil, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", tt.status, tt.file, tt.headers...)
			_, _, err := e.a.Item(ctx(t), gh12)
			want := serverDate.Add(tt.want)
			if reset := rateLimit(t, err); !reset.Equal(want) {
				t.Fatalf("reset = %v, want %v", reset, want)
			}
			e.clock.Set(want.Add(-time.Second))
			_, _, err = e.a.Item(ctx(t), gh12)
			rateLimit(t, err)
			if n := len(e.srv.sent()); n != 1 {
				t.Errorf("%d requests sent during the pause", n)
			}
			e.clock.Set(want)
			_, _, _ = e.a.Item(ctx(t), gh12)
			if n := len(e.srv.sent()); n != 2 {
				t.Errorf("%d requests after the pause, want 2", n)
			}
		})
	}
}

// A 403 that is not about a rate limit pauses nothing.
func TestForbiddenIsNoRateLimit(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusForbidden, "forbidden.json", "X-RateLimit-Remaining", "4000")
	for range 2 {
		_, _, err := e.a.Item(ctx(t), gh12)
		if err == nil || errors.Is(err, ports.ErrRateLimited) {
			t.Fatalf("err = %v, want a plain 403", err)
		}
	}
	if n := len(e.srv.sent()); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

// Task 7.6: GraphQL has its own limit. It answers 200 with a
// RATE_LIMITED error, which pauses GraphQL but not REST.
func TestGraphQLRateLimit(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("POST /graphql", http.StatusOK, "graphql_rate_limited.json",
		"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", resetHeader(at1042), "X-RateLimit-Resource", "graphql")
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12.json")

	err := e.a.ResolveThread(ctx(t), pr34, "PRRT_kwDOA001")
	if reset := rateLimit(t, err); !reset.Equal(at1042) {
		t.Fatalf("reset = %v", reset)
	}
	err = e.a.ResolveThread(ctx(t), pr34, "PRRT_kwDOA001")
	rateLimit(t, err)
	if n := e.srv.count("POST", "/graphql"); n != 1 {
		t.Errorf("GraphQL requests = %d, want 1", n)
	}
	if _, _, err := e.a.Item(ctx(t), gh12); err != nil {
		t.Errorf("REST is held by the GraphQL limit: %v", err)
	}
}
