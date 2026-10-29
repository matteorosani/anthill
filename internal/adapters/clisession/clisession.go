// Package clisession borrows the session of a provider's own CLI: the
// GitHub CLI (gh) and the Azure CLI (az). It implements
// ports.CredentialSource.
package clisession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// AzureDevOpsResource is the Microsoft Entra resource id of Azure DevOps,
// for which az issues access tokens.
const AzureDevOpsResource = "499b84ac-1321-427f-aa17-267ca6975798"

// Runner runs a command and returns its standard output and standard
// error. Tests replace it so that no real CLI runs.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// ExecRunner runs the command with os/exec.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Source asks gh or az for a token.
type Source struct {
	run Runner
}

var _ ports.CredentialSource = (*Source)(nil)

// New returns a source that runs the CLIs with run; nil means ExecRunner.
func New(run Runner) *Source {
	if run == nil {
		run = ExecRunner
	}
	return &Source{run: run}
}

// command returns the CLI command that prints the token for key.
func command(key domain.CredentialKey) (string, []string, bool) {
	switch key.Kind {
	case domain.CredentialGitHub:
		return "gh", []string{"auth", "token", "--hostname", key.Name}, true
	case domain.CredentialAzureDevOps:
		return "az", []string{"account", "get-access-token", "--resource", AzureDevOpsResource, "--query", "accessToken", "--output", "tsv"}, true
	}
	return "", nil, false
}

// Credential returns the token the CLI prints for key. When the CLI is not
// installed, not logged in or fails, the error wraps ports.ErrNoCredential
// and carries the CLI's message without any token in it.
func (s *Source) Credential(ctx context.Context, key domain.CredentialKey) (domain.Secret, error) {
	name, args, ok := command(key)
	if !ok {
		return domain.Secret{}, fmt.Errorf("%w for %s: no CLI session provides it", ports.ErrNoCredential, key)
	}
	stdout, stderr, err := s.run(ctx, name, args...)
	token := strings.TrimSpace(string(stdout))
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return domain.Secret{}, fmt.Errorf("%w for %s: %s is not installed", ports.ErrNoCredential, key, name)
		}
		return domain.Secret{}, fmt.Errorf("%w for %s: %s failed: %s", ports.ErrNoCredential, key, name, scrub(stderr, token))
	}
	if token == "" {
		return domain.Secret{}, fmt.Errorf("%w for %s: %s printed no token", ports.ErrNoCredential, key, name)
	}
	return domain.NewSecret(token), nil
}

// scrub turns a CLI's standard error into one line without the token or
// anything a built-in secret pattern matches.
func scrub(stderr []byte, token string) string {
	msg := strings.Join(strings.Fields(string(stderr)), " ")
	if token != "" {
		msg = strings.ReplaceAll(msg, token, domain.Redacted)
	}
	redactor, _ := domain.NewRedactor(nil)
	msg = redactor.Redact(msg)
	if msg == "" {
		return "no message"
	}
	return msg
}
