package azuredevops_test

import (
	"net/http"
	"testing"
)

// Login reads the authenticated user's unique name once, lowercased.
func TestLoginIsReadOnce(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /contoso/_apis/connectionData", http.StatusOK, "connection_data.json")
	for range 2 {
		login, err := e.a.Login(ctx(t))
		if err != nil || login != "m.rosani@contoso.com" {
			t.Fatalf("Login = %q, %v", login, err)
		}
	}
	if n := e.srv.count("GET", "/contoso/_apis/connectionData"); n != 1 {
		t.Errorf("connectionData read %d times, want once", n)
	}
}
