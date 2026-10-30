package azuredevops_test

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/matteorosani/anthill/internal/adapters/azuredevops"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The fixtures under testdata are hand-built from the response shapes in
// the Azure DevOps REST API 7.1 documentation, kept to the fields the
// adapter reads. The organization, project and repository are spelled
// Contoso, Shop and Web in them, and logins in mixed case, so the tests
// also check that the adapter lowercases names.

// pat is the fake personal access token; no log line or error may
// contain it, raw or Basic-encoded. It matches none of the redactor's
// patterns, so the tests show that the adapter removes the token it
// uses, not only token-shaped text.
const pat = "s3cr3t-pat-for-tests"

// basicPAT is the Authorization header value of pat.
var basicPAT = "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+pat))

// serverDate is the Date header every answer carries.
var serverDate = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

var (
	shop  = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop"}
	web   = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "web"}
	ado12 = domain.Ref{Repo: shop, Kind: domain.KindItem, Number: 12}
	pr34  = domain.Ref{Repo: web, Kind: domain.KindPR, Number: 34}
)

// Paths of the fake Azure DevOps.
const (
	itemPath     = "/contoso/shop/_apis/wit/workitems/12"
	commentsPath = "/contoso/shop/_apis/wit/workItems/12/comments"
	batchPath    = "/contoso/_apis/wit/workitemsbatch"
	wiqlPath     = "/contoso/shop/_apis/wit/wiql"
	prPath       = "/contoso/shop/_apis/git/repositories/web/pullrequests/34"
	prsPath      = "/contoso/shop/_apis/git/repositories/web/pullrequests"
	reposPath    = "/contoso/shop/_apis/git/repositories"
)

// request is one request the fake Azure DevOps received.
type request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// server is a fake Azure DevOps: an httptest.Server whose routes the test
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
// "GET /contoso/shop/_apis/wit/workitems/12".
func (s *server) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h)
}

// fixture registers a route that answers status with a testdata file and
// the given header pairs.
func (s *server) fixture(pattern string, status int, file string, headers ...string) {
	s.handle(pattern, respond(s.t, status, file, headers...))
}

// sequence registers a route whose successive requests get the given
// handlers; the last one answers every later request.
func (s *server) sequence(pattern string, handlers ...http.HandlerFunc) {
	var mu sync.Mutex
	n := 0
	s.handle(pattern, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		h := handlers[min(n, len(handlers)-1)]
		n++
		mu.Unlock()
		h(w, r)
	})
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

// sent returns a copy of the requests received so far.
func (s *server) sent() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request(nil), s.requests...)
}

// matching returns the requests whose method and path match.
func (s *server) matching(method, path string) []request {
	var out []request
	for _, r := range s.sent() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// count is the number of requests whose method and path match.
func (s *server) count(method, path string) int {
	return len(s.matching(method, path))
}

// env holds what a test needs around the adapter.
type env struct {
	srv   *server
	clock *testkit.Clock
	creds *testkit.CredentialStore
	logs  *bytes.Buffer
	a     *azuredevops.Adapter
}

// newEnv returns an adapter for the organization Contoso talking to a
// fresh fake Azure DevOps, with the PAT stored and every request logged
// at debug level.
func newEnv(t *testing.T, opts ...func(*azuredevops.Options)) *env {
	t.Helper()
	e := &env{
		srv:   newServer(t),
		clock: testkit.NewClock(serverDate),
		creds: &testkit.CredentialStore{},
		logs:  &bytes.Buffer{},
	}
	e.creds.Set(domain.AzureDevOpsCredential("contoso"), pat)
	o := azuredevops.Options{
		Organization: "Contoso",
		Credentials:  e.creds,
		Clock:        e.clock,
		Version:      "1.2.3",
		Logger:       slog.New(slog.NewTextHandler(&lockedWriter{w: e.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		BaseURL:      e.srv.srv.URL + "/",
		IdentityURL:  e.srv.srv.URL + "/vssps/",
	}
	for _, f := range opts {
		f(&o)
	}
	a, err := azuredevops.New(o)
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

// noToken fails the test if the logs or err contain the token, raw or
// Basic-encoded.
func (e *env) noToken(t *testing.T, err error, token string) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(":" + token))
	for _, secret := range []string{token, encoded} {
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("error contains the token: %v", err)
		}
		if strings.Contains(e.logs.String(), secret) {
			t.Errorf("logs contain the token:\n%s", e.logs)
		}
	}
}

func ctx(t *testing.T) context.Context {
	return t.Context()
}

// comment is an Anthill comment of the given kind on ado-12, with marker
// ID id (none when empty).
func comment(kind domain.CommentKind, id string) domain.AnthillComment {
	m := domain.Marker{Version: domain.MarkerVersion, ID: id, Instance: "mrosani@LAPTOP-42", Kind: kind, Ref: "ado-12"}
	if kind == domain.CommentClaim {
		m.ClaimFields = &domain.ClaimFields{Claim: domain.ClaimTask, LeaseMinutes: 15}
	}
	return domain.AnthillComment{Role: domain.RoleAnt, Body: "hello", Marker: m}
}

// itemRoutes registers what reading work item 12 needs beyond the item
// itself: the project of its links, the states of a Bug and its comments.
func itemRoutes(s *server) {
	s.fixture("POST "+batchPath, http.StatusOK, "link_projects.json")
	s.fixture("GET /contoso/shop/_apis/wit/workitemtypes/Bug/states", http.StatusOK, "states_bug.json")
	s.fixture("GET "+commentsPath, http.StatusOK, "comments12.json")
}
