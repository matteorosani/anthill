package clisession_test

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/clisession"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

const token = "gho_abcdefghijklmnopqrstuvwxyz0123456789"

// fakeRunner records the command and answers like the CLI would. No real
// CLI runs.
type fakeRunner struct {
	stdout, stderr string
	err            error
	calls          [][]string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return []byte(f.stdout), []byte(f.stderr), f.err
}

func TestGitHubCLISession(t *testing.T) {
	f := &fakeRunner{stdout: token + "\n"}
	secret, err := clisession.New(f.run).Credential(context.Background(), domain.GitHubCredential("github.example.com"))
	if err != nil || secret.Reveal() != token {
		t.Fatalf("Credential() = %v, %v", secret, err)
	}
	want := []string{"gh", "auth", "token", "--hostname", "github.example.com"}
	if len(f.calls) != 1 || !slices.Equal(f.calls[0], want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

func TestAzureCLISession(t *testing.T) {
	f := &fakeRunner{stdout: "eyJ0eXAi.token\n"}
	secret, err := clisession.New(f.run).Credential(context.Background(), domain.AzureDevOpsCredential("contoso"))
	if err != nil || secret.Reveal() != "eyJ0eXAi.token" {
		t.Fatalf("Credential() = %v, %v", secret, err)
	}
	want := []string{"az", "account", "get-access-token", "--resource", clisession.AzureDevOpsResource, "--query", "accessToken", "--output", "tsv"}
	if !slices.Equal(f.calls[0], want) {
		t.Errorf("call = %q, want %q", f.calls[0], want)
	}
}

func TestCLISessionFailures(t *testing.T) {
	tests := []struct {
		name   string
		runner *fakeRunner
		key    domain.CredentialKey
		want   string
	}{
		{"not logged in", &fakeRunner{stderr: "no oauth token found for github.com\n", err: errors.New("exit status 1")}, domain.GitHubCredential("github.com"),
			"no credential for github:github.com: gh failed: no oauth token found for github.com"},
		{"not installed", &fakeRunner{err: &exec.Error{Name: "az", Err: exec.ErrNotFound}}, domain.AzureDevOpsCredential("contoso"),
			"no credential for azure-devops:contoso: az is not installed"},
		{"empty output", &fakeRunner{stdout: "\n"}, domain.GitHubCredential("github.com"),
			"no credential for github:github.com: gh printed no token"},
		{"no CLI for the key", &fakeRunner{}, domain.OTLPHeadersCredential(),
			"no credential for otlp-headers: no CLI session provides it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := clisession.New(tt.runner.run).Credential(context.Background(), tt.key)
			if !errors.Is(err, ports.ErrNoCredential) || err.Error() != tt.want {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// A CLI that fails after printing the token, and repeats it and another
// secret on standard error, leaves no value in the error.
func TestCLISessionErrorHasNoCredentialValue(t *testing.T) {
	f := &fakeRunner{
		stdout: "s3cr3t-plain-value\n",
		stderr: "warning: token s3cr3t-plain-value expired; also ghp_abcdefghijklmnopqrstuvwxyz0123456789\n",
		err:    errors.New("exit status 1"),
	}
	_, err := clisession.New(f.run).Credential(context.Background(), domain.GitHubCredential("github.com"))
	if err == nil {
		t.Fatal("no error")
	}
	for _, value := range []string{"s3cr3t-plain-value", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"} {
		if strings.Contains(err.Error(), value) {
			t.Errorf("%q leaked in %q", value, err)
		}
	}
}

func TestExecRunnerMissingCommand(t *testing.T) {
	_, _, err := clisession.ExecRunner(context.Background(), "anthill-no-such-command-for-tests")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want exec.ErrNotFound", err)
	}
}
