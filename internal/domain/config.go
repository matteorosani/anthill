package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The defaults of anthill-configuration that have no other home. The
// labels, budgets, lease and branch prefix keep their defaults next to
// the rules that use them.
const (
	DefaultPollInterval = 2 * time.Minute
	// MinProviderPollInterval is the shortest interval
	// provider-poll-intervals accepts.
	MinProviderPollInterval  = 30 * time.Second
	DefaultCapacity          = 2
	DefaultRoundLimit        = 3
	DefaultFollowUpLimit     = 5
	DefaultQuestionStaleness = 72 * time.Hour
	DefaultRunner            = RunnerClaudeCode
	DefaultShepherdScope     = ScopeAntAuthored
	DefaultAutoFollowUp      = true
	DefaultAutoResume        = false
	DefaultReviewing         = true
)

// Runner names an agent runner.
type Runner string

// The supported runners.
const (
	RunnerClaudeCode Runner = "claude-code"
	RunnerCodex      Runner = "codex"
	RunnerOpenCode   Runner = "opencode"
)

// Valid reports whether r is a supported runner.
func (r Runner) Valid() bool {
	return r == RunnerClaudeCode || r == RunnerCodex || r == RunnerOpenCode
}

// ShepherdScope decides which PRs the shepherd reviews on its own (spec
// pr-shepherd).
type ShepherdScope string

// The shepherd scopes.
const (
	ScopeAntAuthored ShepherdScope = "ant-authored"
	ScopeLabelled    ShepherdScope = "labelled"
	ScopeAll         ShepherdScope = "all"
)

// Valid reports whether s is a known scope.
func (s ShepherdScope) Valid() bool {
	return s == ScopeAntAuthored || s == ScopeLabelled || s == ScopeAll
}

// Layer is one place a setting can come from, and the name shown as a
// setting's source when it is inspected.
type Layer string

// The layers, highest precedence first (spec anthill-configuration).
const (
	LayerLabel          Layer = "label"
	LayerRepositoryFile Layer = ".anthill.yml"
	LayerLocalEntry     Layer = "local repository entry"
	LayerGlobal         Layer = "global configuration"
	LayerDefault        Layer = "default"
)

// The setting keys. They are the same in every file.
const (
	KeyOptInLabel                = "opt-in-label"
	KeyHoldLabel                 = "hold-label"
	KeyClaimLabel                = "claim-label"
	KeyBudget                    = "budget"
	KeyFollowUpBudget            = "follow-up-budget"
	KeyMaxBaseBudget             = "max-base-budget"
	KeyWarningThreshold          = "warning-threshold"
	KeyExtensionAllowance        = "extension-allowance"
	KeyCeiling                   = "ceiling"
	KeyBranchPrefix              = "branch-prefix"
	KeyBaseBranch                = "base-branch"
	KeyProtectedBranches         = "protected-branches"
	KeyShepherdScope             = "shepherd-scope"
	KeyReviewRunner              = "review-runner"
	KeyRoundLimit                = "round-limit"
	KeyAutoFollowUp              = "auto-follow-up"
	KeyFollowUpLimit             = "follow-up-limit"
	KeyAutoResume                = "auto-resume"
	KeyVerificationCommand       = "verification-command"
	KeyRequireAcceptanceCriteria = "require-acceptance-criteria"
	KeyAgentInstructions         = "agent-instructions"

	KeyRunner            = "runner"
	KeyCapacity          = "capacity"
	KeyReviewing         = "reviewing"
	KeyPollInterval      = "poll-interval"
	KeyLease             = "lease"
	KeyGracePeriod       = "grace-period"
	KeyExtensionWait     = "extension-wait"
	KeyQuestionStaleness = "question-staleness"
	KeyOTLPEndpoint      = "otlp-endpoint"
	KeySecretPatterns    = "secret-patterns"
	KeyWorktreeRoot      = "worktree-root"
	// KeyAzureDevOpsProjects maps Azure DevOps projects to their default
	// target repository (spec work-item-sources, "Repository mapping").
	KeyAzureDevOpsProjects = "azure-devops-projects"
	// KeyProviderPollIntervals overrides poll-interval per provider
	// connection (spec work-item-sources, "Polling cadence").
	KeyProviderPollIntervals = "provider-poll-intervals"

	KeyProvider = "provider"
	KeyRemote   = "remote"
	KeyPath     = "path"

	// KeyRepositories holds the local repository entries in the global
	// configuration file. It is not a setting.
	KeyRepositories = "repositories"
	// KeyOTLPHeaders is never accepted: OTLP headers live in the OS
	// credential store.
	KeyOTLPHeaders = "otlp-headers"
)

// The sub-keys of extension-allowance.
const (
	KeyAllowanceMinutes = "minutes"
	KeyAllowanceTimes   = "times"
)

