package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func ptr[T any](v T) *T { return &v }

// absPath returns an absolute path on every OS.
func absPath(parts ...string) string {
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func entry(remote string) domain.RepositoryEntry {
	return domain.RepositoryEntry{Provider: "github", Remote: remote, Path: absPath("src", remote)}
}

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
			return
		}
		if inner := errors.Unwrap(err); inner != nil {
			walk(inner)
		}
	}
	if err != nil {
		walk(err)
	}
	return out
}

func source(global domain.SettingValues, entries ...domain.RepositoryEntry) *testkit.ConfigSource {
	src := &testkit.ConfigSource{
		Config:          ports.LocalConfig{File: "config.yml", Global: global},
		RepositoryFiles: map[string]*domain.SettingValues{},
		RepositoryErrs:  map[string]error{},
	}
	for _, e := range entries {
		src.Config.Repositories = append(src.Config.Repositories, ports.LocalEntry{Entry: e})
	}
	return src
}

// Scenario "Warning threshold out of range".
func TestWarningThresholdOutOfRange(t *testing.T) {
	cfg := app.NewConfiguration(source(domain.SettingValues{WarningThreshold: ptr(120)}, entry("a/b")))
	snap, err := cfg.Load(context.Background())
	if snap != nil || err == nil {
		t.Fatalf("Load() = %v, %v; want the daemon to refuse to start", snap, err)
	}
	if got := messages(err); !slices.Equal(got, []string{"warning threshold must be between 1 and 99 percent"}) {
		t.Errorf("messages = %q", got)
	}
	if cfg.Current() != nil {
		t.Error("an invalid configuration became current")
	}
}

func TestInvalidGlobalConfigurationPreventsStartup(t *testing.T) {
	tests := []struct {
		name string
		src  *testkit.ConfigSource
		want string
	}{
		{"unreadable", func() *testkit.ConfigSource {
			s := source(domain.SettingValues{})
			s.ConfigErr = &domain.ConfigError{File: "config.yml", Message: "unknown key \"colour\""}
			return s
		}(), `unknown key "colour"`},
		{"bad secret pattern", source(domain.SettingValues{SecretPatterns: []string{"("}}), "secret pattern \"(\" is not valid: error parsing regexp: missing closing ): `(`"},
		{"budget over maximum", source(domain.SettingValues{Budget: ptr(200)}), domain.MessageBudgetOverMaximum},
		{"duplicate entry", source(domain.SettingValues{}, entry("a/b"), entry("a/b")), "repository github a/b is listed more than once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := app.NewConfiguration(tt.src).Load(context.Background())
			if got := messages(err); !slices.Equal(got, []string{tt.want}) {
				t.Errorf("messages = %q, want %q", got, tt.want)
			}
			if !strings.HasPrefix(err.Error(), "invalid global configuration: ") {
				t.Errorf("error = %q", err)
			}
		})
	}
}

// One bad repository configuration leaves the other repositories running.
func TestOneBadRepositoryLeavesTheOthersRunning(t *testing.T) {
	good, badFile, badEntry, badRead := entry("a/good"), entry("a/bad-file"), entry("a/bad-entry"), entry("a/bad-read")
	badEntry.Settings.Runner = ptr(domain.Runner("gpt"))
	src := source(domain.SettingValues{}, good, badFile, badEntry, badRead)
	src.Config.Repositories = append(src.Config.Repositories, ports.LocalEntry{
		Entry: domain.RepositoryEntry{Provider: "github", Remote: "a/unparsed"},
		Err:   &domain.ConfigError{File: "config.yml", Key: "repositories[4]", Message: `unknown key "colour"`},
	})
	src.RepositoryFiles[badFile.Path] = &domain.SettingValues{WarningThreshold: ptr(0)}
	src.RepositoryErrs[badRead.Path] = &domain.ConfigError{File: ".anthill.yml", Message: `forbidden key "token": credentials belong in the OS credential store`}

	snap, err := app.NewConfiguration(src).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() = %v; a bad repository must not stop startup", err)
	}
	want := map[string]string{
		"a/good":      "",
		"a/bad-file":  domain.MessageWarningThreshold,
		"a/bad-entry": `runner must be "claude-code", "codex" or "opencode"`,
		"a/bad-read":  `forbidden key "token": credentials belong in the OS credential store`,
		"a/unparsed":  `unknown key "colour"`,
	}
	for _, rc := range snap.Repositories {
		msg := strings.Join(messages(rc.Err), "; ")
		if msg != want[rc.Entry.Remote] || rc.Enabled() != (msg == "") {
			t.Errorf("%s: enabled %v, error %q; want error %q", rc.Entry.Remote, rc.Enabled(), msg, want[rc.Entry.Remote])
		}
	}
	if len(snap.Repositories) != len(want) {
		t.Errorf("repositories = %d, want %d", len(snap.Repositories), len(want))
	}
	if rc, ok := snap.Repository(domain.Repository{Provider: domain.GitHub, Org: "a", Name: "bad-file"}); !ok || rc.Enabled() {
		t.Errorf("Repository(a/bad-file) = %+v, %v", rc, ok)
	}
	if !strings.Contains(snapRepo(t, snap, "a/bad-file").Err.Error(), filepath.Join(absPath("src", "a/bad-file"), ".anthill.yml")) {
		t.Errorf("the error does not name the repository file: %v", snapRepo(t, snap, "a/bad-file").Err)
	}
}

