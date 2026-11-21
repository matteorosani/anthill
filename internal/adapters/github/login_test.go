package github_test

import (
	"net/http"
	"testing"
)

// Login reads the authenticated user once and reuses it.
func TestLoginIsReadOnce(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /user", http.StatusOK, "user.json")
	for range 2 {
		login, err := e.a.Login(ctx(t))
		if err != nil || login != "MRosani" {
			t.Fatalf("Login = %q, %v", login, err)
		}
	}
	if n := e.srv.count("GET", "/user"); n != 1 {
		t.Errorf("GET /user sent %d times, want once", n)
	}
}