// The sub-keys of each azure-devops-projects entry.
const (
	KeyProject           = "project"
	KeyDefaultRepository = "default-repository"
)

// SettingInfo describes one setting: its key and the layers that may set
// it, highest precedence first.
type SettingInfo struct {
	Key    string
	Layers []Layer
}

var (
	sharedLayers  = []Layer{LayerRepositoryFile, LayerLocalEntry, LayerGlobal}
	labelShared   = []Layer{LayerLabel, LayerRepositoryFile, LayerLocalEntry, LayerGlobal}
	machineLayers = []Layer{LayerLocalEntry, LayerGlobal}
	globalOnly    = []Layer{LayerGlobal}
	entryOnly     = []Layer{LayerLocalEntry}
)

// settingInfos lists every setting, in the order of docs/configuration.md.
var settingInfos = []SettingInfo{
	{KeyOptInLabel, sharedLayers},
	{KeyHoldLabel, sharedLayers},
	{KeyClaimLabel, sharedLayers},
	{KeyBudget, labelShared},
	{KeyFollowUpBudget, sharedLayers},
	{KeyMaxBaseBudget, sharedLayers},
	{KeyWarningThreshold, sharedLayers},
	{KeyExtensionAllowance, sharedLayers},
	{KeyCeiling, sharedLayers},
	{KeyBranchPrefix, sharedLayers},
	{KeyBaseBranch, labelShared},
	{KeyProtectedBranches, sharedLayers},
	{KeyShepherdScope, sharedLayers},
	{KeyReviewRunner, sharedLayers},
	{KeyRoundLimit, sharedLayers},
	{KeyAutoFollowUp, sharedLayers},
	{KeyFollowUpLimit, sharedLayers},
	{KeyAutoResume, sharedLayers},
	{KeyVerificationCommand, sharedLayers},
	{KeyRequireAcceptanceCriteria, sharedLayers},
	{KeyAgentInstructions, sharedLayers},
	{KeyRunner, []Layer{LayerLabel, LayerLocalEntry, LayerGlobal}},
	{KeyCapacity, machineLayers},
	{KeyReviewing, machineLayers},
	{KeyPollInterval, globalOnly},
	{KeyLease, globalOnly},
	{KeyGracePeriod, globalOnly},
	{KeyExtensionWait, globalOnly},
	{KeyQuestionStaleness, globalOnly},
	{KeyOTLPEndpoint, globalOnly},
	{KeySecretPatterns, globalOnly},
	{KeyWorktreeRoot, globalOnly},
	{KeyAzureDevOpsProjects, globalOnly},
	{KeyProviderPollIntervals, globalOnly},
	{KeyProvider, entryOnly},
	{KeyRemote, entryOnly},
	{KeyPath, entryOnly},
}

// AllSettings lists every setting with the layers that may set it.
func AllSettings() []SettingInfo {
	out := make([]SettingInfo, len(settingInfos))
	for i, s := range settingInfos {
		out[i] = SettingInfo{Key: s.Key, Layers: slices.Clone(s.Layers)}
	}
	return out
}

func settingInfo(key string) (SettingInfo, bool) {
	for _, s := range settingInfos {
		if s.Key == key {
			return s, true
		}
	}
	return SettingInfo{}, false
}

// The reasons a key is forbidden. Each names where the setting belongs
// instead.
const (
	reasonCredentials = "credentials belong in the OS credential store"
	reasonTelemetry   = "telemetry settings belong in the local configuration"
	reasonPatterns    = "secret patterns belong in the local configuration"
	reasonPaths       = "machine paths belong in the local configuration"
	reasonMachine     = "machine settings belong in the local configuration"
)

// MessageOTLPHeaders is the error for OTLP headers in any configuration
// file (spec anthill-configuration, "Telemetry settings are local").
const MessageOTLPHeaders = "OTLP headers belong in the OS credential store, not in a configuration file"

// credentialKey reports whether a key name looks like it holds a
// credential, such as token, github-token, password or api-key.
func credentialKey(name string) bool {
	switch name {
	case "token", "password", "passwd", "secret", "pat", "api-key", "apikey",
		"credential", "credentials", "private-key", "client-secret":
		return true
	}
	for _, suffix := range []string{"-token", "-password", "-secret", "-pat", "-api-key", "-private-key", "-credentials"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// normalizeKey lowercases a key and treats "_" like "-", so that
// GitHub_Token is caught like github-token.
func normalizeKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(key), "_", "-")
}