func snapRepo(t *testing.T, snap *app.ConfigSnapshot, remote string) app.RepositoryConfig {
	t.Helper()
	for _, rc := range snap.Repositories {
		if rc.Entry.Remote == remote {
			return rc
		}
	}
	t.Fatalf("no repository %s", remote)
	return app.RepositoryConfig{}
}

func TestReloadDisablesOnlyTheBadRepository(t *testing.T) {
	a, b := entry("a/one"), entry("a/two")
	src := source(domain.SettingValues{}, a, b)
	cfg := app.NewConfiguration(src)
	ctx := context.Background()
	if _, err := cfg.Load(ctx); err != nil {
		t.Fatal(err)
	}

	src.Update(func(s *testkit.ConfigSource) {
		s.RepositoryFiles[a.Path] = &domain.SettingValues{Budget: ptr(-5)}
		s.RepositoryFiles[b.Path] = &domain.SettingValues{Budget: ptr(45)}
	})
	snap, err := cfg.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if one := snapRepo(t, snap, "a/one"); one.Enabled() {
		t.Error("a/one is still enabled")
	}
	two := snapRepo(t, snap, "a/two")
	if !two.Enabled() || two.Settings.Budget != 45*time.Minute {
		t.Errorf("a/two: enabled %v, budget %v", two.Enabled(), two.Settings.Budget)
	}
	if cfg.Current() != snap {
		t.Error("the reloaded configuration is not current")
	}

	// Fixing the file enables the repository again.
	src.Update(func(s *testkit.ConfigSource) { delete(s.RepositoryFiles, a.Path) })
	snap, err = cfg.Reload(ctx)
	if err != nil || !snapRepo(t, snap, "a/one").Enabled() {
		t.Errorf("after the fix: %v", err)
	}
}

func TestReloadKeepsThePreviousConfigurationWhenTheGlobalOneIsInvalid(t *testing.T) {
	src := source(domain.SettingValues{Capacity: ptr(3)}, entry("a/b"))
	cfg := app.NewConfiguration(src)
	ctx := context.Background()
	first, err := cfg.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	src.Update(func(s *testkit.ConfigSource) { s.Config.Global.WarningThreshold = ptr(120) })
	if _, err := cfg.Reload(ctx); err == nil {
		t.Fatal("Reload() accepted an invalid global configuration")
	}
	if cfg.Current() != first || cfg.Current().Global.Capacity != 3 {
		t.Error("the previous configuration was not kept")
	}
}

// Scenario "Repository file overrides global default", through the
// service.
func TestConfigurationRepositoryFileOverridesGlobalDefault(t *testing.T) {
	e := entry("a/b")
	src := source(domain.SettingValues{Budget: ptr(60)}, e)
	src.RepositoryFiles[e.Path] = &domain.SettingValues{Budget: ptr(45)}
	snap, err := app.NewConfiguration(src).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc := snap.Repositories[0]
	if rc.Settings.Budget != 45*time.Minute || rc.Settings.Source(domain.KeyBudget) != domain.LayerRepositoryFile {
		t.Errorf("budget %v from %q, want 45m from .anthill.yml", rc.Settings.Budget, rc.Settings.Source(domain.KeyBudget))
	}
}

// Scenario "Empty configuration", through the service.
func TestConfigurationEmptyConfiguration(t *testing.T) {
	snap, err := app.NewConfiguration(source(domain.SettingValues{}, entry("a/b"))).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := snap.Repositories[0].Settings.Inspect()
	want := domain.Defaults().Inspect()
	if !slices.Equal(got, want) {
		t.Errorf("Inspect() = %v, want the defaults %v", got, want)
	}
}

func TestConfigurationInvalidEntry(t *testing.T) {
	e := entry("a/b")
	e.Path = "relative/path"
	snap, err := app.NewConfiguration(source(domain.SettingValues{}, e)).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(snap.Repositories[0].Err); !slices.Equal(got, []string{`path "relative/path" must be absolute`}) {
		t.Errorf("messages = %q", got)
	}
}

func TestConfigurationBudgetAboveMaximumAcrossLayers(t *testing.T) {
	e := entry("a/b")
	src := source(domain.SettingValues{MaxBaseBudget: ptr(90)}, e)
	src.RepositoryFiles[e.Path] = &domain.SettingValues{Budget: ptr(120)}
	snap, err := app.NewConfiguration(src).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := messages(snap.Repositories[0].Err); !slices.Equal(got, []string{domain.MessageBudgetOverMaximum}) {
		t.Errorf("messages = %q", got)
	}
}

// Scenario "Local pattern added".
func TestLocalSecretPatternAdded(t *testing.T) {
	snap, err := app.NewConfiguration(source(domain.SettingValues{SecretPatterns: []string{`ACME-[0-9]{8}`}})).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := snap.Redactor.Redact("key ACME-12345678 and ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	if got != "key [REDACTED] and [REDACTED]" {
		t.Errorf("Redact() = %q", got)
	}
}
