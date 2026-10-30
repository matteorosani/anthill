package azuredevops

import (
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The item of a PR comes from its native links first, then from a
// closing keyword before AB#<id>, then from the ant branch name.
func TestLinkedItem(t *testing.T) {
	a, err := New(Options{Organization: "contoso", Credentials: &testkit.CredentialStore{}, Clock: testkit.NewClock(time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "web"}
	tests := []struct {
		name   string
		body   string
		branch string
		native []int
		want   int
	}{
		{name: "native link wins", body: "Closes AB#12", branch: "refs/heads/ant/ado-13-x", native: []int{14, 11}, want: 11},
		{name: "closing reference", body: "Fixes the timeout.\n\nCloses AB#12", branch: "refs/heads/feature/x", want: 12},
		{name: "closing keyword with colon", body: "resolves: AB#7.", branch: "refs/heads/x", want: 7},
		{name: "mention without closing keyword", body: "See AB#12", branch: "refs/heads/feature/x"},
		{name: "PR body reference removed", body: "Edited away", branch: "refs/heads/ant/ado-12-fix-login", want: 12},
		{name: "other prefix", branch: "refs/heads/bot/ado-12-fix"},
		{name: "github branch", branch: "refs/heads/ant/gh-12-fix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.linkedItem(repo, prJSON{Description: tt.body, SourceRefName: tt.branch}, tt.native)
			switch {
			case tt.want == 0 && got != nil:
				t.Errorf("linked to %v, want none", got)
			case tt.want != 0 && (got == nil || got.Number != tt.want || got.Repo.Name != "" || got.Repo.Project != "shop"):
				t.Errorf("linked to %v, want item %d of contoso/shop", got, tt.want)
			}
		})
	}
}
