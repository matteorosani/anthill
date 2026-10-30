package azuredevops_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

var api = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "api"}

// Repositories lists a project's Git repositories in lowercase, and keeps
// the list for ten minutes.
func TestRepositories(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_two.json")
	for range 2 {
		got, err := e.a.Repositories(ctx(t), domain.Repository{Provider: domain.AzureDevOps, Org: "Contoso", Project: "SHOP"})
		if err != nil || !slices.Equal(got, []domain.Repository{web, api}) {
			t.Fatalf("Repositories() = %v, %v", got, err)
		}
	}
	if n := e.srv.count(http.MethodGet, reposPath); n != 1 {
		t.Errorf("listed %d times, want 1 (cached)", n)
	}
	e.clock.Advance(10 * time.Minute)
	_, _ = e.a.Repositories(ctx(t), shop)
	if n := e.srv.count(http.MethodGet, reposPath); n != 2 {
		t.Errorf("listed %d times after ten minutes, want 2", n)
	}
}

// Scenario "Azure DevOps item with no repository": an item with the
// anthill tag in a project with several repositories, no default and no
// repo: tag is unmapped with the reason "cannot determine target
// repository". With a repo: tag, a default, or a single repository, it
// maps.
func TestAzureDevOpsItemWithNoRepository(t *testing.T) {
	e := newEnv(t)
	e.srv.fixture("GET "+reposPath, http.StatusOK, "repositories_two.json")
	repos, err := e.a.Repositories(ctx(t), shop)
	if err != nil {
		t.Fatal(err)
	}
	item := domain.WorkItem{Ref: ado12, Labels: []string{"anthill"}}
	if got, reason := domain.ResolveTargetRepository(item, "", repos); got != (domain.Repository{}) || reason != "cannot determine target repository" {
		t.Errorf("no repo: tag, no default = %v, %q", got, reason)
	}
	item.Labels = append(item.Labels, "repo:API")
	if got, reason := domain.ResolveTargetRepository(item, "web", repos); got != api || reason != "" {
		t.Errorf("repo: tag = %v, %q; want %v", got, reason, api)
	}
	item.Labels = []string{"anthill"}
	if got, reason := domain.ResolveTargetRepository(item, "Web", repos); got != web || reason != "" {
		t.Errorf("project default = %v, %q; want %v", got, reason, web)
	}
	if got, reason := domain.ResolveTargetRepository(item, "", repos[:1]); got != web || reason != "" {
		t.Errorf("single repository = %v, %q; want %v", got, reason, web)
	}
}
