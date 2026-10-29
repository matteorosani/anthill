// Package configfile reads Anthill's YAML configuration files: the user's
// config.yml, which holds the global configuration and the local
// repository entries, and the .anthill.yml committed in a repository. It
// implements ports.ConfigSource.
package configfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The file names.
const (
	FileName           = "config.yml"
	RepositoryFileName = ".anthill.yml"
)

// DefaultPath returns where the user's configuration file lives:
// anthill/config.yml in the OS user configuration directory, that is
// ~/.config on Linux, ~/Library/Application Support on macOS and
// %AppData% on Windows.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("finding the configuration directory: %w", err)
	}
	return filepath.Join(dir, "anthill", FileName), nil
}

// Source reads the configuration from files.
type Source struct {
	path string
}

var _ ports.ConfigSource = (*Source)(nil)

// New returns a source that reads the user's configuration from path.
func New(path string) *Source {
	return &Source{path: path}
}

// Local reads the user's configuration file. A missing file is an empty
// configuration.
func (s *Source) Local(_ context.Context) (ports.LocalConfig, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return ports.LocalConfig{File: s.path}, nil
	}
	if err != nil {
		return ports.LocalConfig{}, err
	}
	return ParseLocal(s.path, data)
}

// RepositoryFile reads .anthill.yml at the root of the entry's clone.
func (s *Source) RepositoryFile(_ context.Context, entry domain.RepositoryEntry) (*domain.SettingValues, error) {
	name := filepath.Join(entry.Path, RepositoryFileName)
	data, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseRepositoryFile(name, data)
}

// ParseRepositoryFile parses the content of a .anthill.yml named name.
// It rejects forbidden keys (credentials, telemetry, secret patterns,
// machine paths and settings), unknown keys and values that look like
// secrets, and returns every problem joined.
func ParseRepositoryFile(name string, data []byte) (*domain.SettingValues, error) {
	root, err := parseMapping(name, data)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return &domain.SettingValues{}, nil
	}
	errs := checkKeys(name, domain.LayerRepositoryFile, "", root, nil)
	errs = append(errs, checkValues(name, "", root)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	var v domain.SettingValues
	if err := decode(name, "", root, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// ParseLocal parses the content of the user's configuration file named
// name. Problems at the top level are returned as the error. Problems in
// one repository entry are kept in that entry's Err, so the other
// repositories stay usable.
func ParseLocal(name string, data []byte) (ports.LocalConfig, error) {
	cfg := ports.LocalConfig{File: name}
	root, err := parseMapping(name, data)
	if err != nil || root == nil {
		return cfg, err
	}

	// The top level without the repositories, decoded as the global
	// settings.
	global := &yaml.Node{Kind: yaml.MappingNode, Tag: root.Tag, Line: root.Line, Column: root.Column}
	var repositories *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == domain.KeyRepositories {
			repositories = root.Content[i+1]
			continue
		}
		global.Content = append(global.Content, root.Content[i], root.Content[i+1])
	}

	errs := checkKeys(name, domain.LayerGlobal, "", global, nil)
	errs = append(errs, checkValues(name, "", global)...)
	if repositories != nil && repositories.Kind != yaml.SequenceNode {
		errs = append(errs, &domain.ConfigError{File: name, Key: domain.KeyRepositories, Message: "repositories must be a list of entries"})
	}
	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	if err := decode(name, "", global, &cfg.Global); err != nil {
		return cfg, err
	}
	if repositories == nil {
		return cfg, nil
	}
	for i, node := range repositories.Content {
		cfg.Repositories = append(cfg.Repositories, parseEntry(name, i, node))
	}
	return cfg, nil
}

// parseEntry reads one repository entry.
func parseEntry(name string, index int, node *yaml.Node) ports.LocalEntry {
	prefix := fmt.Sprintf("%s[%d]", domain.KeyRepositories, index)
	if node.Kind != yaml.MappingNode {
		return ports.LocalEntry{Err: &domain.ConfigError{File: name, Key: prefix, Message: "a repository entry must be a mapping of keys to values"}}
	}
	errs := checkKeys(name, domain.LayerLocalEntry, prefix, node, nil)
	errs = append(errs, checkValues(name, prefix, node)...)
	var entry domain.RepositoryEntry
	if len(errs) == 0 {
		if err := decode(name, prefix, node, &entry); err != nil {
			errs = append(errs, err)
		}
	} else {
		// Keep the identity, so the disabled repository can be named.
		_ = node.Decode(&struct {
			Provider *string `yaml:"provider"`
			Remote   *string `yaml:"remote"`
		}{&entry.Provider, &entry.Remote})
	}
	return ports.LocalEntry{Entry: entry, Err: errors.Join(errs...)}
}

// parseMapping parses data and returns its top-level mapping, or nil for
// an empty document.
func parseMapping(name string, data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, &domain.ConfigError{File: name, Message: err.Error()}
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, &domain.ConfigError{File: name, Message: "the file must be a mapping of keys to values"}
	}
	return root, nil
}

// checkKeys checks every key of a mapping, and of the mappings inside it,
// against domain.CheckKey. It does not look inside a key that is refused.
func checkKeys(file string, layer domain.Layer, prefix string, node *yaml.Node, path []string) []error {
	var errs []error
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			p := append(path[:len(path):len(path)], node.Content[i].Value)
			if msg := domain.CheckKey(layer, p); msg != "" {
				errs = append(errs, &domain.ConfigError{File: file, Key: prefix, Message: msg})
				continue
			}
			errs = append(errs, checkKeys(file, layer, prefix, node.Content[i+1], p)...)
		}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			errs = append(errs, checkKeys(file, layer, prefix, item, path)...)
		}
	}
	return errs
}

// checkValues rejects values that look like secrets. The local secret
// patterns themselves are left alone.
func checkValues(file, prefix string, node *yaml.Node) []error {
	var errs []error
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key == domain.KeySecretPatterns {
			continue
		}
		if containsSecret(node.Content[i+1]) {
			errs = append(errs, &domain.ConfigError{File: file, Key: prefix, Message: domain.SecretValueMessage(key)})
		}
	}
	return errs
}

func containsSecret(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode {
		return domain.LooksLikeSecret(node.Value)
	}
	for _, c := range node.Content {
		if containsSecret(c) {
			return true
		}
	}
	return false
}

// decode decodes node into out, turning YAML type errors into a
// *domain.ConfigError.
func decode(file, prefix string, node *yaml.Node, out any) error {
	if err := node.Decode(out); err != nil {
		msg := err.Error()
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			msg = strings.Join(typeErr.Errors, "; ")
		}
		return &domain.ConfigError{File: file, Key: prefix, Message: msg}
	}
	return nil
}
