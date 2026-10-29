package domain

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ConfigError is one problem in a configuration. File is the file or the
// layer it was found in, Key the setting, and Message what is wrong. Find
// it in a returned error with errors.As.
type ConfigError struct {
	File    string
	Key     string
	Message string
}

// Error prints the error as "<file>: <key>: <message>", leaving out the
// parts that are empty.
func (e *ConfigError) Error() string {
	var parts []string
	for _, p := range []string{e.File, e.Key, e.Message} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ": ")
}

// MessageWarningThreshold is the error for a warning threshold out of
// range (spec anthill-configuration, "Validation at startup and reload").
const MessageWarningThreshold = "warning threshold must be between 1 and 99 percent"

// MessageBudgetOverMaximum is the error for a budget above the maximum
// base budget.
const MessageBudgetOverMaximum = "budget must not exceed the maximum base budget"

// settingCheck checks one setting and returns the message for a bad
// value, or "".
type settingCheck struct {
	key   string
	check func(*SettingValues) string
}

func checkLabel(name string, v *string) string {
	if v != nil && strings.TrimSpace(*v) == "" {
		return name + " must not be empty"
	}
	return ""
}

func checkPositive(name, unit string, v *int) string {
	if v != nil && *v <= 0 {
		return fmt.Sprintf("%s must be more than 0%s", name, unit)
	}
	return ""
}

func checkNotNegative(name string, v *int) string {
	if v != nil && *v < 0 {
		return name + " must not be negative"
	}
	return ""
}

func checkDuration(name string, v *time.Duration) string {
	if v != nil && *v <= 0 {
		return name + " must be more than 0"
	}
	return ""
}

func checkRunner(name string, v *Runner) string {
	if v != nil && !v.Valid() {
		return fmt.Sprintf("%s must be %q, %q or %q", name, RunnerClaudeCode, RunnerCodex, RunnerOpenCode)
	}
	return ""
}

func checkBranch(v *string) string {
	if v == nil {
		return ""
	}
	if err := validateRefName(*v); err != nil {
		return err.Error()
	}
	return ""
}

var settingChecks = []settingCheck{
	{KeyOptInLabel, func(s *SettingValues) string { return checkLabel("opt-in label", s.OptInLabel) }},
	{KeyHoldLabel, func(s *SettingValues) string { return checkLabel("hold label", s.HoldLabel) }},
	{KeyClaimLabel, func(s *SettingValues) string { return checkLabel("claim label", s.ClaimLabel) }},
	{KeyBudget, func(s *SettingValues) string { return checkPositive("budget", " minutes", s.Budget) }},
	{KeyFollowUpBudget, func(s *SettingValues) string {
		if v := s.FollowUpBudget; v != nil && (*v < 1 || *v > 100) {
			return "follow-up budget must be between 1 and 100 percent"
		}
		return ""
	}},
	{KeyMaxBaseBudget, func(s *SettingValues) string {
		return checkPositive("maximum base budget", " minutes", s.MaxBaseBudget)
	}},
	{KeyWarningThreshold, func(s *SettingValues) string {
		if v := s.WarningThreshold; v != nil && (*v < 1 || *v > 99) {
			return MessageWarningThreshold
		}
		return ""
	}},
	{KeyExtensionAllowance, func(s *SettingValues) string {
		a := s.ExtensionAllowance
		switch {
		case a == nil:
			return ""
		case a.Minutes < 0:
			return "extension allowance minutes must not be negative"
		case a.Times != nil && *a.Times < 0:
			return "extension allowance times must not be negative"
		}
		return ""
	}},
	{KeyCeiling, func(s *SettingValues) string {
		if v := s.Ceiling; v != nil && *v < 1 {
			return "ceiling must be at least 1"
		}
		return ""
	}},
	{KeyBranchPrefix, func(s *SettingValues) string { return checkBranch(s.BranchPrefix) }},
	{KeyBaseBranch, func(s *SettingValues) string { return checkBranch(s.BaseBranch) }},
	{KeyProtectedBranches, func(s *SettingValues) string {
		for _, p := range s.ProtectedBranches {
			if _, err := path.Match(p, ""); p == "" || err != nil {
				return fmt.Sprintf("protected branch pattern %q is not valid", p)
			}
		}
		return ""
	}},
	{KeyShepherdScope, func(s *SettingValues) string {
		if v := s.ShepherdScope; v != nil && !v.Valid() {
			return fmt.Sprintf("shepherd scope must be %q, %q or %q", ScopeAntAuthored, ScopeLabelled, ScopeAll)
		}
		return ""
	}},
	{KeyReviewRunner, func(s *SettingValues) string { return checkRunner("review runner", s.ReviewRunner) }},
	{KeyRoundLimit, func(s *SettingValues) string { return checkNotNegative("round limit", s.RoundLimit) }},
	{KeyFollowUpLimit, func(s *SettingValues) string { return checkNotNegative("follow-up limit", s.FollowUpLimit) }},
	{KeyVerificationCommand, func(s *SettingValues) string { return checkLabel("verification command", s.VerificationCommand) }},
	{KeyAgentInstructions, func(s *SettingValues) string {
		v := s.AgentInstructions
		if v != nil && !filepath.IsLocal(filepath.FromSlash(*v)) {
			return "agent instructions file must be a relative path inside the repository"
		}
		return ""
	}},
	{KeyRunner, func(s *SettingValues) string { return checkRunner("runner", s.Runner) }},
	{KeyCapacity, func(s *SettingValues) string {
		if v := s.Capacity; v != nil && *v < 1 {
			return "capacity must be at least 1"
		}
		return ""
	}},
	{KeyPollInterval, func(s *SettingValues) string { return checkDuration("poll interval", s.PollInterval) }},
	{KeyLease, func(s *SettingValues) string { return checkDuration("lease", s.Lease) }},
	{KeyGracePeriod, func(s *SettingValues) string { return checkDuration("grace period", s.GracePeriod) }},
	{KeyExtensionWait, func(s *SettingValues) string { return checkDuration("extension wait", s.ExtensionWait) }},
	{KeyQuestionStaleness, func(s *SettingValues) string { return checkDuration("question staleness", s.QuestionStaleness) }},
	{KeyOTLPEndpoint, func(s *SettingValues) string {
		if s.OTLPEndpoint == nil {
			return ""
		}
		u, err := url.Parse(*s.OTLPEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "OTLP endpoint must be an http or https URL"
		}
		return ""
	}},
	{KeySecretPatterns, func(s *SettingValues) string {
		for _, p := range s.SecretPatterns {
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Sprintf("secret pattern %q is not valid: %v", p, err)
			}
		}
		return ""
	}},
}

