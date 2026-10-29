// Package keyring reads credentials from the OS credential store: the
// macOS Keychain, the Windows Credential Manager, or the Secret Service
// on Linux, through zalando/go-keyring. It implements
// ports.CredentialSource.
package keyring

import (
	"context"
	"errors"
	"fmt"

	gokeyring "github.com/zalando/go-keyring"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Service is the service name of every Anthill entry in the credential
// store. The account is the key's Account, such as github:github.com.
const Service = "anthill"

// Store reads Anthill's entries in the OS credential store.
type Store struct{}

var _ ports.CredentialSource = Store{}

// Credential returns the stored value for key. A missing entry wraps
// ports.ErrNoCredential.
func (Store) Credential(_ context.Context, key domain.CredentialKey) (domain.Secret, error) {
	value, err := gokeyring.Get(Service, key.Account())
	if errors.Is(err, gokeyring.ErrNotFound) || (err == nil && value == "") {
		return domain.Secret{}, fmt.Errorf("%w for %s in the OS credential store", ports.ErrNoCredential, key)
	}
	if err != nil {
		// go-keyring errors describe the store, never the value.
		return domain.Secret{}, fmt.Errorf("reading %s from the OS credential store: %w", key, err)
	}
	return domain.NewSecret(value), nil
}
