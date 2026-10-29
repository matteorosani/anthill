package github_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// Task 7.5: write access by role (spec change-reaction, "Who can trigger
// work"; spec agent-conversation, "Commands are authorized").
func TestCanWrite(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/collaborators/alice/permission", http.StatusOK, "permission_write.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/collaborators/bob/permission", http.StatusOK, "permission_maintain.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/collaborators/carol/permission", http.StatusOK, "permission_triage.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/collaborators/mallory/permission", http.StatusNotFound, "not_found.json")

	for login, want := range map[string]bool{
		"alice":   true,  // collaborator with write
		"bob":     true,  // maintain
		"carol":   false, // triage can label but has no write access
		"mallory": false, // not a collaborator
	} {
		got, err := e.a.CanWrite(ctx(t), repo, login)
		if err != nil {
			t.Fatalf("%s: %v", login, err)
		}
		if got != want {
			t.Errorf("CanWrite(%s) = %v, want %v", login, got, want)
		}
	}
}

// Task 7.5: answers, negative ones included, come from the cache for ten
// minutes on the clock, whatever the case of the repository and login.
func TestCanWriteCache(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET /repos/matteorosani/anthill/collaborators/alice/permission", http.StatusOK, "permission_write.json")
	e.srv.fixture("GET /repos/matteorosani/anthill/collaborators/mallory/permission", http.StatusNotFound, "not_found.json")
	mixed := domain.Repository{Provider: domain.GitHub, Org: "MatteoRosani", Name: "Anthill"}
	alice := "/repos/matteorosani/anthill/collaborators/alice/permission"
	mallory := "/repos/matteorosani/anthill/collaborators/mallory/permission"

	for _, call := range []struct {
		repo  domain.Repository
		login string
	}{{repo, "alice"}, {mixed, "Alice"}, {repo, "mallory"}, {repo, "mallory"}} {
		if _, err := e.a.CanWrite(ctx(t), call.repo, call.login); err != nil {
			t.Fatal(err)
		}
	}
	if a, m := e.srv.count("GET", alice), e.srv.count("GET", mallory); a != 1 || m != 1 {
		t.Errorf("requests for alice, mallory = %d, %d; want 1, 1 (cache hits)", a, m)
	}

	e.clock.Advance(9 * time.Minute)
	if ok, _ := e.a.CanWrite(ctx(t), repo, "alice"); !ok || e.srv.count("GET", alice) != 1 {
		t.Error("cache missed before ten minutes")
	}
	e.clock.Advance(time.Minute)
	if ok, _ := e.a.CanWrite(ctx(t), repo, "alice"); !ok || e.srv.count("GET", alice) != 2 {
		t.Error("cache hit after ten minutes")
	}
}

// Task 7.5: errors are not cached.
func TestCanWriteErrorIsNotCached(t *testing.T) {
	e := newEnv(t)
	calls := 0
	e.srv.handle("GET /repos/matteorosani/anthill/collaborators/alice/permission", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			respond(t, http.StatusInternalServerError, "server_error.json")(w, r)
			return
		}
		respond(t, http.StatusOK, "permission_write.json")(w, r)
	})
	if _, err := e.a.CanWrite(ctx(t), repo, "alice"); err == nil {
		t.Fatal("no error for a 500")
	}
	if ok, err := e.a.CanWrite(ctx(t), repo, "alice"); err != nil || !ok {
		t.Errorf("after the error: %v, %v", ok, err)
	}
}

func TestRepositoriesIsTheRepositoryItself(t *testing.T) {
	e := newEnv(t)
	got, err := e.a.Repositories(ctx(t), domain.Repository{Provider: domain.GitHub, Org: "MatteoRosani", Name: "Anthill"})
	if err != nil || len(got) != 1 || got[0] != repo {
		t.Errorf("Repositories() = %v, %v; want [%v]", got, err, repo)
	}
	if len(e.srv.sent()) != 0 {
		t.Errorf("sent %d requests, want none", len(e.srv.sent()))
	}
}