// forbiddenReason returns why a key may never appear in the layer at any
// depth, or "" when it is not forbidden there.
func forbiddenReason(layer Layer, name string) string {
	name = normalizeKey(name)
	switch {
	case name == KeyOTLPHeaders:
		if layer == LayerRepositoryFile {
			return reasonTelemetry
		}
		return MessageOTLPHeaders
	case credentialKey(name):
		return reasonCredentials
	case layer != LayerRepositoryFile:
		return ""
	case strings.HasPrefix(name, "otlp") || name == "telemetry":
		return reasonTelemetry
	case name == KeySecretPatterns:
		return reasonPatterns
	case name == KeyPath || name == "clone-path" || name == KeyWorktreeRoot:
		return reasonPaths
	case name == KeyRepositories:
		return reasonMachine
	}
	if info, ok := settingInfo(name); ok && !slices.Contains(info.Layers, LayerRepositoryFile) {
		return reasonMachine
	}
	return ""
}

// CheckKey returns why the key at path may not appear in a file of the
// given layer, or "" when it may. path holds the keys from the top of the
// document (for a repository entry, from the top of the entry). A key
// that is forbidden is reported as forbidden before it is reported as
// unknown, so the error always names a forbidden key.
func CheckKey(layer Layer, path []string) string {
	name := path[len(path)-1]
	if reason := forbiddenReason(layer, name); reason != "" {
		if reason == MessageOTLPHeaders {
			return reason
		}
		return "forbidden key " + quote(name) + ": " + reason
	}
	if len(path) == 2 && path[0] == KeyExtensionAllowance {
		if name == KeyAllowanceMinutes || name == KeyAllowanceTimes {
			return ""
		}
		return "unknown key " + quote(strings.Join(path, "."))
	}
	if len(path) == 2 && path[0] == KeyAzureDevOpsProjects {
		if name == KeyProject || name == KeyDefaultRepository {
			return ""
		}
		return "unknown key " + quote(strings.Join(path, "."))
	}
	if len(path) == 2 && path[0] == KeyProviderPollIntervals {
		// The keys are connection names, which are checked with the
		// values.
		return ""
	}
	if len(path) > 1 {
		return "unknown key " + quote(strings.Join(path, "."))
	}
	if layer == LayerGlobal && name == KeyRepositories {
		return ""
	}
	info, ok := settingInfo(name)
	if !ok {
		return "unknown key " + quote(name)
	}
	if !slices.Contains(info.Layers, layer) {
		return "key " + quote(name) + " can be set only in the " + where(info.Layers)
	}
	return ""
}

// where names the files of the layers, for an error message.
func where(layers []Layer) string {
	var names []string
	for _, l := range layers {
		switch l {
		case LayerLocalEntry:
			names = append(names, "repository entries")
		case LayerGlobal:
			names = append(names, "global configuration")
		}
	}
	if len(names) == 0 {
		return "configuration"
	}
	return strings.Join(names, " or ")
}

func quote(s string) string {
	return `"` + s + `"`
}

