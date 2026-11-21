package azuredevops_test

import (
	"net/http"
	"testing"
)

// Task 18.3: Azure DevOps sends X-RateLimit-Remaining only near the
// limit; the adapter reports it once a response has.
func TestRateLimitRemaining(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_one.json")
	if _, err := e.a.Repositories(ctx(t), shop); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.a.RateLimitRemaining(); ok {
		t.Error("a remaining count from a response without the header")
	}
	// The answer need not succeed: any response that sends the header
	// counts.
	e.srv.fixture("GET "+itemPath, http.StatusNotFound, "not_found.json", "X-RateLimit-Remaining", "37")
	_, _, _ = e.a.Item(ctx(t), ado12)
	if n, ok := e.a.RateLimitRemaining(); !ok || n != 37 {
		t.Errorf("remaining = %d, %v; want 37", n, ok)
	}
}
