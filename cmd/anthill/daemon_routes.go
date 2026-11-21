package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// routes finds, for an item, a PR or a repository, the provider
// connection that serves it, this instance's id there, the target
// repository, the clone and the settings. The application services take
// these lookups as functions (app.TrackerRoute, app.WorkspaceRoute and
// the others); routes builds them from the configuration and the provider
// connections of the daemon.
type routes struct {
	config      *app.Configuration
	git         ports.SourceControl
	connections map[string]*connection
	// machine is this machine's id, the second part of the instance id.
	machine string

	mu sync.Mutex
	// clones caches the clone of each code repository, opened once.
	clones map[domain.Repository]ports.Clone
	// targets caches the target repository of each item: an ant's
	// repository does not change once it works on it.
	targets map[domain.Ref]domain.Repository
}

// connection is one provider connection with this instance's login on
// it, read once.
type connection struct {
	providerConnection

	mu    sync.Mutex
	login string
}

func newRoutes(config *app.Configuration, git ports.SourceControl, machine string, conns []providerConnection) *routes {
	r := &routes{
		config: config, git: git, machine: machine,
		connections: map[string]*connection{},
		clones:      map[domain.Repository]ports.Clone{},
		targets:     map[domain.Ref]domain.Repository{},
	}
	for _, c := range conns {
		r.connections[c.Name] = &connection{providerConnection: c}
	}
	return r
}

// connection returns the provider connection of a repository.
func (r *routes) connection(repo domain.Repository) (*connection, error) {
	name := domain.ConnectionName(repo)
	c, ok := r.connections[name]
	if !ok {
		return nil, fmt.Errorf("no provider connection %s for %s", name, repo)
	}
	return c, nil
}

// tracker is an app.TrackerRoute.
func (r *routes) tracker(_ context.Context, ref domain.Ref) (ports.TrackerPort, error) {
	c, err := r.connection(ref.Repo)
	if err != nil {
		return nil, err
	}
	return c.Provider, nil
}

// trackerOf finds the tracker of a tracker repository.
func (r *routes) trackerOf(_ context.Context, repo domain.Repository) (ports.TrackerPort, error) {
	c, err := r.connection(repo)
	if err != nil {
		return nil, err
	}
	return c.Provider, nil
}

// codeHost is an app.CodeHostRoute.
func (r *routes) codeHost(_ context.Context, repo domain.Repository) (ports.CodeHostPort, error) {
	c, err := r.connection(repo)
	if err != nil {
		return nil, err
	}
	return c.Provider, nil
}

// queen is an app.QueenCommentRoute.
func (r *routes) queen(_ context.Context, ref domain.Ref) (ports.QueenCommentPort, error) {
	c, err := r.connection(ref.Repo)
	if err != nil {
		return nil, err
	}
	return c.Provider, nil
}

// writeAccess is an app.WriteAccess.
func (r *routes) writeAccess(ctx context.Context, repo domain.Repository, login string) (bool, error) {
	c, err := r.connection(repo)
	if err != nil {
		return false, err
	}
	return c.Provider.CanWrite(ctx, repo, login)
}

// instance returns this instance's id on the provider of repo: the login
// there and the machine id, such as mrosani@LAPTOP-42 (spec
// work-claiming, "Instance identity"). The login is read once per
// connection; a failed read is tried again at the next call.
func (r *routes) instance(ctx context.Context, repo domain.Repository) (string, error) {
	c, err := r.connection(repo)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.login == "" {
		login, err := c.Login(ctx)
		if err != nil {
			return "", fmt.Errorf("reading the login on %s: %w", c.Name, err)
		}
		c.login = login
	}
	return c.login + "@" + r.machine, nil
}

// snapshot is the configuration in use.
func (r *routes) snapshot() (*app.ConfigSnapshot, error) {
	snap := r.config.Current()
	if snap == nil {
		return nil, errors.New("the configuration is not loaded")
	}
	return snap, nil
}

// entry is the enabled repository entry of a code repository.
func (r *routes) entry(repo domain.Repository) (app.RepositoryConfig, error) {
	snap, err := r.snapshot()
	if err != nil {
		return app.RepositoryConfig{}, err
	}
	for _, rc := range snap.Repositories {
		if sameRepository(rc.Repository, repo) {
			if !rc.Enabled() {
				return rc, fmt.Errorf("repository %s is disabled: %w", repo, rc.Err)
			}
			return rc, nil
		}
	}
	return app.RepositoryConfig{}, fmt.Errorf("repository %s is not configured on this machine", repo)
}

// sameRepository compares repositories in any case, as providers do.
func sameRepository(a, b domain.Repository) bool {
	return a.Provider == b.Provider && strings.EqualFold(a.Org, b.Org) &&
		strings.EqualFold(a.Project, b.Project) && strings.EqualFold(a.Name, b.Name)
}

// clone opens the clone of a configured repository, once.
func (r *routes) clone(ctx context.Context, rc app.RepositoryConfig) (ports.Clone, error) {
	r.mu.Lock()
	c, ok := r.clones[rc.Repository]
	r.mu.Unlock()
	if ok {
		return c, nil
	}
	c, err := r.git.OpenClone(ctx, rc.Entry.Path, rc.Repository)
	if err != nil {
		return ports.Clone{}, fmt.Errorf("opening the clone of %s: %w", rc.Repository, err)
	}
	r.mu.Lock()
	r.clones[rc.Repository] = c
	r.mu.Unlock()
	return c, nil
}

