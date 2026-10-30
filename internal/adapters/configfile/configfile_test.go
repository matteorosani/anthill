package configfile_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/configfile"
	"github.com/matteorosani/anthill/internal/domain"
)

// messages returns the message of every *domain.ConfigError joined in err.
func messages(err error) []string {
	var out []string
	var walk func(error)
	walk = func(err error) {
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range joined.Unwrap() {
				walk(e)
			}
			return
		}
		var ce *domain.ConfigError
		if errors.As(err, &ce) {
			out = append(out, ce.Message)
		}
	}
	if err != nil {
		walk(err)
	}
	return out
}

func parseRepositoryFile(t *testing.T, content string) (*domain.SettingValues, []string) {
	t.Helper()
	v, err := configfile.ParseRepositoryFile(".anthill.yml", []byte(content))
	return v, messages(err)
}

// Scenario "Credentials in repository file".
func TestCredentialsInRepositoryFile(t *testing.T) {
	v, got := parseRepositoryFile(t, "budget: 45\ntoken: abc\n")
	want := []string{`forbidden key "token": credentials belong in the OS credential store`}
	if v != nil || !slices.Equal(got, want) {
		t.Errorf("got %v, %q; want rejection %q", v, got, want)
	}
}

// Scenario "Telemetry endpoint in repository file".
func TestTelemetryEndpointInRepositoryFile(t *testing.T) {
	_, got := parseRepositoryFile(t, "otlp-endpoint: https://collector:4318\n")
	want := []string{`forbidden key "otlp-endpoint": telemetry settings belong in the local configuration`}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRepositoryFileRejections(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"secret patterns", "secret-patterns: ['ACME-[0-9]+']\n", []string{`forbidden key "secret-patterns": secret patterns belong in the local configuration`}},
		{"machine path", "path: /src/anthill\n", []string{`forbidden key "path": machine paths belong in the local configuration`}},
		{"machine setting", "capacity: 4\n", []string{`forbidden key "capacity": machine settings belong in the local configuration`}},
		{"nested credential", "extension-allowance:\n  minutes: 5\n  api_key: x\n", []string{`forbidden key "api_key": credentials belong in the OS credential store`}},
		{"unknown key", "budgte: 45\n", []string{`unknown key "budgte"`}},
		{"forbidden before unknown", "foo: 1\ngithub-token: x\n", []string{`unknown key "foo"`, `forbidden key "github-token": credentials belong in the OS credential store`}},
		{"value shaped like a secret", "verification-command: \"curl -H 'Authorization: Bearer abcdefghijklmnopqrstuvwxyz' x\"\n", []string{`key "verification-command" holds a value that looks like a secret`}},
		{"not a mapping", "- budget\n", []string{"the file must be a mapping of keys to values"}},
		{"wrong type", "budget: abc\n", []string{"line 1: cannot unmarshal !!str `abc` into int"}},
		{"percent sign", "warning-threshold: 80%\n", []string{"line 1: cannot unmarshal !!str `80%` into int"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, got := parseRepositoryFile(t, tt.content)
			if v != nil || !slices.Equal(got, tt.want) {
				t.Errorf("got %v, %q; want %q", v, got, tt.want)
			}
		})
	}
}

func TestRepositoryFileInvalidYAML(t *testing.T) {
	_, err := configfile.ParseRepositoryFile(".anthill.yml", []byte("budget: [45\n"))
	var ce *domain.ConfigError
	if !errors.As(err, &ce) || ce.File != ".anthill.yml" {
		t.Errorf("err = %v, want a ConfigError for .anthill.yml", err)
	}
}

func TestRepositoryFileAcceptsANormalVerificationCommand(t *testing.T) {
	v, got := parseRepositoryFile(t, "verification-command: go test ./...\n")
	if got != nil || v.VerificationCommand == nil || *v.VerificationCommand != "go test ./..." {
		t.Errorf("got %v, %q", v, got)
	}
}

