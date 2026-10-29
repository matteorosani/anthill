package github_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/github"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The fixtures under testdata are hand-built from the response shapes in
// GitHub's REST and GraphQL documentation, kept to the fields the adapter
// reads. The repository is spelled MatteoRosani/Anthill in them, so the
// tests also check that the adapter lowercases names.

// token is the fake token; no log line or error may contain it. It
// matches none of the redactor's patterns, so the tests show that the
// adapter removes the token it uses, not only token-shaped text.
const token = "s3cr3t-token-for-tests"

// serverDate is the Date header every answer carries unless a handler
// sets another.
var serverDate = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

var (
	repo  = domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "anthill"}
	gh12  = domain.Ref{Repo: repo, Kind: domain.KindItem, Number: 12}
	pr34  = domain.Ref{Repo: repo, Kind: domain.KindPR, Number: 34}
	other = domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "other"}
)

// request is one request the fake GitHub received.
type request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// server is a fake GitHub API: an httptest.Server whose routes the test
// registers. A request to a route nobody registered fails the test.
type server struct {
	t   *testing.T
	srv *httptest.Server
	mux *http.ServeMux

	mu       sync.Mutex
	requests []request
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{t: t, mux: http.NewServeMux()}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Header: r.Header.Clone(), Body: string(body),
	})
	s.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if _, pattern := s.mux.Handler(r); pattern == "" {
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Date", serverDate.Format(http.TimeFormat))
	s.mux.ServeHTTP(w, r)
}

// handle registers a handler for a pattern such as
// "GET /repos/matteorosani/anthill/issues".
func (s *server) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h)
}

// fixture registers a route that answers status with a testdata file and
// the given header pairs.
func (s *server) fixture(pattern string, status int, file string, headers ...string) {
	s.handle(pattern, respond(s.t, status, file, headers...))
}

// respond answers with a testdata file.
func respond(t *testing.T, status int, file string, headers ...string) http.HandlerFunc {
	data, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}
}

// graphql registers the GraphQL endpoint. routes are pairs of a key and
// a testdata file: each request is answered with the file of the first
// key its body contains.
func (s *server) graphql(routes ...string) {
	s.handle("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for i := 0; i+1 < len(routes); i += 2 {
			if strings.Contains(string(body), routes[i]) {
				respond(s.t, http.StatusOK, routes[i+1])(w, r)
				return
			}
		}
		s.t.Errorf("unexpected GraphQL request %s", body)
		w.WriteHeader(http.StatusBadRequest)
	})
}

// sent returns a copy of the requests received so far.
func (s *server) sent() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request(nil), s.requests...)
}

// count is the number of requests whose method and path match.
func (s *server) count(method, path string) int {
	n := 0
	for _, r := range s.sent() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// env holds what a test needs around the adapter.
type env struct {
	srv   *server
	clock *testkit.Clock
	creds *testkit.CredentialStore
	logs  *bytes.Buffer
	a     *github.Adapter
}

// newEnv returns an adapter talking to a fresh fake GitHub, with the
// token stored for github.com and every request logged at debug level.
func newEnv(t *testing.T, opts ...func(*github.Options)) *env {
	t.Helper()
	e := &env{
		srv:   newServer(t),
		clock: testkit.NewClock(serverDate),
		creds: &testkit.CredentialStore{},
		logs:  &bytes.Buffer{},
	}
	e.creds.Set(domain.GitHubCredential(github.DefaultHost), token)
	o := github.Options{
		Credentials: e.creds,
		Clock:       e.clock,
		Version:     "1.2.3",
		Logger:      slog.New(slog.NewTextHandler(&lockedWriter{w: e.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		BaseURL:     e.srv.srv.URL + "/",
	}
	for _, f := range opts {
		f(&o)
	}
	a, err := github.New(o)
	if err != nil {
		t.Fatal(err)
	}
	e.a = a
	return e
}

// lockedWriter makes a buffer safe for the concurrent writes of a
// logger.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// noToken fails the test if the logs or err contain the token.
func (e *env) noToken(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), token) {
		t.Errorf("error contains the token: %v", err)
	}
	if strings.Contains(e.logs.String(), token) {
		t.Errorf("logs contain the token:\n%s", e.logs)
	}
}

func ctx(t *testing.T) context.Context {
	return t.Context()
}

// comment is an Anthill comment of the given kind on gh-12, with marker
// ID id (none when empty).
func comment(kind domain.CommentKind, id string) domain.AnthillComment {
	m := domain.Marker{Version: domain.MarkerVersion, ID: id, Instance: "mrosani@LAPTOP-42", Kind: kind, Ref: "gh-12"}
	if kind == domain.CommentClaim {
		m.ClaimFields = &domain.ClaimFields{Claim: domain.ClaimTask, LeaseMinutes: 15}
	}
	return domain.AnthillComment{Role: domain.RoleAnt, Body: "hello", Marker: m}
}
