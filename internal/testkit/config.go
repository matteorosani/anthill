package testkit

import (
	"context"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ConfigSource is an in-memory ports.ConfigSource. Set Config to what the
// user's configuration file holds, and RepositoryFiles to the
// .anthill.yml of each clone, keyed by the entry's path; a path without
// a file has none. ConfigErr and RepositoryErrs are returned instead
// when set. To simulate an edit before a reload, change the fields with
// Update.
type ConfigSource struct {
	mu              sync.Mutex
	Config          ports.LocalConfig
	ConfigErr       error
	RepositoryFiles map[string]*domain.SettingValues
	RepositoryErrs  map[string]error
}

var _ ports.ConfigSource = (*ConfigSource)(nil)

// Update changes the fake's content safely while it is in use.
func (c *ConfigSource) Update(change func(*ConfigSource)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	change(c)
}

// Local returns Config, or ConfigErr when set.
func (c *ConfigSource) Local(_ context.Context) (ports.LocalConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ConfigErr != nil {
		return ports.LocalConfig{}, c.ConfigErr
	}
	return c.Config, nil
}

// RepositoryFile returns the file stored for the entry's path.
func (c *ConfigSource) RepositoryFile(_ context.Context, entry domain.RepositoryEntry) (*domain.SettingValues, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.RepositoryErrs[entry.Path]; err != nil {
		return nil, err
	}
	return c.RepositoryFiles[entry.Path], nil
}