// LooksLikeSecret reports whether a configuration value matches one of the
// built-in secret patterns. Only the built-in patterns are used, so that
// an ordinary long string is not mistaken for a secret.
func LooksLikeSecret(value string) bool {
	for _, re := range builtinSecretPatterns {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

// SecretValueMessage is the error for a value that looks like a secret.
func SecretValueMessage(key string) string {
	return "key " + quote(key) + " holds a value that looks like a secret"
}

// ExtensionAllowanceSetting is the extension-allowance setting as written
// in a file. Times is a pointer so that an omitted times can default to 1.
type ExtensionAllowanceSetting struct {
	Minutes int  `yaml:"minutes"`
	Times   *int `yaml:"times"`
}

// Allowance returns the domain allowance. When minutes are set and times
// is omitted, the allowance applies once.
func (a ExtensionAllowanceSetting) Allowance() ExtensionAllowance {
	times := 0
	switch {
	case a.Times != nil:
		times = *a.Times
	case a.Minutes > 0:
		times = 1
	}
	return ExtensionAllowance{MaxMinutes: a.Minutes, Times: times}
}

// SettingValues holds what one layer sets. A nil pointer or a nil slice
// means the layer does not set that setting, so the next layer applies;
// a pointer to a zero value is a value like any other. Budgets are in
// minutes and percentages are whole numbers. Which fields a layer may
// set is given by AllSettings and checked by CheckKey when the file is read.
type SettingValues struct {
	OptInLabel                *string                    `yaml:"opt-in-label"`
	HoldLabel                 *string                    `yaml:"hold-label"`
	ClaimLabel                *string                    `yaml:"claim-label"`
	Budget                    *int                       `yaml:"budget"`
	FollowUpBudget            *int                       `yaml:"follow-up-budget"`
	MaxBaseBudget             *int                       `yaml:"max-base-budget"`
	WarningThreshold          *int                       `yaml:"warning-threshold"`
	ExtensionAllowance        *ExtensionAllowanceSetting `yaml:"extension-allowance"`
	Ceiling                   *int                       `yaml:"ceiling"`
	BranchPrefix              *string                    `yaml:"branch-prefix"`
	BaseBranch                *string                    `yaml:"base-branch"`
	ProtectedBranches         []string                   `yaml:"protected-branches"`
	ShepherdScope             *ShepherdScope             `yaml:"shepherd-scope"`
	ReviewRunner              *Runner                    `yaml:"review-runner"`
	RoundLimit                *int                       `yaml:"round-limit"`
	AutoFollowUp              *bool                      `yaml:"auto-follow-up"`
	FollowUpLimit             *int                       `yaml:"follow-up-limit"`
	AutoResume                *bool                      `yaml:"auto-resume"`
	VerificationCommand       *string                    `yaml:"verification-command"`
	RequireAcceptanceCriteria *bool                      `yaml:"require-acceptance-criteria"`
	AgentInstructions         *string                    `yaml:"agent-instructions"`

	Runner            *Runner        `yaml:"runner"`
	Capacity          *int           `yaml:"capacity"`
	Reviewing         *bool          `yaml:"reviewing"`
	PollInterval      *time.Duration `yaml:"poll-interval"`
	Lease             *time.Duration `yaml:"lease"`
	GracePeriod       *time.Duration `yaml:"grace-period"`
	ExtensionWait     *time.Duration `yaml:"extension-wait"`
	QuestionStaleness *time.Duration `yaml:"question-staleness"`
	OTLPEndpoint      *string        `yaml:"otlp-endpoint"`
	SecretPatterns    []string       `yaml:"secret-patterns"`
	WorktreeRoot      *string        `yaml:"worktree-root"`
	// AzureDevOpsProjects is nil when the layer does not set it.
	AzureDevOpsProjects []AzureDevOpsProject `yaml:"azure-devops-projects"`
	// ProviderPollIntervals maps connection names, such as github or
	// azure-devops/contoso, to their poll interval. It is nil when the
	// layer does not set it.
	ProviderPollIntervals map[string]time.Duration `yaml:"provider-poll-intervals"`
}

// AzureDevOpsProject is one entry of azure-devops-projects: the target
// repository of the project's work items that carry no repo: label.
type AzureDevOpsProject struct {
	// Project is organization/project.
	Project string `yaml:"project"`
	// DefaultRepository is the name of a Git repository of the project.
	DefaultRepository string `yaml:"default-repository"`
}

// Repository returns the project as a work item repository
// (organization/project, no name) in lowercase, because Azure DevOps
// names ignore case.
func (p AzureDevOpsProject) Repository() (Repository, error) {
	org, project, ok := strings.Cut(p.Project, "/")
	if !ok || strings.TrimSpace(org) == "" || strings.TrimSpace(project) == "" || strings.Contains(project, "/") {
		return Repository{}, fmt.Errorf("project %q must be organization/project", p.Project)
	}
	return Repository{Provider: AzureDevOps, Org: strings.ToLower(org), Project: strings.ToLower(project)}, nil
}

// RepositoryEntry is one local repository entry: a repository this
// machine works on, where its clone is, and its local overrides.
type RepositoryEntry struct {
	// Provider is "github" or "azure-devops", as written in the file.
	Provider string `yaml:"provider"`
	// Remote is owner/name on GitHub and organization/project/repository
	// on Azure DevOps.
	Remote string `yaml:"remote"`
	// Path is the absolute path of the local clone.
	Path     string        `yaml:"path"`
	Settings SettingValues `yaml:",inline"`
}

// The provider names used in configuration files.
const (
	ConfigGitHub      = "github"
	ConfigAzureDevOps = "azure-devops"
)

// Repository returns the repository the entry names, or an error when
// the provider or the remote is not valid.
func (e RepositoryEntry) Repository() (Repository, error) {
	var r Repository
	parts := strings.Split(e.Remote, "/")
	switch e.Provider {
	case ConfigGitHub:
		r.Provider = GitHub
		if len(parts) != 2 {
			return r, fmt.Errorf("remote %q must be owner/name", e.Remote)
		}
		r.Org, r.Name = parts[0], parts[1]
	case ConfigAzureDevOps:
		r.Provider = AzureDevOps
		if len(parts) != 3 {
			return r, fmt.Errorf("remote %q must be organization/project/repository", e.Remote)
		}
		r.Org, r.Project, r.Name = parts[0], parts[1], parts[2]
	case "":
		return r, fmt.Errorf("provider is missing")
	default:
		return r, fmt.Errorf("provider %q must be %q or %q", e.Provider, ConfigGitHub, ConfigAzureDevOps)
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return r, fmt.Errorf("remote %q has an empty part", e.Remote)
		}
	}
	return r, nil
}
