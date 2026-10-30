package azuredevops_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

const (
	identitiesPath = "/vssps/contoso/_apis/identities"
	aclPath        = "/contoso/_apis/accesscontrollists/2e9eb7ed-3c0a-47d4-87c1-0ffdd275fd87"
	projectPath    = "/contoso/_apis/projects/shop"
)

// accessRoutes registers the project, its repositories and the identity
// of m.rosani, with the ACL answer acl.
func accessRoutes(s *server, acl string) {
	s.fixture("GET "+projectPath, http.StatusOK, "project_shop.json")
	s.fixture("GET "+reposPath, http.StatusOK, "repositories_two.json")
	s.fixture("GET "+identitiesPath, http.StatusOK, "identities_mrosani.json")
	s.fixture("GET "+aclPath, http.StatusOK, acl)
}

// accessRoutesWith registers the routes of a contributor whose identity
// lookup answers identities.
func accessRoutesWith(s *server, identities string) {
	s.fixture("GET "+projectPath, http.StatusOK, "project_shop.json")
	s.fixture("GET "+reposPath, http.StatusOK, "repositories_two.json")
	s.fixture("GET "+identitiesPath, http.StatusOK, identities)
	s.fixture("GET "+aclPath, http.StatusOK, "acl_contributor.json")
}

// Write access is the effective Contribute permission on the repository,
// or on all the project's repositories for work items.
func TestCanWrite(t *testing.T) {
	tests := []struct {
		name string
		acl  string
		repo domain.Repository
		want bool
	}{
		{"contributor on the repository", "acl_contributor.json", web, true},
		{"contributor on the project", "acl_contributor.json", shop, true},
		{"reader", "acl_reader.json", web, false},
		{"contribute denied", "acl_denied.json", web, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			accessRoutes(e.srv, tt.acl)
			got, err := e.a.CanWrite(ctx(t), tt.repo, "M.Rosani@contoso.com")
			if err != nil || got != tt.want {
				t.Fatalf("CanWrite() = %v, %v; want %v", got, err, tt.want)
			}
			acl := e.srv.matching(http.MethodGet, aclPath)[0].Query
			wantToken := "token=repoV2%2Fp-shop%2Fr-web"
			if tt.repo.Name == "" {
				wantToken = "token=repoV2%2Fp-shop&"
			}
			if !strings.Contains(acl+"&", wantToken) || !strings.Contains(acl, "includeExtendedInfo=true") {
				t.Errorf("ACL query = %q, want %s", acl, wantToken)
			}
		})
	}
}

// A user Azure DevOps does not know, or a repository the project does
// not have, has no write access.
func TestCanWriteUnknown(t *testing.T) {
	e := newEnv(t)
	accessRoutesWith(e.srv, "identities_none.json")
	if ok, err := e.a.CanWrite(ctx(t), web, "mallory@example.com"); err != nil || ok {
		t.Errorf("unknown user: %v, %v", ok, err)
	}
	mobile := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "mobile"}
	if ok, err := e.a.CanWrite(ctx(t), mobile, "m.rosani@contoso.com"); err != nil || ok {
		t.Errorf("unknown repository: %v, %v", ok, err)
	}
}

// Answers are reused for ten minutes, negative ones included.
func TestCanWriteCache(t *testing.T) {
	e := newEnv(t)
	accessRoutes(e.srv, "acl_reader.json")
	for range 2 {
		if ok, _ := e.a.CanWrite(ctx(t), web, "m.rosani@contoso.com"); ok {
			t.Fatal("reader has write access")
		}
	}
	if n := e.srv.count(http.MethodGet, aclPath); n != 1 {
		t.Errorf("read the ACL %d times, want 1 (cache hit)", n)
	}
	e.clock.Advance(10 * time.Minute)
	_, _ = e.a.CanWrite(ctx(t), web, "m.rosani@contoso.com")
	if n := e.srv.count(http.MethodGet, aclPath); n != 2 {
		t.Errorf("read the ACL %d times after ten minutes, want 2", n)
	}
}

// When Azure DevOps refuses the check, usually a PAT without the Identity
// or Security scope, the answer is "no write access", it is not cached,
// and one warning names the scopes, without the token.
func TestCanWriteFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+projectPath, http.StatusOK, "project_shop.json")
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_two.json")
	e.srv.fixture("GET "+identitiesPath, http.StatusForbidden, "forbidden.json")
	for range 2 {
		ok, err := e.a.CanWrite(ctx(t), web, "m.rosani@contoso.com")
		if ok || err != nil {
			t.Fatalf("CanWrite() = %v, %v; want false, nil", ok, err)
		}
	}
	if n := e.srv.count(http.MethodGet, identitiesPath); n != 2 {
		t.Errorf("asked %d times, want 2 (not cached)", n)
	}
	logs := e.logs.String()
	if strings.Count(logs, "level=WARN") != 1 || !strings.Contains(logs, "Identity (Read) and Security (Manage)") {
		t.Errorf("logs = %s", logs)
	}
	e.noToken(t, nil, pat)
}

// Other errors are returned and not cached.
func TestCanWriteErrorIsNotCached(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+projectPath, http.StatusOK, "project_shop.json")
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_two.json")
	e.srv.sequence("GET "+identitiesPath,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		respond(t, http.StatusOK, "identities_mrosani.json"))
	e.srv.fixture("GET "+aclPath, http.StatusOK, "acl_contributor.json")
	if _, err := e.a.CanWrite(ctx(t), web, "m.rosani@contoso.com"); err == nil {
		t.Fatal("no error for a 500")
	}
	if ok, err := e.a.CanWrite(ctx(t), web, "m.rosani@contoso.com"); err != nil || !ok {
		t.Errorf("second CanWrite() = %v, %v", ok, err)
	}
}
