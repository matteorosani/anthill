package keyring_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	gokeyring "github.com/zalando/go-keyring"

	"github.com/matteorosani/anthill/internal/adapters/keyring"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// The tests use go-keyring's in-memory mock, never the real OS store.

func TestCredentialFromTheStore(t *testing.T) {
	gokeyring.MockInit()
	if err := gokeyring.Set("anthill", "github:github.com", "ghp_value"); err != nil {
		t.Fatal(err)
	}
	secret, err := keyring.Store{}.Credential(context.Background(), domain.GitHubCredential("github.com"))
	if err != nil || secret.Reveal() != "ghp_value" {
		t.Errorf("Credential() = %v, %v", secret, err)
	}
}

func TestMissingCredential(t *testing.T) {
	gokeyring.MockInit()
	_, err := keyring.Store{}.Credential(context.Background(), domain.OTLPHeadersCredential())
	if !errors.Is(err, ports.ErrNoCredential) {
		t.Errorf("err = %v, want ErrNoCredential", err)
	}
}

func TestStoreError(t *testing.T) {
	gokeyring.MockInitWithError(errors.New("secret service unavailable"))
	_, err := keyring.Store{}.Credential(context.Background(), domain.AzureDevOpsCredential("contoso"))
	if err == nil || errors.Is(err, ports.ErrNoCredential) {
		t.Fatalf("err = %v, want a store error", err)
	}
	if !strings.Contains(err.Error(), "azure-devops:contoso") || !strings.Contains(err.Error(), "secret service unavailable") {
		t.Errorf("err = %q", err)
	}
}
