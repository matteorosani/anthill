package ports

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
)

// LocalConfig is the user's configuration file as read: the global
// settings and the local repository entries. An entry that could not be
// read has Err set, so that only that repository is disabled.
type LocalConfig struct {
	// File names the file, for error messages.
	File         string
	Global       domain.SettingValues
	Repositories []LocalEntry
}

// LocalEntry is one local repository entry and the error found while
// reading it, if any.
type LocalEntry struct {
	Entry domain.RepositoryEntry
	Err   error
}

// ConfigSource reads configuration files. It checks which keys each file
// may hold (domain.CheckKey) and decodes the values; value rules are left
// to the caller.
type ConfigSource interface {
	// Local reads the user's configuration file. A missing file is an
	// empty configuration. An error means the global configuration cannot
	// be used.
	Local(ctx context.Context) (LocalConfig, error)
	// RepositoryFile reads .anthill.yml from the entry's clone. It returns
	// nil and no error when the repository has no such file.
	RepositoryFile(ctx context.Context, entry domain.RepositoryEntry) (*domain.SettingValues, error)
}
