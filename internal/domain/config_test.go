package domain_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
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

// Scenario "Repository file overrides global default".
func TestResolveRepositoryFileOverridesGlobalDefault(t *testing.T) {
	e := domain.ConfigLayers{
		RepositoryFile: &domain.SettingValues{Budget: ptr(45)},
		Global:         &domain.SettingValues{Budget: ptr(60)},
	}.Resolve()
	if e.Budget != 45*time.Minute {
		t.Errorf("Budget = %v, want 45m", e.Budget)
	}
	if got := e.Source(domain.KeyBudget); got != ".anthill.yml" {
		t.Errorf("Source(budget) = %q, want .anthill.yml", got)
	}
}

func TestResolveLayerPrecedence(t *testing.T) {
	file := &domain.SettingValues{HoldLabel: ptr("file")}
	entry := &domain.SettingValues{HoldLabel: ptr("entry")}
	global := &domain.SettingValues{HoldLabel: ptr("global")}
	tests := []struct {
		name   string
		layers domain.ConfigLayers
		want   string
		source domain.Layer
	}{
		{"repository file wins", domain.ConfigLayers{RepositoryFile: file, LocalEntry: entry, Global: global}, "file", domain.LayerRepositoryFile},
		{"local entry over global", domain.ConfigLayers{LocalEntry: entry, Global: global}, "entry", domain.LayerLocalEntry},
		{"global over default", domain.ConfigLayers{Global: global}, "global", domain.LayerGlobal},
		{"default", domain.ConfigLayers{RepositoryFile: &domain.SettingValues{}}, domain.DefaultHoldLabel, domain.LayerDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.layers.Resolve()
			if e.HoldLabel != tt.want || e.Source(domain.KeyHoldLabel) != tt.source {
				t.Errorf("hold label = %q from %q, want %q from %q", e.HoldLabel, e.Source(domain.KeyHoldLabel), tt.want, tt.source)
			}
		})
	}
}

func TestResolveZeroValueIsSet(t *testing.T) {
	e := domain.ConfigLayers{
		RepositoryFile: &domain.SettingValues{AutoFollowUp: ptr(false), RoundLimit: ptr(0)},
	}.Resolve()
	if e.AutoFollowUp || e.Source(domain.KeyAutoFollowUp) != domain.LayerRepositoryFile {
		t.Errorf("auto-follow-up = %v from %q, want false from .anthill.yml", e.AutoFollowUp, e.Source(domain.KeyAutoFollowUp))
	}
	if e.RoundLimit != 0 || e.Source(domain.KeyRoundLimit) != domain.LayerRepositoryFile {
		t.Errorf("round limit = %d from %q, want 0 from .anthill.yml", e.RoundLimit, e.Source(domain.KeyRoundLimit))
	}
}

// emptyConfigurationDefaults is every default of anthill-configuration, as
// Inspect prints it.
var emptyConfigurationDefaults = map[string]string{
	"opt-in-label":                "anthill",
	"hold-label":                  "anthill:hold",
	"claim-label":                 "anthill:claimed",
	"budget":                      "60 min",
	"follow-up-budget":            "50%",
	"max-base-budget":             "180 min",
	"warning-threshold":           "80%",
	"extension-allowance":         "0 min",
	"ceiling":                     "3×",
	"branch-prefix":               "ant",
	"base-branch":                 "none",
	"protected-branches":          "none",
	"shepherd-scope":              "ant-authored",
	"review-runner":               "claude-code",
	"round-limit":                 "3",
	"auto-follow-up":              "true",
	"follow-up-limit":             "5",
	"auto-resume":                 "false",
	"verification-command":        "none",
	"require-acceptance-criteria": "false",
	"agent-instructions":          "none",
	"runner":                      "claude-code",
	"capacity":                    "2",
	"reviewing":                   "true",
	"poll-interval":               "2m",
	"lease":                       "15m",
	"grace-period":                "3m",
	"extension-wait":              "5m",
	"question-staleness":          "72h",
	"otlp-endpoint":               "none",
	"secret-patterns":             "none",
	"worktree-root":               "~/.anthill/worktrees",
	"azure-devops-projects":       "none",
	"provider-poll-intervals":     "none",
}