// resolve maps an item to its target repository (spec work-item-sources,
// "Repository mapping"). It returns the zero repository and the reason
// when the item is unmapped.
func (r *routes) resolve(ctx context.Context, item domain.WorkItem) (domain.Repository, string, error) {
	var repos []domain.Repository
	defaultRepo := ""
	if item.Ref.Repo.Provider == domain.AzureDevOps {
		snap, err := r.snapshot()
		if err != nil {
			return domain.Repository{}, "", err
		}
		project := domain.TrackerRepository(item.Ref.Repo)
		defaultRepo = snap.Global.DefaultRepository(project)
		host, err := r.codeHost(ctx, project)
		if err != nil {
			return domain.Repository{}, "", err
		}
		if repos, err = host.Repositories(ctx, project); err != nil {
			return domain.Repository{}, "", fmt.Errorf("listing the repositories of %s: %w", project, err)
		}
	}
	repo, reason := domain.ResolveTargetRepository(item, defaultRepo, repos)
	return repo, reason, nil
}

// target is an app.TargetRoute.
func (r *routes) target(ctx context.Context, item domain.WorkItem) (app.ItemTarget, error) {
	snap, err := r.snapshot()
	if err != nil {
		return app.ItemTarget{}, err
	}
	out := app.ItemTarget{Settings: snap.Global}
	repo, reason, err := r.resolve(ctx, item)
	if err != nil || reason != "" {
		return out, err
	}
	out.Repository = repo
	rc, err := r.entry(repo)
	if err != nil {
		// Not configured or disabled here: the item is not available on
		// this machine, which is a prerequisite, not a failure.
		return out, nil
	}
	out.Settings = rc.Settings
	if _, err := r.clone(ctx, rc); err == nil {
		out.Available = true
	}
	r.mu.Lock()
	r.targets[item.Ref] = repo
	r.mu.Unlock()
	return out, nil
}

// itemRepository is the target repository of an item, read from its
// tracker unless cached.
func (r *routes) itemRepository(ctx context.Context, ref domain.Ref) (domain.Repository, error) {
	r.mu.Lock()
	repo, ok := r.targets[ref]
	r.mu.Unlock()
	if ok {
		return repo, nil
	}
	tracker, err := r.tracker(ctx, ref)
	if err != nil {
		return domain.Repository{}, err
	}
	item, _, err := tracker.Item(ctx, ref)
	if err != nil {
		return domain.Repository{}, fmt.Errorf("reading %s: %w", ref, err)
	}
	repo, reason, err := r.resolve(ctx, item)
	if err != nil {
		return domain.Repository{}, err
	}
	if reason != "" {
		return domain.Repository{}, fmt.Errorf("%s is unmapped: %s", ref, reason)
	}
	r.mu.Lock()
	r.targets[ref] = repo
	r.mu.Unlock()
	return repo, nil
}

// workspace is an app.WorkspaceRoute: an item's target repository, or a
// PR's own, with its clone and effective settings.
func (r *routes) workspace(ctx context.Context, ref domain.Ref) (app.ToolWorkspace, error) {
	repo := ref.Repo
	if ref.Kind == domain.KindItem {
		var err error
		if repo, err = r.itemRepository(ctx, ref); err != nil {
			return app.ToolWorkspace{}, err
		}
	}
	rc, err := r.entry(repo)
	if err != nil {
		return app.ToolWorkspace{}, err
	}
	clone, err := r.clone(ctx, rc)
	if err != nil {
		return app.ToolWorkspace{}, err
	}
	return app.ToolWorkspace{Clone: clone, Repo: rc.Repository, Settings: rc.Settings}, nil
}

// claim is an app.ClaimRoute: the provider of the item or PR, this
// instance's id there, and the claim label of its repository.
func (r *routes) claim(ctx context.Context, ref domain.Ref) (app.ClaimTarget, error) {
	c, err := r.connection(ref.Repo)
	if err != nil {
		return app.ClaimTarget{}, err
	}
	instance, err := r.instance(ctx, ref.Repo)
	if err != nil {
		return app.ClaimTarget{}, err
	}
	label := domain.DefaultClaimLabel
	if snap := r.config.Current(); snap != nil && snap.Global.ClaimLabel != "" {
		label = snap.Global.ClaimLabel
	}
	if ws, err := r.workspace(ctx, ref); err == nil && ws.Settings.ClaimLabel != "" {
		label = ws.Settings.ClaimLabel
	}
	return app.ClaimTarget{Provider: c.Provider, Instance: instance, Label: label}, nil
}

// recoverySources is the session recovery's list of the enabled
// repository entries.
func (r *routes) recoverySources(ctx context.Context) ([]app.RecoverySource, error) {
	snap, err := r.snapshot()
	if err != nil {
		return nil, err
	}
	var out []app.RecoverySource
	var errs []error
	for _, rc := range snap.Repositories {
		if !rc.Enabled() {
			continue
		}
		c, err := r.connection(rc.Repository)
		if err == nil {
			var clone ports.Clone
			if clone, err = r.clone(ctx, rc); err == nil {
				out = append(out, app.RecoverySource{
					Repo: rc.Repository, Clone: clone, Settings: rc.Settings,
					CodeHost: c.Provider, Tracker: c.Provider, TrackerRepo: domain.TrackerRepository(rc.Repository),
				})
				continue
			}
		}
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}

// repositories lists the code repositories of the enabled entries.
func (r *routes) repositories() []domain.Repository {
	snap := r.config.Current()
	if snap == nil {
		return nil
	}
	var out []domain.Repository
	for _, rc := range snap.Repositories {
		if rc.Enabled() {
			out = append(out, rc.Repository)
		}
	}
	return out
}
