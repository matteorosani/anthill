package github_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/github"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// Task 7.1: requests carry the token and the user agent, and the token
// is read from the credential source once.
func TestRequestsCarryTheToken(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusOK, "issue12.json")
	e.srv.graphql("resolveReviewThread", "graphql_resolve.json")

	if _, _, err := e.a.Item(ctx(t), gh12); err != nil {
		t.Fatal(err)
	}
	if err := e.a.ResolveThread(ctx(t), pr34, "PRRT_kwDOA001"); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.srv.sent() {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("%s %s: Authorization = %q", r.Method, r.Path, got)
		}
		if got := r.Header.Get("User-Agent"); got != "anthill/1.2.3" {
			t.Errorf("%s %s: User-Agent = %q", r.Method, r.Path, got)
		}
	}
	want := []domain.CredentialKey{domain.GitHubCredential("github.com")}
	if len(e.creds.Lookups) != 1 || e.creds.Lookups[0] != want[0] {
		t.Errorf("credential lookups = %v, want %v", e.creds.Lookups, want)
	}
	e.noToken(t, nil)
}

// Spec anthill-configuration, "Token never logged": a request that fails
// with an authentication error gives an error and logs without the
// token, and the next request reads the token again.
func TestAuthenticationErrorHasNoToken(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/issues/12", http.StatusUnauthorized, "bad_credentials.json")

	_, _, err := e.a.Item(ctx(t), gh12)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v, want the 401 and GitHub's message", err)
	}
	e.noToken(t, err)
	if !strings.Contains(e.logs.String(), "status=401") {
		t.Errorf("logs do not show the request:\n%s", e.logs)
	}

	_, _, err = e.a.Item(ctx(t), gh12)
	e.noToken(t, err)
	if len(e.creds.Lookups) != 2 {
		t.Errorf("credential lookups = %d, want 2: a 401 drops the token", len(e.creds.Lookups))
	}
}

// A token that came back in an error message from GitHub is redacted.
func TestGitHubMessageIsRedacted(t *testing.T) {
	e := newEnv(t)
	e.srv.handle("GET /repos/matteorosani/anthill/issues/12", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"message":"token `+token+` is malformed"}`)
	})
	_, _, err := e.a.Item(ctx(t), gh12)
	if err == nil {
		t.Fatal("no error")
	}
	e.noToken(t, err)
}

// GraphQL errors carry no token either.
func TestGraphQLErrorsHaveNoToken(t *testing.T) {
	e := newEnv(t)
	calls := 0
	e.srv.handle("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			respond(t, http.StatusUnauthorized, "graphql_bad_credentials.json")(w, r)
			return
		}
		respond(t, http.StatusOK, "graphql_not_found.json")(w, r)
	})

	err := e.a.ResolveThread(ctx(t), pr34, "PRRT_kwDOA001")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want a 401", err)
	}
	e.noToken(t, err)

	err = e.a.ResolveThread(ctx(t), pr34, "PRRT_missing")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	e.noToken(t, err)
}

// Without a token nothing is sent.
func TestMissingCredential(t *testing.T) {
	e := newEnv(t)
	e.creds.Values = nil
	_, _, err := e.a.Item(ctx(t), gh12)
	if !errors.Is(err, ports.ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if n := len(e.srv.sent()); n != 0 {
		t.Errorf("%d requests sent without a token", n)
	}
}

// roundTripFunc lets a function act as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A GitHub Enterprise host gets its own API URLs and its own credential.
func TestEnterpriseHost(t *testing.T) {
	creds := &testkit.CredentialStore{}
	creds.Set(domain.GitHubCredential("ghe.example.com"), token)
	var urls []string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		body := `{"data":{"resolveReviewThread":{"thread":{"id":"x","isResolved":true}}}}`
		if !strings.HasSuffix(r.URL.Path, "/graphql") {
			body = `{"number":12,"state":"open"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})
	a, err := github.New(github.Options{
		Host:        "ghe.example.com",
		Credentials: creds,
		Clock:       testkit.NewClock(serverDate),
		Transport:   transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Item(ctx(t), gh12); err != nil {
		t.Fatal(err)
	}
	if err := a.ResolveThread(ctx(t), pr34, "x"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://ghe.example.com/api/v3/repos/matteorosani/anthill/issues/12",
		"https://ghe.example.com/api/graphql",
	}
	if len(urls) != 2 || urls[0] != want[0] || urls[1] != want[1] {
		t.Errorf("urls = %v, want %v", urls, want)
	}
	if last := creds.Lookups[len(creds.Lookups)-1]; last != domain.GitHubCredential("ghe.example.com") {
		t.Errorf("credential = %v", last)
	}
}