// Scenario "Empty configuration": with only a repository entry, every
// setting has its listed default.
func TestEmptyConfigurationListsEveryDefault(t *testing.T) {
	e := domain.ConfigLayers{LocalEntry: &domain.SettingValues{}, Global: &domain.SettingValues{}}.Resolve()
	rows := e.Inspect()
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Value
		if r.Source != domain.LayerDefault {
			t.Errorf("%s comes from %q, want default", r.Key, r.Source)
		}
	}
	if !reflect.DeepEqual(got, emptyConfigurationDefaults) {
		t.Errorf("Inspect() =\n%v\nwant\n%v", got, emptyConfigurationDefaults)
	}

	want := domain.Effective{
		OptInLabel:         "anthill",
		HoldLabel:          "anthill:hold",
		ClaimLabel:         "anthill:claimed",
		Budget:             60 * time.Minute,
		FollowUpBudget:     50,
		MaxBaseBudget:      180 * time.Minute,
		WarningThreshold:   80,
		ExtensionAllowance: domain.ExtensionAllowance{},
		Ceiling:            3,
		BranchPrefix:       "ant",
		ShepherdScope:      domain.ScopeAntAuthored,
		ReviewRunner:       domain.RunnerClaudeCode,
		RoundLimit:         3,
		AutoFollowUp:       true,
		FollowUpLimit:      5,
		Runner:             domain.RunnerClaudeCode,
		Capacity:           2,
		Reviewing:          true,
		PollInterval:       2 * time.Minute,
		Lease:              15 * time.Minute,
		GracePeriod:        3 * time.Minute,
		ExtensionWait:      5 * time.Minute,
		QuestionStaleness:  72 * time.Hour,
	}
	// Compare the exported fields only.
	e2 := domain.Effective{}
	copyExported(&e2, e)
	if !reflect.DeepEqual(e2, want) {
		t.Errorf("defaults =\n%+v\nwant\n%+v", e2, want)
	}
	if e.BudgetPolicy() != domain.DefaultBudgetPolicy() {
		t.Errorf("BudgetPolicy() = %+v, want %+v", e.BudgetPolicy(), domain.DefaultBudgetPolicy())
	}
}

// copyExported copies the exported fields of src into dst.
func copyExported(dst *domain.Effective, src domain.Effective) {
	dv, sv := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src)
	for i := range sv.NumField() {
		if sv.Type().Field(i).IsExported() {
			dv.Field(i).Set(sv.Field(i))
		}
	}
}

// provider-poll-intervals overrides poll-interval per connection, in any
// case; other connections keep poll-interval (spec work-item-sources,
// "Polling cadence").
func TestPollIntervalForConnection(t *testing.T) {
	e := domain.ConfigLayers{Global: &domain.SettingValues{
		PollInterval:          ptr(3 * time.Minute),
		ProviderPollIntervals: map[string]time.Duration{"Azure-DevOps/Contoso": 5 * time.Minute},
	}}.Resolve()
	if got := e.PollIntervalFor("azure-devops/contoso"); got != 5*time.Minute {
		t.Errorf("azure-devops/contoso: %v", got)
	}
	if got := e.PollIntervalFor("github"); got != 3*time.Minute {
		t.Errorf("github: %v", got)
	}
	if got := domain.Defaults().PollIntervalFor("github"); got != domain.DefaultPollInterval {
		t.Errorf("default: %v", got)
	}
	if e.Source(domain.KeyProviderPollIntervals) != domain.LayerGlobal {
		t.Errorf("source = %q", e.Source(domain.KeyProviderPollIntervals))
	}
}

// Scenario "Default budget and lease".
func TestDefaultBudgetAndLease(t *testing.T) {
	e := domain.Defaults()
	if e.Budget != 60*time.Minute || e.Lease != 15*time.Minute {
		t.Errorf("budget %v, lease %v, want 60m and 15m", e.Budget, e.Lease)
	}
}

