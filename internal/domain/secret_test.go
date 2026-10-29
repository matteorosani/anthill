package domain_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

const tokenValue = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

func TestSecretNeverPrintsItsValue(t *testing.T) {
	s := domain.NewSecret(tokenValue)
	if s.Reveal() != tokenValue {
		t.Fatalf("Reveal() = %q", s.Reveal())
	}
	printed := []string{
		fmt.Sprint(s),
		fmt.Sprintf("%s %v %+v %#v %q %x %d", s, s, s, s, s, s, s),
		fmt.Sprintf("%v", struct{ Token domain.Secret }{s}),
		fmt.Sprintf("%+v", &struct{ Token domain.Secret }{s}),
		fmt.Errorf("request with %v failed: %w", s, errors.New("401")).Error(),
	}
	data, err := json.Marshal(map[string]domain.Secret{"token": s})
	if err != nil {
		t.Fatal(err)
	}
	printed = append(printed, string(data))

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	logger.Info("auth", "token", s, "nested", struct{ T domain.Secret }{s})
	text := slog.New(slog.NewTextHandler(&logs, nil))
	text.Error("auth failed", "token", s)
	printed = append(printed, logs.String())

	for _, p := range printed {
		if strings.Contains(p, tokenValue) || strings.Contains(p, "abcdefghij") {
			t.Errorf("value leaked: %s", p)
		}
		if !strings.Contains(p, domain.Redacted) {
			t.Errorf("no %s in %s", domain.Redacted, p)
		}
	}
}

func TestCredentialKeyAccount(t *testing.T) {
	tests := []struct {
		key  domain.CredentialKey
		want string
	}{
		{domain.GitHubCredential("github.com"), "github:github.com"},
		{domain.AzureDevOpsCredential("contoso"), "azure-devops:contoso"},
		{domain.OTLPHeadersCredential(), "otlp-headers"},
	}
	for _, tt := range tests {
		if got := tt.key.Account(); got != tt.want || tt.key.String() != tt.want {
			t.Errorf("Account() = %q, want %q", got, tt.want)
		}
	}
}
