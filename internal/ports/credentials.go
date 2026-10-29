package ports

import (
	"context"
	"errors"

	"github.com/matteorosani/anthill/internal/domain"
)

// ErrNoCredential is returned, wrapped, when a credential source has no
// value for a key. Check for it with errors.Is.
var ErrNoCredential = errors.New("no credential")

// CredentialSource gives credentials, such as the OS credential store or
// a provider CLI session. Errors never contain a credential value.
type CredentialSource interface {
	Credential(ctx context.Context, key domain.CredentialKey) (domain.Secret, error)
}
