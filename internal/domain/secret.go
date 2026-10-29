package domain

import (
	"fmt"
	"log/slog"
)

// Secret holds a credential value, such as a provider token or the OTLP
// headers. Every way of printing, logging or encoding it shows
// [REDACTED], so a secret that ends up in a log line, an error or JSON by
// mistake does not leak. Only Reveal returns the value; call it at the
// last moment, where the value is handed to the provider.
type Secret struct {
	value string
}

// NewSecret wraps a credential value.
func NewSecret(value string) Secret {
	return Secret{value: value}
}

// Reveal returns the credential value.
func (s Secret) Reveal() string {
	return s.value
}

// Empty reports whether the secret holds no value.
func (s Secret) Empty() bool {
	return s.value == ""
}

// String implements fmt.Stringer, used by %s and %v.
func (s Secret) String() string {
	return Redacted
}

// GoString implements fmt.GoStringer, used by %#v.
func (s Secret) GoString() string {
	return Redacted
}

// Format implements fmt.Formatter, so that every verb, %q and %x
// included, prints [REDACTED].
func (s Secret) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(Redacted))
}

// MarshalText implements encoding.TextMarshaler, used by JSON, YAML and
// most loggers.
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(Redacted), nil
}

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value {
	return slog.StringValue(Redacted)
}

// CredentialKind tells what a credential is for.
type CredentialKind string

// The credential kinds.
const (
	CredentialGitHub      CredentialKind = "github"
	CredentialAzureDevOps CredentialKind = "azure-devops"
	CredentialOTLPHeaders CredentialKind = "otlp-headers"
)

// CredentialKey names one credential. Name is the GitHub host or the
// Azure DevOps organization, and is empty for the OTLP headers.
type CredentialKey struct {
	Kind CredentialKind
	Name string
}

// GitHubCredential names the token for a GitHub host, such as github.com.
func GitHubCredential(host string) CredentialKey {
	return CredentialKey{Kind: CredentialGitHub, Name: host}
}

// AzureDevOpsCredential names the token for an Azure DevOps organization.
func AzureDevOpsCredential(organization string) CredentialKey {
	return CredentialKey{Kind: CredentialAzureDevOps, Name: organization}
}

// OTLPHeadersCredential names the OTLP headers, stored in the
// OTEL_EXPORTER_OTLP_HEADERS format k1=v1,k2=v2.
func OTLPHeadersCredential() CredentialKey {
	return CredentialKey{Kind: CredentialOTLPHeaders}
}

// Account is the key's account name in the OS credential store:
// github:github.com, azure-devops:contoso or otlp-headers.
func (k CredentialKey) Account() string {
	if k.Name == "" {
		return string(k.Kind)
	}
	return string(k.Kind) + ":" + k.Name
}

// String prints the key, never a value.
func (k CredentialKey) String() string {
	return k.Account()
}
