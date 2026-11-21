package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

var (
	adoProject = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "web"}
	adoSite    = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "web", Name: "site"}
	adoAPI     = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "web", Name: "api"}
)

// routesEnv has acme/shop on GitHub and contoso/web/site on Azure
// DevOps configured, with clones, and contoso/web/api not configured.
type routesEnv struct {
	r      *routes
	gh     *testkit.Provider
	ado    *testkit.Provider
	logins int
	fail   error
}

func newRoutesEnv(t *testing.T) *routesEnv {
	t.Helper()
	ctx := context.Background()
	shopDir, siteDir := t.TempDir(), t.TempDir()
	label := "shop:claimed"
	config := app.NewConfiguration(&testkit.ConfigSource{Config: ports.LocalConfig{Repositories: []ports.LocalEntry{
		{Entry: domain.RepositoryEntry{Provider: domain.ConfigGitHub, Remote: "acme/shop", Path: shopDir,
			Settings: domain.SettingValues{ClaimLabel: &label}}},
		{Entry: domain.RepositoryEntry{Provider: domain.ConfigAzureDevOps, Remote: "contoso/web/site", Path: siteDir}},
	}}})
	if _, err := config.Load(ctx); err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewClock(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC))
	e := &routesEnv{gh: testkit.NewProvider(clock), ado: testkit.NewProvider(clock)}
	e.ado.PutRepositories(adoProject, adoSite, adoAPI)
	git := testkit.NewSourceControl()
	git.AddClone(shopDir, shopRepo, "main")
	git.AddClone(siteDir, adoSite, "main")
	login := func(name string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) {
			e.logins++
			if e.fail != nil {
				return "", e.fail
			}
			return name, nil
		}
	}
	e.r = newRoutes(config, git, "LAPTOP-42", []providerConnection{
		{Name: domain.ConnectionGitHub, Provider: e.gh.Client("mrosani"), Login: login("mrosani")},
		{Name: domain.ConnectionName(adoSite), Provider: e.ado.Client("m.rosani@contoso.com"), Login: login("m.rosani@contoso.com")},
	})
	return e
}

func adoItem(n int, labels ...string) domain.WorkItem {
	return domain.WorkItem{Ref: domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: n}, State: domain.StateOpen, Labels: labels}
}

// Spec work-item-sources, "Repository mapping": an item maps to its
// target repository, which is available when configured and cloned here;
// an Azure DevOps item whose project has several repositories and no
// repo: label is unmapped.
func TestRoutesTarget(t *testing.T) {
	ctx := context.Background()
	e := newRoutesEnv(t)
	for _, tt := range []struct {
		name      string
		item      domain.WorkItem
		repo      domain.Repository
		available bool
		claim     string
	}{
		{"GitHub issue", domain.WorkItem{Ref: shopItem(12)}, shopRepo, true, "shop:claimed"},
		{"repo label", adoItem(7, "repo:site"), adoSite, true, domain.DefaultClaimLabel},
		{"not configured here", adoItem(8, "repo:api"), adoAPI, false, domain.DefaultClaimLabel},
		{"unmapped", adoItem(9), domain.Repository{}, false, domain.DefaultClaimLabel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.r.target(ctx, tt.item)
			if err != nil {
				t.Fatal(err)
			}
			if got.Repository != tt.repo || got.Available != tt.available || got.Settings.ClaimLabel != tt.claim {
				t.Errorf("target = %s, available %v, claim label %q", got.Repository, got.Available, got.Settings.ClaimLabel)
			}
		})
	}
}

// The workspace of an item is its target repository's, and of a PR its
// own repository's, with the clone and the repository's settings. An
// item of a repository not configured here has none.
func TestRoutesWorkspace(t *testing.T) {
	ctx := context.Background()
	e := newRoutesEnv(t)
	e.gh.PutItem(domain.WorkItem{Ref: shopItem(12), State: domain.StateOpen})
	ws, err := e.r.workspace(ctx, shopItem(12))
	if err != nil || ws.Repo != shopRepo || ws.Clone.Path == "" || ws.Settings.ClaimLabel != "shop:claimed" {
		t.Errorf("item workspace = %+v, %v", ws, err)
	}
	pr := domain.Ref{Repo: adoSite, Kind: domain.KindPR, Number: 34}
	if ws, err := e.r.workspace(ctx, pr); err != nil || ws.Repo != adoSite {
		t.Errorf("PR workspace = %+v, %v", ws, err)
	}
	e.ado.PutItem(adoItem(8, "repo:api"))
	if _, err := e.r.workspace(ctx, adoItem(8).Ref); err == nil {
		t.Error("workspace of an item of a repository not configured here, want an error")
	}
}

// Spec work-claiming, "Instance identity": the instance id is the login
// on the item's provider and the machine id. The login is read once per
// connection, and a failed read is tried again.
func TestRoutesClaimTarget(t *testing.T) {
	ctx := context.Background()
	e := newRoutesEnv(t)
	e.fail = errors.New("offline")
	if _, err := e.r.claim(ctx, shopItem(12)); err == nil {
		t.Fatal("claim target without login, want an error")
	}
	e.fail = nil
	e.gh.PutItem(domain.WorkItem{Ref: shopItem(12), State: domain.StateOpen})
	for range 2 {
		got, err := e.r.claim(ctx, shopItem(12))
		if err != nil || got.Instance != "mrosani@LAPTOP-42" || got.Label != "shop:claimed" {
			t.Fatalf("claim target = %+v, %v", got, err)
		}
	}
	if e.logins != 2 {
		t.Errorf("login read %d times, want 2 (one failed, one cached)", e.logins)
	}
	got, err := e.r.instance(ctx, adoSite)
	if err != nil || got != "m.rosani@contoso.com@LAPTOP-42" {
		t.Errorf("Azure DevOps instance = %q, %v", got, err)
	}
	if _, err := e.r.tracker(ctx, domain.Ref{Repo: domain.Repository{Provider: domain.AzureDevOps, Org: "fabrikam", Project: "x"}}); err == nil {
		t.Error("tracker of an unknown connection, want an error")
	}
}
