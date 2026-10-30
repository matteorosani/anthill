package httpkit_test

import (
	"errors"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/httpkit"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

var start = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func TestGatePausesUntilReset(t *testing.T) {
	clock := testkit.NewClock(start)
	g := httpkit.NewGate(clock)
	if err := g.Check("core"); err != nil {
		t.Fatalf("fresh gate: %v", err)
	}
	reset := start.Add(time.Minute)
	g.Pause("core", reset)
	g.Pause("core", start.Add(time.Second)) // earlier: ignored
	err := g.Check("core")
	rl, ok := errors.AsType[*ports.RateLimitError](err)
	if !ok || !rl.Reset.Equal(reset) {
		t.Fatalf("Check = %v, want reset %v", err, reset)
	}
	if err := g.Check("graphql"); err != nil {
		t.Errorf("other key paused: %v", err)
	}
	clock.Set(reset)
	if err := g.Check("core"); err != nil {
		t.Errorf("after reset: %v", err)
	}
}

func TestTokenCachesDropsAndScrubs(t *testing.T) {
	key := domain.GitHubCredential("github.com")
	store := &testkit.CredentialStore{}
	store.Set(key, "first")
	tok := &httpkit.Token{Credentials: store, Key: key}
	for range 2 {
		if s, err := tok.Get(t.Context()); err != nil || s.Reveal() != "first" {
			t.Fatalf("Get = %q, %v", s.Reveal(), err)
		}
	}
	if len(store.Lookups) != 1 {
		t.Errorf("lookups = %d, want 1 (cached)", len(store.Lookups))
	}
	store.Set(key, "second")
	tok.Drop()
	if got := tok.Scrub("x first y"); got != "x "+domain.Redacted+" y" {
		t.Errorf("Scrub after Drop = %q", got)
	}
	if s, _ := tok.Get(t.Context()); s.Reveal() != "second" {
		t.Errorf("after Drop = %q, want second", s.Reveal())
	}
}

func TestTokenRefreshesBeforeExpiry(t *testing.T) {
	key := domain.AzureDevOpsCredential("contoso")
	store := &testkit.CredentialStore{}
	store.Set(key, "a")
	clock := testkit.NewClock(start)
	tok := &httpkit.Token{
		Credentials:   store,
		Key:           key,
		Clock:         clock,
		RefreshBefore: 2 * time.Minute,
		Expiry:        func(domain.Secret) time.Time { return start.Add(10 * time.Minute) },
	}
	_, _ = tok.Get(t.Context())
	clock.Advance(7 * time.Minute)
	_, _ = tok.Get(t.Context())
	if len(store.Lookups) != 1 {
		t.Fatalf("lookups = %d before the refresh window, want 1", len(store.Lookups))
	}
	clock.Advance(time.Minute) // 8 minutes: 2 minutes before expiry
	_, _ = tok.Get(t.Context())
	if len(store.Lookups) != 2 {
		t.Errorf("lookups = %d in the refresh window, want 2", len(store.Lookups))
	}
}

func TestScrubbedErrorWraps(t *testing.T) {
	err := &httpkit.ScrubbedError{Msg: "shown", Err: ports.ErrNoCredential}
	if err.Error() != "shown" || !errors.Is(err, ports.ErrNoCredential) {
		t.Errorf("ScrubbedError = %q, Is = %v", err, errors.Is(err, ports.ErrNoCredential))
	}
}
