package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RepositoryConfig is the configuration of one local repository entry.
// A repository whose configuration is invalid is disabled: Err says why,
// and Settings is not usable.
type RepositoryConfig struct {
	Entry      domain.RepositoryEntry
	Repository domain.Repository
	Settings   domain.Effective
	Err        error
}

// Enabled reports whether the repository's configuration is valid.
func (r RepositoryConfig) Enabled() bool {
	return r.Err == nil
}

// ConfigSnapshot is one validated configuration: the global settings, the
// redactor built from the secret patterns, and every repository with its
// effective settings or the reason it is disabled.
type ConfigSnapshot struct {
	File         string
	Global       domain.Effective
	Redactor     *domain.Redactor
	Repositories []RepositoryConfig
}

// Repository returns the configuration of a repository, if it has an
// entry.
func (s *ConfigSnapshot) Repository(r domain.Repository) (RepositoryConfig, bool) {
	for _, rc := range s.Repositories {
		if rc.Repository == r {
			return rc, true
		}
	}
	return RepositoryConfig{}, false
}

// Configuration loads and validates the configuration at startup and on
// reload (spec anthill-configuration, "Validation at startup and reload").
// It is safe for concurrent use.
type Configuration struct {
	source ports.ConfigSource

	mu      sync.RWMutex
	current *ConfigSnapshot
}

// NewConfiguration returns a configuration service reading from source.
func NewConfiguration(source ports.ConfigSource) *Configuration {
	return &Configuration{source: source}
}

// Load reads and validates the configuration at startup. An invalid
// global configuration is an error, and the daemon must not start. An
// invalid repository configuration only disables that repository.
func (c *Configuration) Load(ctx context.Context) (*ConfigSnapshot, error) {
	snap, err := c.read(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.current = snap
	c.mu.Unlock()
	return snap, nil
}

// Reload reads and validates the configuration again. When the new
// global configuration is invalid, the previous configuration stays in
// use and the error is returned. Otherwise the new configuration replaces
// it, and each repository is enabled or disabled on its own.
func (c *Configuration) Reload(ctx context.Context) (*ConfigSnapshot, error) {
	return c.Load(ctx)
}

// Current returns the configuration in use, or nil before Load.
func (c *Configuration) Current() *ConfigSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

// read builds a snapshot from the source.
func (c *Configuration) read(ctx context.Context) (*ConfigSnapshot, error) {
	local, err := c.source.Local(ctx)
	if err != nil {
		return nil, fmt.Errorf("invalid global configuration: %w", err)
	}
	errs := domain.ValidateSettings(local.File, &local.Global)
	global := domain.ConfigLayers{Global: &local.Global}.Resolve()
	if len(errs) == 0 {
		errs = global.Validate()
	}
	redactor, err := domain.NewRedactor(local.Global.SecretPatterns)
	if err != nil && len(errs) == 0 {
		errs = append(errs, &domain.ConfigError{File: local.File, Key: domain.KeySecretPatterns, Message: err.Error()})
	}
	errs = append(errs, duplicateEntries(local)...)
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid global configuration: %w", errors.Join(errs...))
	}

	snap := &ConfigSnapshot{File: local.File, Global: global, Redactor: redactor}
	for i, le := range local.Repositories {
		snap.Repositories = append(snap.Repositories, c.repository(ctx, local, i, le))
	}
	return snap, nil
}

// repository validates one entry and its .anthill.yml.
func (c *Configuration) repository(ctx context.Context, local ports.LocalConfig, index int, le ports.LocalEntry) RepositoryConfig {
	rc := RepositoryConfig{Entry: le.Entry}
	rc.Repository, _ = le.Entry.Repository()
	if le.Err != nil {
		rc.Err = le.Err
		return rc
	}
	if errs := domain.ValidateEntry(local.File, index, le.Entry); len(errs) > 0 {
		rc.Err = errors.Join(errs...)
		return rc
	}
	file, err := c.source.RepositoryFile(ctx, le.Entry)
	if err != nil {
		rc.Err = err
		return rc
	}
	if errs := domain.ValidateSettings(filepath.Join(le.Entry.Path, string(domain.LayerRepositoryFile)), file); len(errs) > 0 {
		rc.Err = errors.Join(errs...)
		return rc
	}
	rc.Settings = domain.ConfigLayers{
		RepositoryFile: file,
		LocalEntry:     &le.Entry.Settings,
		Global:         &local.Global,
	}.Resolve()
	if errs := rc.Settings.Validate(); len(errs) > 0 {
		rc.Err = errors.Join(errs...)
	}
	return rc
}

// duplicateEntries reports repositories listed more than once.
func duplicateEntries(local ports.LocalConfig) []error {
	var errs []error
	seen := map[domain.Repository]bool{}
	for i, le := range local.Repositories {
		r, err := le.Entry.Repository()
		if err != nil {
			continue
		}
		if seen[r] {
			errs = append(errs, &domain.ConfigError{
				File:    local.File,
				Key:     fmt.Sprintf("%s[%d]", domain.KeyRepositories, i),
				Message: fmt.Sprintf("repository %s %s is listed more than once", le.Entry.Provider, le.Entry.Remote),
			})
		}
		seen[r] = true
	}
	return errs
}