// Scenario "No auto-approve allowance by default".
func TestNoAutoApproveAllowanceByDefault(t *testing.T) {
	e := domain.Defaults()
	b := domain.NewBudget(e.Budget, e.BudgetPolicy())
	req, err := b.RequestExtension(5, "tests are slow", t0)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != domain.ExtensionPending {
		t.Errorf("status = %q, want pending for the queen", req.Status)
	}
}

func TestExtensionAllowanceTimesDefaultsToOnce(t *testing.T) {
	tests := []struct {
		name    string
		setting domain.ExtensionAllowanceSetting
		want    domain.ExtensionAllowance
	}{
		{"minutes only", domain.ExtensionAllowanceSetting{Minutes: 15}, domain.ExtensionAllowance{MaxMinutes: 15, Times: 1}},
		{"minutes and times", domain.ExtensionAllowanceSetting{Minutes: 15, Times: ptr(3)}, domain.ExtensionAllowance{MaxMinutes: 15, Times: 3}},
		{"zero", domain.ExtensionAllowanceSetting{}, domain.ExtensionAllowance{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.setting.Allowance(); got != tt.want {
				t.Errorf("Allowance() = %+v, want %+v", got, tt.want)
			}
		})
	}
	e := domain.ConfigLayers{RepositoryFile: &domain.SettingValues{
		ExtensionAllowance: &domain.ExtensionAllowanceSetting{Minutes: 15},
	}}.Resolve()
	if got := e.Inspect()[7]; got.Key != domain.KeyExtensionAllowance || got.Value != "15 min, 1 times" {
		t.Errorf("inspected allowance = %+v", got)
	}
}

func TestResolveReviewRunnerFollowsRunner(t *testing.T) {
	e := domain.ConfigLayers{LocalEntry: &domain.SettingValues{Runner: ptr(domain.RunnerCodex)}}.Resolve()
	if e.ReviewRunner != domain.RunnerCodex || e.Source(domain.KeyReviewRunner) != domain.LayerDefault {
		t.Errorf("review runner = %q from %q, want codex from default", e.ReviewRunner, e.Source(domain.KeyReviewRunner))
	}
	e = domain.ConfigLayers{
		RepositoryFile: &domain.SettingValues{ReviewRunner: ptr(domain.RunnerOpenCode)},
		LocalEntry:     &domain.SettingValues{Runner: ptr(domain.RunnerCodex)},
	}.Resolve()
	if e.ReviewRunner != domain.RunnerOpenCode {
		t.Errorf("review runner = %q, want opencode", e.ReviewRunner)
	}
}

func TestResolveReviewingOff(t *testing.T) {
	tests := []struct {
		name          string
		entry, global *bool
		want          bool
		source        domain.Layer
	}{
		{"on by default", nil, nil, true, domain.LayerDefault},
		{"off for one repository", ptr(false), nil, false, domain.LayerLocalEntry},
		{"off for the machine", nil, ptr(false), false, domain.LayerGlobal},
		{"machine off wins over an entry", ptr(true), ptr(false), false, domain.LayerGlobal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := domain.ConfigLayers{
				LocalEntry: &domain.SettingValues{Reviewing: tt.entry},
				Global:     &domain.SettingValues{Reviewing: tt.global},
			}.Resolve()
			if e.Reviewing != tt.want || e.Source(domain.KeyReviewing) != tt.source {
				t.Errorf("reviewing = %v from %q, want %v from %q", e.Reviewing, e.Source(domain.KeyReviewing), tt.want, tt.source)
			}
		})
	}
}

// Setting labels ignore case like every label: Agent:Codex picks codex.
func TestForItemLabelInAnotherCase(t *testing.T) {
	item, warnings := domain.ConfigLayers{}.Resolve().ForItem([]string{"Anthill", "Agent:Codex", "BUDGET:30"})
	if item.Runner != domain.RunnerCodex || item.Budget != 30*time.Minute || len(warnings) != 0 {
		t.Errorf("runner %q, budget %v, warnings %+v; want codex, 30m, none", item.Runner, item.Budget, warnings)
	}
}