func TestRepositoryFileAllSharedSettings(t *testing.T) {
	v, got := parseRepositoryFile(t, `
opt-in-label: ai
hold-label: ai:hold
claim-label: ai:claimed
budget: 45
follow-up-budget: 40
max-base-budget: 120
warning-threshold: 75
extension-allowance: {minutes: 15, times: 2}
ceiling: 2
branch-prefix: bot
base-branch: develop
protected-branches: [main, "release/*"]
shepherd-scope: labelled
review-runner: codex
round-limit: 2
auto-follow-up: false
follow-up-limit: 3
auto-resume: true
verification-command: make test
require-acceptance-criteria: true
agent-instructions: docs/AGENTS.md
`)
	if got != nil {
		t.Fatalf("errors: %q", got)
	}
	e := domain.ConfigLayers{RepositoryFile: v}.Resolve()
	for _, row := range e.Inspect() {
		switch row.Key {
		case domain.KeyRunner, domain.KeyCapacity, domain.KeyReviewing, domain.KeyPollInterval, domain.KeyLease,
			domain.KeyGracePeriod, domain.KeyExtensionWait, domain.KeyQuestionStaleness, domain.KeyOTLPEndpoint, domain.KeySecretPatterns, domain.KeyWorktreeRoot, domain.KeyAzureDevOpsProjects, domain.KeyProviderPollIntervals,
			domain.KeyNotifications:
			continue
		}
		if row.Source != domain.LayerRepositoryFile {
			t.Errorf("%s comes from %q, want .anthill.yml", row.Key, row.Source)
		}
	}
	if e.ExtensionAllowance != (domain.ExtensionAllowance{MaxMinutes: 15, Times: 2}) || !slices.Equal(e.ProtectedBranches, []string{"main", "release/*"}) {
		t.Errorf("allowance %+v, protected %v", e.ExtensionAllowance, e.ProtectedBranches)
	}
}

func TestEmptyRepositoryFile(t *testing.T) {
	v, got := parseRepositoryFile(t, "# nothing yet\n")
	if got != nil || v == nil || !reflect.DeepEqual(*v, domain.SettingValues{}) {
		t.Errorf("got %+v, %q", v, got)
	}
}

