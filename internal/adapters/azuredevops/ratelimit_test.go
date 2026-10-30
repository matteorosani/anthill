package azuredevops_test

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// Scenario "Rate limit exhausted", on Azure DevOps: after a 429 with
// Retry-After, no request is sent before the reported time, and every
// call returns a *ports.RateLimitError with that reset. The fake clock
// decides the time.
func TestRateLimitExhausted(t *testing.T) {
	e := newEnv(t)
	e.srv.sequence("GET "+reposPath,
		respond(t, http.StatusTooManyRequests, "throttled.json", "Retry-After", "120", "X-RateLimit-Resource", "ATCPU"),
		respond(t, http.StatusOK, "repositories_one.json"))
	reset := serverDate.Add(2 * time.Minute)
	_, err := e.a.Repositories(ctx(t), shop)
	rl, ok := errors.AsType[*ports.RateLimitError](err)
	if !ok || !rl.Reset.Equal(reset) || !errors.Is(err, ports.ErrRateLimited) {
		t.Fatalf("err = %v, want a rate limit until %v", err, reset)
	}
	// Every call, on any endpoint, is held until the reset.
	e.clock.Advance(time.Minute)
	if _, _, err := e.a.Item(ctx(t), ado12); !errors.Is(err, ports.ErrRateLimited) {
		t.Errorf("Item() during the pause: %v", err)
	}
	if _, err := e.a.Repositories(ctx(t), shop); !errors.Is(err, ports.ErrRateLimited) {
		t.Errorf("Repositories() during the pause: %v", err)
	}
	if n := len(e.srv.sent()); n != 1 {
		t.Fatalf("sent %d requests before the reset, want 1", n)
	}
	e.clock.Set(reset)
	if _, err := e.a.Repositories(ctx(t), shop); err != nil {
		t.Errorf("after the reset: %v", err)
	}
}

// A 429 without Retry-After waits until X-RateLimit-Reset.
func TestRateLimitReset(t *testing.T) {
	e := newEnv(t)
	reset := serverDate.Add(5 * time.Minute)
	e.srv.fixture("GET "+reposPath, http.StatusTooManyRequests, "throttled.json", "X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	_, err := e.a.Repositories(ctx(t), shop)
	if rl, ok := errors.AsType[*ports.RateLimitError](err); !ok || !rl.Reset.Equal(reset) {
		t.Errorf("err = %v, want a rate limit until %v", err, reset)
	}
}

// A successful answer with Retry-After, which Azure DevOps sends when a
// user nears the limit, is used, and the next request is held.
func TestRetryAfterOnSuccess(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_one.json", "Retry-After", "30", "X-RateLimit-Delay", "10")
	if _, err := e.a.Repositories(ctx(t), shop); err != nil {
		t.Fatalf("delayed answer: %v", err)
	}
	_, _, err := e.a.Item(ctx(t), ado12)
	if rl, ok := errors.AsType[*ports.RateLimitError](err); !ok || !rl.Reset.Equal(serverDate.Add(30*time.Second)) {
		t.Errorf("next request: %v", err)
	}
	if n := len(e.srv.sent()); n != 1 {
		t.Errorf("sent %d requests, want 1", n)
	}
}
