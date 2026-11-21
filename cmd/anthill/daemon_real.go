package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/matteorosani/anthill/internal/adapters/azuredevops"
	"github.com/matteorosani/anthill/internal/adapters/clisession"
	"github.com/matteorosani/anthill/internal/adapters/git"
	"github.com/matteorosani/anthill/internal/adapters/github"
	"github.com/matteorosani/anthill/internal/adapters/keyring"
	"github.com/matteorosani/anthill/internal/adapters/localexec"
	"github.com/matteorosani/anthill/internal/adapters/sqlite"
	"github.com/matteorosani/anthill/internal/adapters/verify"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// storeFileName is the local store's file name in <anthill-home>.
const storeFileName = "anthill.db"

// portsFactory builds the daemon's adapters once the configuration is
// loaded.
type portsFactory func(ctx context.Context, env *environment, config *app.ConfigSnapshot, logger *slog.Logger) (daemonPorts, error)

// realPorts builds the adapters of this machine: the SQLite store in
// <anthill-home>, one GitHub adapter and one Azure DevOps adapter per
// organization of the enabled repository entries, with credentials from
// the OS credential store and then the provider's CLI session, git, the
// local executor with the three runners, and the verification command
// runner. No adapter sends a request before its first call, so a daemon
// started offline or without credentials starts; its polls fail and say
// why.
func realPorts(ctx context.Context, env *environment, config *app.ConfigSnapshot, logger *slog.Logger) (daemonPorts, error) {
	exe, err := os.Executable()
	if err != nil {
		return daemonPorts{}, fmt.Errorf("finding the anthill program: %w", err)
	}
	runners, err := agentRunners(exe, nil)
	if err != nil {
		return daemonPorts{}, err
	}
	machine, err := os.Hostname()
	if err != nil {
		return daemonPorts{}, fmt.Errorf("finding the machine name: %w", err)
	}
	if err := os.MkdirAll(env.home, 0o700); err != nil {
		return daemonPorts{}, fmt.Errorf("creating %s: %w", env.home, err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(env.home, storeFileName))
	if err != nil {
		return daemonPorts{}, err
	}
	g := git.New(config.Redactor, env.home)
	return daemonPorts{
		Store:       store,
		Connections: providerConnections(env, config, logger),
		Git:         g,
		Files:       g,
		Executor:    localexec.New(env.clock, runners, localexec.Options{}),
		Runners:     runners,
		Verifier:    verify.New(),
		Machine:     machine,
		Close:       store.Close,
	}, nil
}

// providerConnections builds one provider adapter per connection of the
// enabled repository entries: GitHub, and each Azure DevOps organization.
func providerConnections(env *environment, config *app.ConfigSnapshot, logger *slog.Logger) []providerConnection {
	creds := app.NewCredentials(config.Redactor, keyring.Store{}, clisession.New(clisession.ExecRunner))
	settings := func(repo domain.Repository) domain.Effective {
		for _, rc := range config.Repositories {
			if rc.Enabled() && (sameRepository(rc.Repository, repo) || sameRepository(domain.TrackerRepository(rc.Repository), repo)) {
				return rc.Settings
			}
		}
		return config.Global
	}
	var out []providerConnection
	var names []string
	for _, rc := range config.Repositories {
		name := domain.ConnectionName(rc.Repository)
		if !rc.Enabled() || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
		switch rc.Repository.Provider {
		case domain.GitHub:
			a, err := github.New(github.Options{
				Credentials: creds, Clock: env.clock, Version: version, Logger: logger, Redactor: config.Redactor,
				Repo: func(repo domain.Repository) github.RepoOptions {
					s := settings(repo)
					return github.RepoOptions{OptInLabel: s.OptInLabel, BranchPrefix: s.BranchPrefix}
				},
			})
			if err != nil {
				logger.Error("GitHub adapter not built", "error", err)
				continue
			}
			out = append(out, providerConnection{Name: name, Provider: a, Login: a.Login})
		case domain.AzureDevOps:
			a, err := azuredevops.New(azuredevops.Options{
				Organization: rc.Repository.Org,
				Credentials:  creds, Clock: env.clock, Version: version, Logger: logger, Redactor: config.Redactor,
				Repo: func(repo domain.Repository) azuredevops.RepoOptions {
					s := settings(repo)
					return azuredevops.RepoOptions{OptInLabel: s.OptInLabel, BranchPrefix: s.BranchPrefix}
				},
			})
			if err != nil {
				logger.Error("Azure DevOps adapter not built", "organization", rc.Repository.Org, "error", err)
				continue
			}
			out = append(out, providerConnection{Name: name, Provider: a, Login: a.Login})
		}
	}
	return out
}

var _ portsFactory = realPorts

// providerCheck makes sure both adapters are complete providers.
var (
	_ provider            = (*github.Adapter)(nil)
	_ provider            = (*azuredevops.Adapter)(nil)
	_ ports.WorktreeFiles = (*git.Git)(nil)
)