// ValidateSettings checks the values one layer sets, and returns one
// *ConfigError per bad value, naming file. Which keys a layer may set is
// checked when the file is read (CheckKey); a value check does not depend
// on the layer.
func ValidateSettings(file string, s *SettingValues) []error {
	return validateSettings(file, "", s)
}

func validateSettings(file, prefix string, s *SettingValues) []error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, c := range settingChecks {
		if msg := c.check(s); msg != "" {
			errs = append(errs, &ConfigError{File: file, Key: prefix + c.key, Message: msg})
		}
	}
	// The budget must fit the maximum when the same layer sets both.
	if s.Budget != nil && s.MaxBaseBudget != nil && *s.Budget > 0 && *s.Budget > *s.MaxBaseBudget {
		errs = append(errs, &ConfigError{File: file, Key: prefix + KeyBudget, Message: MessageBudgetOverMaximum})
	}
	return errs
}

// ValidateEntry checks a local repository entry: its provider and remote,
// that its path is absolute, and the values it sets. file names the
// configuration file and index the entry's place in it.
func ValidateEntry(file string, index int, e RepositoryEntry) []error {
	prefix := fmt.Sprintf("%s[%d].", KeyRepositories, index)
	var errs []error
	if _, err := e.Repository(); err != nil {
		errs = append(errs, &ConfigError{File: file, Key: prefix + KeyRemote, Message: err.Error()})
	}
	switch {
	case e.Path == "":
		errs = append(errs, &ConfigError{File: file, Key: prefix + KeyPath, Message: "path is missing"})
	case !filepath.IsAbs(e.Path):
		errs = append(errs, &ConfigError{File: file, Key: prefix + KeyPath, Message: fmt.Sprintf("path %q must be absolute", e.Path)})
	}
	return append(errs, validateSettings(file, prefix, &e.Settings)...)
}

// Validate checks the rules that span layers, such as a budget from one
// layer above the maximum base budget from another. Each error names the
// layer of the offending value.
func (e Effective) Validate() []error {
	if e.Budget > e.MaxBaseBudget {
		return []error{&ConfigError{File: string(e.Source(KeyBudget)), Key: KeyBudget, Message: MessageBudgetOverMaximum}}
	}
	return nil
}
