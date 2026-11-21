package app_test

import (
	"context"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// ConnectionCandidates reads only the trackers of one connection's
// repository entries.
func TestConnectionCandidates(t *testing.T) {
	ctx := context.Background()
	config := app.NewConfiguration(&testkit.ConfigSource{Config: ports.LocalConfig{Repositories: []ports.LocalEntry{
		{Entry: domain.RepositoryEntry{Provider: domain.ConfigGitHub, Remote: "acme/shop", Path: t.TempDir()}},
		{Entry: domain.RepositoryEntry{Provider: domain.ConfigAzureDevOps, Remote: "contoso/web/site", Path: t.TempDir()}},
	}}})
	if _, err := config.Load(ctx); err != nil {
		t.Fatal(err)
	}
	var asked []domain.Repository
	gh := testkit.NewProvider(testkit.NewClock(t0))
	shopRepo := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}
	gh.PutItem(domain.WorkItem{Ref: domain.Ref{Repo: shopRepo, Kind: domain.KindItem, Number: 12}, State: domain.StateOpen, Labels: []string{"anthill"}})
	tracker := func(_ context.Context, repo domain.Repository) (ports.TrackerPort, error) {
		asked = append(asked, repo)
		return gh.Client("mrosani"), nil
	}
	got, err := app.ConnectionCandidates(config, domain.ConnectionGitHub, tracker)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != shopRepo {
		t.Errorf("trackers asked for %v, want only %s", asked, shopRepo)
	}
	if len(got) != 1 || got[0].Item.Ref.Number != 12 {
		t.Errorf("candidates = %+v", got)
	}
}
