package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

const (
	storedToken  = "ghp_storedstoredstoredstoredstored123456"
	sessionToken = "gho_sessionsessionsessionsession12345678"
)

var github = domain.GitHubCredential("github.com")

func TestCredentialStoreComesFirst(t *testing.T) {
	store, session := &testkit.CredentialStore{}, &testkit.CredentialStore{}
	store.Set(github, storedToken)
	session.Set(github, sessionToken)
	secret, err := app.NewCredentials(nil, store, session).Credential(context.Background(), github)
	if err != nil || secret.Reveal() != storedToken {
		t.Errorf("Credential() = %v, %v; want the stored token", secret, err)
	}
	if len(session.Lookups) != 0 {
		t.Error("the CLI session was asked although the store had a token")
	}
}

// Scenario "Using an existing gh login".
func TestUsingAnExistingGhLogin(t *testing.T) {
	store, session := &testkit.CredentialStore{}, &testkit.CredentialStore{}
	session.Set(github, sessionToken)
	secret, err := app.NewCredentials(nil, store, session).Credential(context.Background(), github)
	if err != nil || secret.Reveal() != sessionToken {
		t.Errorf("Credential() = %v, %v; want the gh session token", secret, err)
	}
}

func TestBrokenStoreFallsBackToTheSession(t *testing.T) {
	store := &testkit.CredentialStore{Errs: map[string]error{github.Account(): errors.New("secret service unavailable")}}
	session := &testkit.CredentialStore{}
	session.Set(github, sessionToken)
	secret, err := app.NewCredentials(nil, store, session).Credential(context.Background(), github)
	if err != nil || secret.Reveal() != sessionToken {
		t.Errorf("Credential() = %v, %v", secret, err)
	}
}

func TestNoCredential(t *testing.T) {
	_, err := app.NewCredentials(nil, &testkit.CredentialStore{}, &testkit.CredentialStore{}).Credential(context.Background(), github)
	if !errors.Is(err, ports.ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
	if !strings.Contains(err.Error(), "github:github.com") {
		t.Errorf("err = %q does not name the key", err)
	}
}

// Scenario "Token never logged": a source whose error carries a token,
// and a failed provider request logged with the credential, leave no
// credential value in errors or logs.
func TestTokenNeverLogged(t *testing.T) {
	leaky := &testkit.CredentialStore{Errs: map[string]error{
		github.Account(): fmt.Errorf("authentication failed for %s", storedToken),
	}}
	redactor, err := domain.NewRedactor([]string{`internal-[a-z]{6}`})
	if err != nil {
		t.Fatal(err)
	}
	other := &testkit.CredentialStore{Errs: map[string]error{github.Account(): errors.New("bad key internal-abcdef")}}
	_, lookupErr := app.NewCredentials(redactor, leaky, other).Credential(context.Background(), github)
	if lookupErr == nil {
		t.Fatal("no error")
	}

	session := &testkit.CredentialStore{}
	session.Set(github, sessionToken)
	secret, err := app.NewCredentials(redactor, session).Credential(context.Background(), github)
	if err != nil {
		t.Fatal(err)
	}
	// A provider request failing with an authentication error, as an
	// adapter would report it.
	requestErr := fmt.Errorf("GET https://api.github.com/user with token %v: 401 Bad credentials", secret)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	logger.Error("credential lookup failed", "err", lookupErr)
	logger.Error("provider request failed", "err", requestErr, "token", secret)

	for _, text := range []string{lookupErr.Error(), requestErr.Error(), logs.String()} {
		for _, value := range []string{storedToken, sessionToken, "internal-abcdef"} {
			if strings.Contains(text, value) {
				t.Errorf("credential value %q found in %q", value, text)
			}
		}
	}
	if !strings.Contains(lookupErr.Error(), "authentication failed for [REDACTED]") {
		t.Errorf("lookup error lost its reason: %q", lookupErr)
	}
}
