package domain_test

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestResolveTargetRepository(t *testing.T) {
	shop := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop"}
	web := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "web"}
	api := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "api"}
	ado := func(labels ...string) domain.WorkItem {
		return domain.WorkItem{Ref: domain.Ref{Repo: shop, Kind: domain.KindItem, Number: 12}, Labels: labels}
	}
	gh := domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "anthill"}
	ghItem := func(labels ...string) domain.WorkItem {
		return domain.WorkItem{Ref: domain.Ref{Repo: gh, Kind: domain.KindItem, Number: 12}, Labels: labels}
	}
	tests := []struct {
		name        string
		item        domain.WorkItem
		defaultRepo string
		repos       []domain.Repository
		want        domain.Repository
		reason      string
	}{
		{name: "github issue maps to its own repository", item: ghItem("anthill"), want: gh},
		{name: "github repo: override with owner", item: ghItem("repo:Acme/Web"), want: domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}},
		{name: "github repo: override under the same owner", item: ghItem("repo:other"), want: domain.Repository{Provider: domain.GitHub, Org: "matteorosani", Name: "other"}},
		{name: "repo: label wins over the default", item: ado("anthill", "repo:API"), defaultRepo: "web", repos: []domain.Repository{web, api}, want: api},
		{name: "project default", item: ado("anthill"), defaultRepo: "Web", repos: []domain.Repository{web, api}, want: web},
		{name: "only repository of the project", item: ado("anthill"), repos: []domain.Repository{web}, want: web},
		// Scenario "Azure DevOps item with no repository".
		{name: "azure devops item with no repository", item: ado("anthill"), repos: []domain.Repository{web, api}, reason: "cannot determine target repository"},
		{name: "repo: label names no repository of the project", item: ado("repo:mobile"), repos: []domain.Repository{web, api}, reason: "repository mobile not found in project contoso/shop"},
		{name: "default names no repository of the project", item: ado(), defaultRepo: "mobile", repos: []domain.Repository{web}, reason: "repository mobile not found in project contoso/shop"},
		{name: "conflicting repo: labels", item: ado("repo:web", "repo:api"), repos: []domain.Repository{web, api}, reason: "conflicting labels repo:web, repo:api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := domain.ResolveTargetRepository(tt.item, tt.defaultRepo, tt.repos)
			if got != tt.want || reason != tt.reason {
				t.Errorf("got %+v, %q; want %+v, %q", got, reason, tt.want, tt.reason)
			}
		})
	}
}