// azure-devops-projects names a project in any case; its default
// repository is found case-insensitively and the project is lowercased.
func TestAzureDevOpsProjectDefault(t *testing.T) {
	e := domain.ConfigLayers{Global: &domain.SettingValues{AzureDevOpsProjects: []domain.AzureDevOpsProject{
		{Project: "Contoso/Shop", DefaultRepository: "web"},
	}}}.Resolve()
	shop := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop"}
	if got := e.DefaultRepository(shop); got != "web" {
		t.Errorf("DefaultRepository = %q, want web", got)
	}
	if got := e.DefaultRepository(domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "hr"}); got != "" {
		t.Errorf("DefaultRepository of an unlisted project = %q, want none", got)
	}
	if r, err := e.AzureDevOpsProjects[0].Repository(); err != nil || r != shop {
		t.Errorf("Repository() = %+v, %v; want lowercase %+v", r, err, shop)
	}
	if e.Source(domain.KeyAzureDevOpsProjects) != domain.LayerGlobal {
		t.Errorf("source = %q", e.Source(domain.KeyAzureDevOpsProjects))
	}
}

// The runner order: agent: label, local entry, global, default (spec
// ant-execution, "Label overrides repository default").
func TestForItemLabelOverridesRepositoryDefault(t *testing.T) {
	e := domain.ConfigLayers{
		RepositoryFile: &domain.SettingValues{MaxBaseBudget: ptr(120)},
		LocalEntry:     &domain.SettingValues{Runner: ptr(domain.RunnerClaudeCode)},
		Global:         &domain.SettingValues{Runner: ptr(domain.RunnerOpenCode)},
	}.Resolve()
	item, warnings := e.ForItem([]string{"agent:codex", "budget:600", "base:release/2.0", "bug"})
	if item.Runner != domain.RunnerCodex || item.Source(domain.KeyRunner) != domain.LayerLabel {
		t.Errorf("runner = %q from %q, want codex from label", item.Runner, item.Source(domain.KeyRunner))
	}
	if item.Budget != 120*time.Minute || item.Source(domain.KeyBudget) != domain.LayerLabel {
		t.Errorf("budget = %v from %q, want 120m (capped) from label", item.Budget, item.Source(domain.KeyBudget))
	}
	if item.BaseBranch != "release/2.0" || item.Source(domain.KeyBaseBranch) != domain.LayerLabel {
		t.Errorf("base branch = %q from %q", item.BaseBranch, item.Source(domain.KeyBaseBranch))
	}
	if len(warnings) != 1 || warnings[0].Reason != domain.WarnCapped {
		t.Errorf("warnings = %+v, want one capped warning", warnings)
	}
	// The repository's settings are unchanged.
	if e.Runner != domain.RunnerClaudeCode || e.Source(domain.KeyRunner) != domain.LayerLocalEntry {
		t.Errorf("ForItem changed the repository settings: runner %q from %q", e.Runner, e.Source(domain.KeyRunner))
	}
	if item.ReviewRunner != domain.RunnerClaudeCode {
		t.Errorf("review runner = %q, want the repository's runner", item.ReviewRunner)
	}
}

func TestForItemUnknownRunnerLabel(t *testing.T) {
	item, warnings := domain.Defaults().ForItem([]string{"agent:gpt"})
	if item.Runner != domain.DefaultRunner {
		t.Errorf("runner = %q, want the default", item.Runner)
	}
	if len(warnings) != 1 || warnings[0].Message != `label "agent:gpt" ignored: unknown runner` {
		t.Errorf("warnings = %+v", warnings)
	}
}