// Scenario "OTLP headers in a configuration file".
func TestOTLPHeadersInAConfigurationFile(t *testing.T) {
	want := "OTLP headers belong in the OS credential store, not in a configuration file"
	t.Run("global", func(t *testing.T) {
		_, err := configfile.ParseLocal("config.yml", []byte("otlp-endpoint: https://c:4318\notlp-headers: {authorization: x}\n"))
		if got := messages(err); !slices.Equal(got, []string{want}) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("repository entry", func(t *testing.T) {
		cfg, err := configfile.ParseLocal("config.yml", []byte("repositories:\n  - provider: github\n    remote: a/b\n    path: /a\n    otlp-headers: x\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := messages(cfg.Repositories[0].Err); !slices.Equal(got, []string{want}) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestParseLocal(t *testing.T) {
	cfg, err := configfile.ParseLocal("config.yml", []byte(`
runner: codex
capacity: 3
reviewing: false
poll-interval: 90s
lease: 20m
grace-period: 5m
extension-wait: 2m
question-staleness: 48h
otlp-endpoint: http://localhost:4318
secret-patterns: ['ACME-[0-9]{8}']
budget: 30
azure-devops-projects:
  - project: contoso/shop
    default-repository: web
provider-poll-intervals:
  GitHub: 1m
  azure-devops/contoso: 5m
notifications:
  claim-lost: false
  proposal: true
repositories:
  - provider: github
    remote: matteorosani/anthill
    path: /src/anthill
    runner: claude-code
    capacity: 1
    reviewing: false
    budget: 50
  - provider: azure-devops
    remote: contoso/shop/web
    path: /src/web
`))
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.Global
	if *g.Runner != domain.RunnerCodex || *g.Capacity != 3 || *g.Reviewing || *g.PollInterval != 90*time.Second ||
		*g.Lease != 20*time.Minute || *g.GracePeriod != 5*time.Minute || *g.ExtensionWait != 2*time.Minute ||
		*g.QuestionStaleness != 48*time.Hour || *g.OTLPEndpoint != "http://localhost:4318" ||
		!slices.Equal(g.SecretPatterns, []string{"ACME-[0-9]{8}"}) || *g.Budget != 30 ||
		!slices.Equal(g.AzureDevOpsProjects, []domain.AzureDevOpsProject{{Project: "contoso/shop", DefaultRepository: "web"}}) ||
		!maps.Equal(g.ProviderPollIntervals, map[string]time.Duration{"GitHub": time.Minute, "azure-devops/contoso": 5 * time.Minute}) ||
		!maps.Equal(g.Notifications, map[string]bool{"claim-lost": false, "proposal": true}) {
		t.Errorf("global = %+v", g)
	}
	if len(cfg.Repositories) != 2 {
		t.Fatalf("repositories = %+v", cfg.Repositories)
	}
	first := cfg.Repositories[0]
	if first.Err != nil || first.Entry.Remote != "matteorosani/anthill" || first.Entry.Path != "/src/anthill" ||
		*first.Entry.Settings.Runner != domain.RunnerClaudeCode || *first.Entry.Settings.Budget != 50 {
		t.Errorf("first entry = %+v, %v", first.Entry, first.Err)
	}
	if second := cfg.Repositories[1]; second.Err != nil || second.Entry.Provider != "azure-devops" {
		t.Errorf("second entry = %+v, %v", second.Entry, second.Err)
	}
}

func TestParseLocalBadEntryKeepsTheOthers(t *testing.T) {
	cfg, err := configfile.ParseLocal("config.yml", []byte(`
repositories:
  - provider: github
    remote: a/good
    path: /a
  - provider: github
    remote: a/bad
    path: /b
    lease: 2m
    budget: lots
  - just a string
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repositories[0].Err != nil {
		t.Errorf("good entry: %v", cfg.Repositories[0].Err)
	}
	bad := cfg.Repositories[1]
	if got := messages(bad.Err); !slices.Equal(got, []string{`key "lease" can be set only in the global configuration`}) {
		t.Errorf("bad entry messages = %q", got)
	}
	if bad.Entry.Remote != "a/bad" {
		t.Errorf("bad entry lost its identity: %+v", bad.Entry)
	}
	if !strings.HasPrefix(bad.Err.Error(), "config.yml: repositories[1]: ") {
		t.Errorf("bad entry error = %q", bad.Err)
	}
	if got := messages(cfg.Repositories[2].Err); !slices.Equal(got, []string{"a repository entry must be a mapping of keys to values"}) {
		t.Errorf("string entry messages = %q", got)
	}
}

func TestParseLocalGlobalRejections(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"credential", "github-token: x\n", []string{`forbidden key "github-token": credentials belong in the OS credential store`}},
		{"entry key at the top", "path: /src\n", []string{`key "path" can be set only in the repository entries`}},
		{"unknown", "colour: blue\n", []string{`unknown key "colour"`}},
		{"repositories not a list", "repositories: {a: b}\n", []string{"repositories must be a list of entries"}},
		{"secret value", "otlp-endpoint: https://user:hunter2hunter2@collector\n", []string{`key "otlp-endpoint" holds a value that looks like a secret`}},
		{"wrong type", "lease: 15\n", []string{"line 1: cannot unmarshal !!int `15` into time.Duration"}},
		{"notification not a bool", "notifications:\n  claim-lost: sometimes\n", []string{"line 2: cannot unmarshal !!str `sometimes` into bool"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := configfile.ParseLocal("config.yml", []byte(tt.content))
			if got := messages(err); !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSourceReadsFiles(t *testing.T) {
	dir := t.TempDir()
	clone := filepath.Join(dir, "clone")
	if err := os.Mkdir(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yml")
	src := configfile.New(path)
	ctx := context.Background()

	cfg, err := src.Local(ctx)
	if err != nil || cfg.File != path || len(cfg.Repositories) != 0 {
		t.Errorf("missing file: %+v, %v; want an empty configuration", cfg, err)
	}
	entry := domain.RepositoryEntry{Provider: "github", Remote: "a/b", Path: clone}
	if v, err := src.RepositoryFile(ctx, entry); v != nil || err != nil {
		t.Errorf("missing .anthill.yml: %v, %v; want nil, nil", v, err)
	}

	if err := os.WriteFile(path, []byte("capacity: 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".anthill.yml"), []byte("budget: 45\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := src.Local(ctx); err != nil || *cfg.Global.Capacity != 4 {
		t.Errorf("Local() = %+v, %v", cfg, err)
	}
	if v, err := src.RepositoryFile(ctx, entry); err != nil || *v.Budget != 45 {
		t.Errorf("RepositoryFile() = %+v, %v", v, err)
	}

	if err := os.WriteFile(filepath.Join(clone, ".anthill.yml"), []byte("token: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = src.RepositoryFile(ctx, entry)
	var ce *domain.ConfigError
	if !errors.As(err, &ce) || ce.File != filepath.Join(clone, ".anthill.yml") {
		t.Errorf("err = %v, want a ConfigError naming the file", err)
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := configfile.DefaultPath()
	if err != nil {
		t.Skipf("no user configuration directory: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("anthill", "config.yml")) || !filepath.IsAbs(path) {
		t.Errorf("DefaultPath() = %q", path)
	}
}

// Scenario "Uncommitted edit is used": the file is read from the clone's
// working folder, not from a commit.
func TestUncommittedEditIsUsed(t *testing.T) {
	clone := t.TempDir()
	file := filepath.Join(clone, ".anthill.yml")
	if err := os.WriteFile(file, []byte("budget: 45\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := configfile.New(filepath.Join(t.TempDir(), "config.yml"))
	entry := domain.RepositoryEntry{Provider: "github", Remote: "a/b", Path: clone}
	if err := os.WriteFile(file, []byte("budget: 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := src.RepositoryFile(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	e := domain.ConfigLayers{RepositoryFile: v, Global: &domain.SettingValues{}}.Resolve()
	if e.Budget != 30*time.Minute || e.Source(domain.KeyBudget) != domain.LayerRepositoryFile {
		t.Errorf("budget %v from %q, want 30m from .anthill.yml", e.Budget, e.Source(domain.KeyBudget))
	}
}

// worktree-root is a machine path: only the global configuration sets it.
func TestWorktreeRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wt")
	cfg, err := configfile.ParseLocal("config.yml", []byte("worktree-root: '"+root+"'\n"))
	if err != nil || cfg.Global.WorktreeRoot == nil || *cfg.Global.WorktreeRoot != root {
		t.Fatalf("ParseLocal() = %+v, %v", cfg.Global, err)
	}
	if e := (domain.ConfigLayers{Global: &cfg.Global}).Resolve(); e.WorktreeRoot != root {
		t.Errorf("effective worktree root = %q", e.WorktreeRoot)
	}
	cfg, err = configfile.ParseLocal("config.yml", []byte("repositories:\n  - provider: github\n    remote: a/b\n    path: /a\n    worktree-root: /wt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(cfg.Repositories[0].Err); !slices.Equal(got, []string{`key "worktree-root" can be set only in the global configuration`}) {
		t.Errorf("entry messages = %q", got)
	}
	_, got := parseRepositoryFile(t, "worktree-root: /wt\n")
	if !slices.Equal(got, []string{`forbidden key "worktree-root": machine paths belong in the local configuration`}) {
		t.Errorf("repository file messages = %q", got)
	}
}
