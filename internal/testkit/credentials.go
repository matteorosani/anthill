package testkit

import (
	"context"
	"fmt"
	"sync"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// CredentialStore is an in-memory ports.CredentialSource, standing in for
// the OS credential store or a CLI session. Values are keyed by the
// key's Account, such as github:github.com. An error set in Errs for an
// account is returned instead of the value.
type CredentialStore struct {
	mu     sync.Mutex
	Values map[string]string
	Errs   map[string]error
	// Lookups records every key asked for, in order.
	Lookups []domain.CredentialKey
}

var _ ports.CredentialSource = (*CredentialStore)(nil)

// Set stores a value.
func (s *CredentialStore) Set(key domain.CredentialKey, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Values == nil {
		s.Values = map[string]string{}
	}
	s.Values[key.Account()] = value
}

// Credential returns the stored value, the stored error, or an error
// wrapping ports.ErrNoCredential.
func (s *CredentialStore) Credential(_ context.Context, key domain.CredentialKey) (domain.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Lookups = append(s.Lookups, key)
	if err := s.Errs[key.Account()]; err != nil {
		return domain.Secret{}, err
	}
	value, ok := s.Values[key.Account()]
	if !ok {
		return domain.Secret{}, fmt.Errorf("%w for %s in the fake store", ports.ErrNoCredential, key)
	}
	return domain.NewSecret(value), nil
}