func TestCheckKey(t *testing.T) {
	tests := []struct {
		layer domain.Layer
		path  string
		want  string
	}{
		// Scenario "Credentials in repository file".
		{domain.LayerRepositoryFile, "token", `forbidden key "token": credentials belong in the OS credential store`},
		{domain.LayerRepositoryFile, "GitHub_Token", `forbidden key "GitHub_Token": credentials belong in the OS credential store`},
		{domain.LayerRepositoryFile, "extension-allowance.password", `forbidden key "password": credentials belong in the OS credential store`},
		// Scenario "Telemetry endpoint in repository file".
		{domain.LayerRepositoryFile, "otlp-endpoint", `forbidden key "otlp-endpoint": telemetry settings belong in the local configuration`},
		{domain.LayerRepositoryFile, "otlp-headers", `forbidden key "otlp-headers": telemetry settings belong in the local configuration`},
		{domain.LayerRepositoryFile, "secret-patterns", `forbidden key "secret-patterns": secret patterns belong in the local configuration`},
		{domain.LayerRepositoryFile, "path", `forbidden key "path": machine paths belong in the local configuration`},
		{domain.LayerRepositoryFile, "worktree-root", `forbidden key "worktree-root": machine paths belong in the local configuration`},
		{domain.LayerRepositoryFile, "runner", `forbidden key "runner": machine settings belong in the local configuration`},
		{domain.LayerRepositoryFile, "lease", `forbidden key "lease": machine settings belong in the local configuration`},
		{domain.LayerRepositoryFile, "foo", `unknown key "foo"`},
		{domain.LayerRepositoryFile, "extension-allowance.foo", `unknown key "extension-allowance.foo"`},
		{domain.LayerRepositoryFile, "budget", ""},
		{domain.LayerRepositoryFile, "extension-allowance.times", ""},
		{domain.LayerRepositoryFile, "azure-devops-projects", `forbidden key "azure-devops-projects": machine settings belong in the local configuration`},
		{domain.LayerLocalEntry, "azure-devops-projects", `key "azure-devops-projects" can be set only in the global configuration`},
		{domain.LayerGlobal, "azure-devops-projects.default-repository", ""},
		{domain.LayerGlobal, "azure-devops-projects.repo", `unknown key "azure-devops-projects.repo"`},
		{domain.LayerGlobal, "provider-poll-intervals.azure-devops/contoso", ""},
		{domain.LayerLocalEntry, "provider-poll-intervals", `key "provider-poll-intervals" can be set only in the global configuration`},
		{domain.LayerRepositoryFile, "provider-poll-intervals", `forbidden key "provider-poll-intervals": machine settings belong in the local configuration`},
		// Scenario "OTLP headers in a configuration file".
		{domain.LayerGlobal, "otlp-headers", "OTLP headers belong in the OS credential store, not in a configuration file"},
		{domain.LayerLocalEntry, "otlp-headers", "OTLP headers belong in the OS credential store, not in a configuration file"},
		{domain.LayerGlobal, "token", `forbidden key "token": credentials belong in the OS credential store`},
		{domain.LayerGlobal, "otlp-endpoint", ""},
		{domain.LayerGlobal, "repositories", ""},
		{domain.LayerGlobal, "path", `key "path" can be set only in the repository entries`},
		{domain.LayerLocalEntry, "otlp-endpoint", `key "otlp-endpoint" can be set only in the global configuration`},
		{domain.LayerLocalEntry, "poll-interval", `key "poll-interval" can be set only in the global configuration`},
		{domain.LayerLocalEntry, "capacity", ""},
		{domain.LayerLocalEntry, "budget", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.layer)+" "+tt.path, func(t *testing.T) {
			if got := domain.CheckKey(tt.layer, strings.Split(tt.path, ".")); got != tt.want {
				t.Errorf("CheckKey = %q, want %q", got, tt.want)
			}
		})
	}
}

// configMessage returns the messages of every *ConfigError in errs.
func configMessages(errs []error) []string {
	var out []string
	for _, err := range errs {
		var ce *domain.ConfigError
		if errors.As(err, &ce) {
			out = append(out, ce.Message)
		}
	}
	return out
}

