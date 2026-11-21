package github_test

import (
	"net/http"
	"testing"
)

// Task 18.3: the adapter reports the lowest remaining rate limit of the
// resources it used, from X-RateLimit-Remaining, for the remaining rate
// limit metric.
func TestRateLimitRemaining(t *testing.T) {
	e := newEnv(t)
	if _, ok := e.a.RateLimitRemaining(); ok {
		t.Error("a remaining count before any response")
	}
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12.json", "X-RateLimit-Remaining", "4321", "X-RateLimit-Resource", "core")
	if _, _, err := e.a.Item(ctx(t), gh12); err != nil {
		t.Fatal(err)
	}
	if n, ok := e.a.RateLimitRemaining(); !ok || n != 4321 {
		t.Errorf("remaining = %d, %v; want 4321", n, ok)
	}

	// A lower count on another resource is the one reported; a later
	// response replaces its resource's count.
	e.srv.fixture("POST /graphql", http.StatusOK, "graphql_rate_limited.json", "X-RateLimit-Remaining", "12", "X-RateLimit-Resource", "graphql")
	_ = e.a.ResolveThread(ctx(t), pr34, "PRRT_kwDOA001")
	if n, _ := e.a.RateLimitRemaining(); n != 12 {
		t.Errorf("remaining = %d, want the graphql count 12", n)
	}
}
