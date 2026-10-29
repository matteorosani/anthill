// Package httpkit holds the HTTP pieces the provider adapters share: a
// rate-limit gate timed on ports.Clock, a token cache that also scrubs
// the token from text, and an error that shows a scrubbed message while
// it still wraps the original error. Provider-specific parts, such as
// which headers announce a rate limit or how the token goes into a
// request, stay in each adapter.
package httpkit

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Gate remembers, per key such as a rate-limit resource, until when a
// provider refuses requests. Its zero value is not usable; use NewGate.
type Gate struct {
	clock ports.Clock
	mu    sync.Mutex
	until map[string]time.Time
}

// NewGate returns a gate that reads the time from clock.
func NewGate(clock ports.Clock) *Gate {
	return &Gate{clock: clock, until: map[string]time.Time{}}
}

// Check returns a *ports.RateLimitError while key is paused.
func (g *Gate) Check(key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := g.until[key]; g.clock.Now().Before(until) {
		return &ports.RateLimitError{Reset: until}
	}
	return nil
}

// Pause refuses requests for key until the given time. An earlier time
// than the current pause changes nothing.
func (g *Gate) Pause(key string, until time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until.After(g.until[key]) {
		g.until[key] = until
	}
}

// Token reads a token from a credential source on first use and keeps it
// until Drop, or until it is about to expire when Expiry is set. Set the
// exported fields before the first call and do not change them after.
type Token struct {
	Credentials ports.CredentialSource
	Key         domain.CredentialKey
	// Expiry, when set, says when a token stops being valid, or returns
	// the zero time when it cannot tell. Get then reads a new token
	// RefreshBefore ahead of that time, measured on Clock.
	Expiry        func(domain.Secret) time.Time
	RefreshBefore time.Duration
	Clock         ports.Clock

	mu      sync.Mutex
	token   domain.Secret
	expires time.Time
	// last is the last token handed out, kept after Drop so that text can
	// still be scrubbed of it.
	last domain.Secret
}

// Get returns the cached token, or reads one. Its error comes from the
// credential source, which never puts a value in its errors.
func (t *Token) Get(ctx context.Context) (domain.Secret, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.token.Empty() && !t.expiring() {
		return t.token, nil
	}
	token, err := t.Credentials.Credential(ctx, t.Key)
	if err != nil {
		return domain.Secret{}, err
	}
	t.token, t.last = token, token
	t.expires = time.Time{}
	if t.Expiry != nil {
		t.expires = t.Expiry(token)
	}
	return token, nil
}

// expiring reports whether the cached token expires within
// RefreshBefore. The caller holds mu.
func (t *Token) expiring() bool {
	if t.expires.IsZero() || t.Clock == nil {
		return false
	}
	return !t.Clock.Now().Add(t.RefreshBefore).Before(t.expires)
}

// Drop forgets the cached token, such as after a 401, so that the next
// Get reads a fresh one from the source.
func (t *Token) Drop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = domain.Secret{}
	t.expires = time.Time{}
}

// Scrub replaces the last token handed out in text.
func (t *Token) Scrub(text string) string {
	t.mu.Lock()
	last := t.last
	t.mu.Unlock()
	if last.Empty() {
		return text
	}
	return strings.ReplaceAll(text, last.Reveal(), domain.Redacted)
}

// ScrubbedError shows a scrubbed message but still wraps the original
// error, so that errors.Is finds ports.ErrNoCredential or a context
// error behind a message that no longer contains a secret.
type ScrubbedError struct {
	Msg string
	Err error
}

func (e *ScrubbedError) Error() string { return e.Msg }
func (e *ScrubbedError) Unwrap() error { return e.Err }