// Scenario "Warning threshold out of range", at the value level.
func TestValidateSettingsWarningThresholdOutOfRange(t *testing.T) {
	errs := domain.ValidateSettings("config.yml", &domain.SettingValues{WarningThreshold: ptr(120)})
	if got := configMessages(errs); !slices.Equal(got, []string{"warning threshold must be between 1 and 99 percent"}) {
		t.Errorf("messages = %q", got)
	}
	if got := errs[0].Error(); got != "config.yml: warning-threshold: warning threshold must be between 1 and 99 percent" {
		t.Errorf("Error() = %q", got)
	}
}

func TestValidateSettings(t *testing.T) {
	tests := []struct {
		name   string
		values domain.SettingValues
		want   string
	}{
		{"empty label", domain.SettingValues{OptInLabel: ptr(" ")}, "opt-in label must not be empty"},
		{"zero budget", domain.SettingValues{Budget: ptr(0)}, "budget must be more than 0 minutes"},
		{"follow-up budget", domain.SettingValues{FollowUpBudget: ptr(150)}, "follow-up budget must be between 1 and 100 percent"},
		{"maximum base budget", domain.SettingValues{MaxBaseBudget: ptr(-1)}, "maximum base budget must be more than 0 minutes"},
		{"warning threshold zero", domain.SettingValues{WarningThreshold: ptr(0)}, domain.MessageWarningThreshold},
		{"allowance minutes", domain.SettingValues{ExtensionAllowance: &domain.ExtensionAllowanceSetting{Minutes: -5}}, "extension allowance minutes must not be negative"},
		{"allowance times", domain.SettingValues{ExtensionAllowance: &domain.ExtensionAllowanceSetting{Times: ptr(-1)}}, "extension allowance times must not be negative"},
		{"ceiling", domain.SettingValues{Ceiling: ptr(0)}, "ceiling must be at least 1"},
		{"branch prefix", domain.SettingValues{BranchPrefix: ptr("ant..x")}, `branch name "ant..x" contains an invalid sequence`},
		{"base branch", domain.SettingValues{BaseBranch: ptr("-main")}, `branch name "-main" must not start with "-"`},
		{"protected pattern", domain.SettingValues{ProtectedBranches: []string{"release/["}}, `protected branch pattern "release/[" is not valid`},
		{"shepherd scope", domain.SettingValues{ShepherdScope: ptr(domain.ShepherdScope("some"))}, `shepherd scope must be "ant-authored", "labelled" or "all"`},
		{"review runner", domain.SettingValues{ReviewRunner: ptr(domain.Runner("gpt"))}, `review runner must be "claude-code", "codex" or "opencode"`},
		{"runner", domain.SettingValues{Runner: ptr(domain.Runner("gpt"))}, `runner must be "claude-code", "codex" or "opencode"`},
		{"round limit", domain.SettingValues{RoundLimit: ptr(-1)}, "round limit must not be negative"},
		{"follow-up limit", domain.SettingValues{FollowUpLimit: ptr(-1)}, "follow-up limit must not be negative"},
		{"verification command", domain.SettingValues{VerificationCommand: ptr("")}, "verification command must not be empty"},
		{"agent instructions absolute", domain.SettingValues{AgentInstructions: ptr(absPath("etc", "AGENTS.md"))}, "agent instructions file must be a relative path inside the repository"},
		{"agent instructions outside", domain.SettingValues{AgentInstructions: ptr("../AGENTS.md")}, "agent instructions file must be a relative path inside the repository"},
		{"capacity", domain.SettingValues{Capacity: ptr(0)}, "capacity must be at least 1"},
		{"poll interval", domain.SettingValues{PollInterval: ptr(time.Duration(0))}, "poll interval must be more than 0"},
		{"lease", domain.SettingValues{Lease: ptr(-time.Minute)}, "lease must be more than 0"},
		{"grace period", domain.SettingValues{GracePeriod: ptr(time.Duration(0))}, "grace period must be more than 0"},
		{"extension wait", domain.SettingValues{ExtensionWait: ptr(time.Duration(0))}, "extension wait must be more than 0"},
		{"question staleness", domain.SettingValues{QuestionStaleness: ptr(time.Duration(0))}, "question staleness must be more than 0"},
		{"otlp endpoint", domain.SettingValues{OTLPEndpoint: ptr("collector:4318")}, "OTLP endpoint must be an http or https URL"},
		{"secret pattern", domain.SettingValues{SecretPatterns: []string{"("}}, "secret pattern \"(\" is not valid: error parsing regexp: missing closing ): `(`"},
		{"azure devops project without organization", domain.SettingValues{AzureDevOpsProjects: []domain.AzureDevOpsProject{{Project: "shop", DefaultRepository: "web"}}}, `project "shop" must be organization/project`},
		{"azure devops project without default", domain.SettingValues{AzureDevOpsProjects: []domain.AzureDevOpsProject{{Project: "contoso/shop"}}}, `project "contoso/shop": default repository "" must be the name of one repository`},
		{"azure devops project twice", domain.SettingValues{AzureDevOpsProjects: []domain.AzureDevOpsProject{
			{Project: "contoso/shop", DefaultRepository: "web"}, {Project: "Contoso/Shop", DefaultRepository: "api"},
		}}, `project "Contoso/Shop" is listed more than once`},
		{"provider poll interval too short", domain.SettingValues{ProviderPollIntervals: map[string]time.Duration{"github": 10 * time.Second}}, `poll interval of "github" must be at least 30s`},
		{"provider poll interval empty name", domain.SettingValues{ProviderPollIntervals: map[string]time.Duration{" ": time.Minute}}, "provider connection name must not be empty"},
		{"provider poll interval twice", domain.SettingValues{ProviderPollIntervals: map[string]time.Duration{"GitHub": time.Minute, "github": time.Minute}}, `provider connection "github" is listed more than once`},
		{"worktree root relative", domain.SettingValues{WorktreeRoot: ptr("worktrees")}, `worktree root "worktrees" must be an absolute path`},
		{"budget over maximum", domain.SettingValues{Budget: ptr(200), MaxBaseBudget: ptr(100)}, domain.MessageBudgetOverMaximum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := configMessages(domain.ValidateSettings("f", &tt.values))
			if !slices.Equal(got, []string{tt.want}) {
				t.Errorf("messages = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateSettingsAcceptsValidValues(t *testing.T) {
	v := domain.SettingValues{
		OptInLabel:            ptr("ai"),
		Budget:                ptr(45),
		WarningThreshold:      ptr(99),
		ProtectedBranches:     []string{"release/*", "main"},
		AgentInstructions:     ptr("docs/AGENTS.md"),
		OTLPEndpoint:          ptr("https://collector.example.com:4318"),
		SecretPatterns:        []string{`ACME-[0-9]{8}`},
		VerificationCommand:   ptr("go test ./..."),
		Reviewing:             ptr(false),
		ExtensionAllowance:    &domain.ExtensionAllowanceSetting{Minutes: 15},
		ProviderPollIntervals: map[string]time.Duration{"github": 30 * time.Second, "azure-devops/contoso": 5 * time.Minute},
	}
	if errs := domain.ValidateSettings("f", &v); errs != nil {
		t.Errorf("errors = %v", errs)
	}
	if errs := domain.ValidateSettings("f", nil); errs != nil {
		t.Errorf("errors for nil = %v", errs)
	}
}

func TestValidateEntry(t *testing.T) {
	good := domain.RepositoryEntry{Provider: "github", Remote: "matteorosani/anthill", Path: absPath("src", "anthill")}
	if errs := domain.ValidateEntry("config.yml", 0, good); errs != nil {
		t.Fatalf("errors = %v", errs)
	}
	tests := []struct {
		name  string
		entry domain.RepositoryEntry
		key   string
		want  string
	}{
		{"missing provider", domain.RepositoryEntry{Remote: "a/b", Path: good.Path}, "repositories[1].remote", "provider is missing"},
		{"unknown provider", domain.RepositoryEntry{Provider: "gitlab", Remote: "a/b", Path: good.Path}, "repositories[1].remote", `provider "gitlab" must be "github" or "azure-devops"`},
		{"github remote", domain.RepositoryEntry{Provider: "github", Remote: "a/b/c", Path: good.Path}, "repositories[1].remote", `remote "a/b/c" must be owner/name`},
		{"azure remote", domain.RepositoryEntry{Provider: "azure-devops", Remote: "contoso/shop", Path: good.Path}, "repositories[1].remote", `remote "contoso/shop" must be organization/project/repository`},
		{"empty part", domain.RepositoryEntry{Provider: "github", Remote: "a/", Path: good.Path}, "repositories[1].remote", `remote "a/" has an empty part`},
		{"missing path", domain.RepositoryEntry{Provider: "github", Remote: "a/b"}, "repositories[1].path", "path is missing"},
		{"relative path", domain.RepositoryEntry{Provider: "github", Remote: "a/b", Path: "src/b"}, "repositories[1].path", `path "src/b" must be absolute`},
		{"bad setting", domain.RepositoryEntry{Provider: "github", Remote: "a/b", Path: good.Path, Settings: domain.SettingValues{Capacity: ptr(0)}}, "repositories[1].capacity", "capacity must be at least 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := domain.ValidateEntry("config.yml", 1, tt.entry)
			var ce *domain.ConfigError
			if len(errs) != 1 || !errors.As(errs[0], &ce) || ce.Key != tt.key || ce.Message != tt.want {
				t.Errorf("errors = %v, want %s: %s", errs, tt.key, tt.want)
			}
		})
	}
}

func TestRepositoryEntryRepository(t *testing.T) {
	r, err := domain.RepositoryEntry{Provider: "azure-devops", Remote: "contoso/shop/web"}.Repository()
	want := domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "web"}
	if err != nil || r != want {
		t.Errorf("Repository() = %+v, %v", r, err)
	}
}

func TestEffectiveValidateBudgetAcrossLayers(t *testing.T) {
	e := domain.ConfigLayers{
		RepositoryFile: &domain.SettingValues{Budget: ptr(200)},
		Global:         &domain.SettingValues{MaxBaseBudget: ptr(120)},
	}.Resolve()
	errs := e.Validate()
	if len(errs) != 1 || errs[0].Error() != ".anthill.yml: budget: budget must not exceed the maximum base budget" {
		t.Errorf("Validate() = %v", errs)
	}
	if errs := domain.Defaults().Validate(); errs != nil {
		t.Errorf("defaults invalid: %v", errs)
	}
}

func TestLooksLikeSecret(t *testing.T) {
	if !domain.LooksLikeSecret("ghp_" + strings.Repeat("a", 36)) {
		t.Error("GitHub token not detected")
	}
	for _, v := range []string{"go test ./...", strings.Repeat("abcdefgh", 20), "make lint test"} {
		if domain.LooksLikeSecret(v) {
			t.Errorf("%q detected as a secret", v)
		}
	}
}

// The yaml tags of SettingValues and RepositoryEntry are the keys of
// AllSettings, so that the file keys and the registry cannot drift apart.
func TestSettingKeysMatchFileTags(t *testing.T) {
	var tags []string
	for _, typ := range []reflect.Type{reflect.TypeFor[domain.SettingValues](), reflect.TypeFor[domain.RepositoryEntry]()} {
		for i := range typ.NumField() {
			tag := typ.Field(i).Tag.Get("yaml")
			if tag != ",inline" {
				tags = append(tags, tag)
			}
		}
	}
	var keys []string
	for _, s := range domain.AllSettings() {
		keys = append(keys, s.Key)
	}
	slices.Sort(tags)
	slices.Sort(keys)
	if !slices.Equal(tags, keys) {
		t.Errorf("yaml tags %v\ndiffer from keys %v", tags, keys)
	}
}
