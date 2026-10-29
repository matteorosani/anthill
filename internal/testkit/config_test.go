package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestConfigSource(t *testing.T) {
	ctx := context.Background()
	budget := 45
	e := domain.RepositoryEntry{Provider: "github", Remote: "a/b", Path: "/a"}
	src := &testkit.ConfigSource{
		Config:          ports.LocalConfig{File: "config.yml", Repositories: []ports.LocalEntry{{Entry: e}}},
		RepositoryFiles: map[string]*domain.SettingValues{"/a": {Budget: &budget}},
	}
	cfg, err := src.Local(ctx)
	if err != nil || cfg.File != "config.yml" || len(cfg.Repositories) != 1 {
		t.Errorf("Local() = %+v, %v", cfg, err)
	}
	if v, err := src.RepositoryFile(ctx, e); err != nil || *v.Budget != 45 {
		t.Errorf("RepositoryFile() = %+v, %v", v, err)
	}
	if v, err := src.RepositoryFile(ctx, domain.RepositoryEntry{Path: "/none"}); v != nil || err != nil {
		t.Errorf("missing file = %+v, %v", v, err)
	}

	boom := errors.New("boom")
	src.Update(func(s *testkit.ConfigSource) {
		s.ConfigErr = boom
		s.RepositoryErrs = map[string]error{"/a": boom}
	})
	if _, err := src.Local(ctx); !errors.Is(err, boom) {
		t.Errorf("Local() error = %v", err)
	}
	if _, err := src.RepositoryFile(ctx, e); !errors.Is(err, boom) {
		t.Errorf("RepositoryFile() error = %v", err)
	}
}

func TestCredentialStore(t *testing.T) {
	ctx := context.Background()
	key := domain.GitHubCredential("github.com")
	s := &testkit.CredentialStore{}
	if _, err := s.Credential(ctx, key); !errors.Is(err, ports.ErrNoCredential) {
		t.Errorf("empty store: %v", err)
	}
	s.Set(key, "value")
	if secret, err := s.Credential(ctx, key); err != nil || secret.Reveal() != "value" {
		t.Errorf("Credential() = %v, %v", secret, err)
	}
	boom := errors.New("boom")
	s.Errs = map[string]error{key.Account(): boom}
	if _, err := s.Credential(ctx, key); !errors.Is(err, boom) {
		t.Errorf("error = %v", err)
	}
	if len(s.Lookups) != 3 || s.Lookups[0] != key {
		t.Errorf("Lookups = %v", s.Lookups)
	}
}
