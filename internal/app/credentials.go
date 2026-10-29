package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Credentials looks a credential up in several sources in order, such as
// the OS credential store and then the provider's CLI session (spec
// anthill-configuration, "Credentials stay in secure storage"). The
// first source with a value wins. A source that fails does not stop the
// lookup, so a broken credential store still lets the CLI session work.
type Credentials struct {
	sources  []ports.CredentialSource
	redactor *domain.Redactor
}

var _ ports.CredentialSource = (*Credentials)(nil)

// NewCredentials returns a lookup over sources, in order. The redactor
// scrubs the errors it returns; nil means the built-in patterns only.
func NewCredentials(redactor *domain.Redactor, sources ...ports.CredentialSource) *Credentials {
	if redactor == nil {
		redactor, _ = domain.NewRedactor(nil)
	}
	return &Credentials{sources: sources, redactor: redactor}
}

// Credential returns the first value found for key. When no source has
// one, the error wraps ports.ErrNoCredential and says why each source
// failed, with secrets redacted.
func (c *Credentials) Credential(ctx context.Context, key domain.CredentialKey) (domain.Secret, error) {
	var reasons []error
	for _, s := range c.sources {
		secret, err := s.Credential(ctx, key)
		if err == nil && !secret.Empty() {
			return secret, nil
		}
		if err != nil {
			reasons = append(reasons, errors.New(c.redactor.Redact(err.Error())))
		}
	}
	if len(reasons) == 0 {
		return domain.Secret{}, fmt.Errorf("%w for %s", ports.ErrNoCredential, key)
	}
	return domain.Secret{}, fmt.Errorf("%w for %s: %w", ports.ErrNoCredential, key, errors.Join(reasons...))
}
