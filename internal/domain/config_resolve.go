package domain

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"
)

// ConfigLayers holds the file layers of one repository's configuration.
// A nil layer is absent. Item labels are applied afterwards with
// Effective.ForItem, because they need the effective maximum base budget.
type ConfigLayers struct {
	RepositoryFile *SettingValues
	LocalEntry     *SettingValues
	Global         *SettingValues
}

// Effective is the value of every setting for one repository on this
// machine, with the layer each value came from. Budgets and intervals are
// durations; percentages are whole numbers.
type Effective struct {
	OptInLabel                string
	HoldLabel                 string
	ClaimLabel                string
	Budget                    time.Duration
	FollowUpBudget            int
	MaxBaseBudget             time.Duration
	WarningThreshold          int
	ExtensionAllowance        ExtensionAllowance
	Ceiling                   int
	BranchPrefix              string
	BaseBranch                string // "" means the remote's default branch
	ProtectedBranches         []string
	ShepherdScope             ShepherdScope
	ReviewRunner              Runner
	RoundLimit                int
	AutoFollowUp              bool
	FollowUpLimit             int
	AutoResume                bool
	VerificationCommand       string // "" means none
	RequireAcceptanceCriteria bool
	AgentInstructions         string // "" means none

	Runner            Runner
	Capacity          int
	Reviewing         bool
	PollInterval      time.Duration
	Lease             time.Duration
	GracePeriod       time.Duration
	ExtensionWait     time.Duration
	QuestionStaleness time.Duration
	OTLPEndpoint      string // "" means telemetry export is off
	SecretPatterns    []string
	WorktreeRoot      string // "" means DefaultWorktreeRoot under the user's home

	sources map[string]Layer
	shown   map[string]string
}

// layerValues pairs a layer with what it sets.
type layerValues struct {
	layer  Layer
	values *SettingValues
}

// resolver records the source and the printed value of each setting.
type resolver struct {
	layers []layerValues
	e      *Effective
}

func (r resolver) record(key string, source Layer, shown string) {
	r.e.sources[key] = source
	r.e.shown[key] = shown
}

// resolveValue returns the value of the highest layer that sets key, or
// def, and records where it came from.
func resolveValue[T any](r resolver, key string, def T, get func(*SettingValues) *T, show func(T) string) T {
	for _, l := range r.layers {
		if p := get(l.values); p != nil {
			r.record(key, l.layer, show(*p))
			return *p
		}
	}
	r.record(key, LayerDefault, show(def))
	return def
}

// resolveList is resolveValue for lists, where nil means not set.
func resolveList(r resolver, key string, get func(*SettingValues) []string) []string {
	for _, l := range r.layers {
		if v := get(l.values); v != nil {
			r.record(key, l.layer, showList(v))
			return v
		}
	}
	r.record(key, LayerDefault, showList(nil))
	return nil
}

func showString(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func showMinutes(m int) string { return strconv.Itoa(m) + " min" }

func showPercent(p int) string { return strconv.Itoa(p) + "%" }

func showFactor(f int) string { return strconv.Itoa(f) + "×" }

// showDuration prints 2m rather than 2m0s, and 72h rather than 72h0m0s.
func showDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func showList(v []string) string {
	if len(v) == 0 {
		return "none"
	}
	return strings.Join(v, ", ")
}

func showAllowance(a ExtensionAllowance) string {
	if a.MaxMinutes == 0 || a.Times == 0 {
		return "0 min"
	}
	return fmt.Sprintf("%d min, %d times", a.MaxMinutes, a.Times)
}

func minutes(d time.Duration) int { return int(d / time.Minute) }

// Resolve combines the layers with the built-in defaults: for each
// setting, the highest layer that sets it wins. Turning reviewing off in
// the global configuration turns it off for every repository.
func (l ConfigLayers) Resolve() Effective {
	e := Effective{sources: map[string]Layer{}, shown: map[string]string{}}
	r := resolver{e: &e}
	for _, lv := range []layerValues{
		{LayerRepositoryFile, l.RepositoryFile},
		{LayerLocalEntry, l.LocalEntry},
		{LayerGlobal, l.Global},
	} {
		if lv.values != nil {
			r.layers = append(r.layers, lv)
		}
	}

	str := func(key, def string, get func(*SettingValues) *string) string {
		return resolveValue(r, key, def, get, showString)
	}
	e.OptInLabel = str(KeyOptInLabel, DefaultOptInLabel, func(s *SettingValues) *string { return s.OptInLabel })
	e.HoldLabel = str(KeyHoldLabel, DefaultHoldLabel, func(s *SettingValues) *string { return s.HoldLabel })
	e.ClaimLabel = str(KeyClaimLabel, DefaultClaimLabel, func(s *SettingValues) *string { return s.ClaimLabel })
	e.Budget = time.Duration(resolveValue(r, KeyBudget, minutes(DefaultBaseBudget),
		func(s *SettingValues) *int { return s.Budget }, showMinutes)) * time.Minute
	e.FollowUpBudget = resolveValue(r, KeyFollowUpBudget, DefaultFollowUpPercent,
		func(s *SettingValues) *int { return s.FollowUpBudget }, showPercent)
	e.MaxBaseBudget = time.Duration(resolveValue(r, KeyMaxBaseBudget, minutes(DefaultMaxBaseBudget),
		func(s *SettingValues) *int { return s.MaxBaseBudget }, showMinutes)) * time.Minute
	e.WarningThreshold = resolveValue(r, KeyWarningThreshold, DefaultWarningPercent,
		func(s *SettingValues) *int { return s.WarningThreshold }, showPercent)
	e.ExtensionAllowance = resolveValue(r, KeyExtensionAllowance, ExtensionAllowance{},
		func(s *SettingValues) *ExtensionAllowance {
			if s.ExtensionAllowance == nil {
				return nil
			}
			a := s.ExtensionAllowance.Allowance()
			return &a
		}, showAllowance)
	e.Ceiling = resolveValue(r, KeyCeiling, DefaultCeilingFactor,
		func(s *SettingValues) *int { return s.Ceiling }, showFactor)
	e.BranchPrefix = str(KeyBranchPrefix, DefaultBranchPrefix, func(s *SettingValues) *string { return s.BranchPrefix })
	e.BaseBranch = str(KeyBaseBranch, "", func(s *SettingValues) *string { return s.BaseBranch })
	e.ProtectedBranches = resolveList(r, KeyProtectedBranches, func(s *SettingValues) []string { return s.ProtectedBranches })
	e.ShepherdScope = resolveValue(r, KeyShepherdScope, DefaultShepherdScope,
		func(s *SettingValues) *ShepherdScope { return s.ShepherdScope },
		func(v ShepherdScope) string { return string(v) })
	e.RoundLimit = resolveValue(r, KeyRoundLimit, DefaultRoundLimit,
		func(s *SettingValues) *int { return s.RoundLimit }, strconv.Itoa)
	e.AutoFollowUp = resolveValue(r, KeyAutoFollowUp, DefaultAutoFollowUp,
		func(s *SettingValues) *bool { return s.AutoFollowUp }, strconv.FormatBool)
	e.FollowUpLimit = resolveValue(r, KeyFollowUpLimit, DefaultFollowUpLimit,
		func(s *SettingValues) *int { return s.FollowUpLimit }, strconv.Itoa)
	e.AutoResume = resolveValue(r, KeyAutoResume, DefaultAutoResume,
		func(s *SettingValues) *bool { return s.AutoResume }, strconv.FormatBool)
	e.VerificationCommand = str(KeyVerificationCommand, "", func(s *SettingValues) *string { return s.VerificationCommand })
	e.RequireAcceptanceCriteria = resolveValue(r, KeyRequireAcceptanceCriteria, false,
		func(s *SettingValues) *bool { return s.RequireAcceptanceCriteria }, strconv.FormatBool)
	e.AgentInstructions = str(KeyAgentInstructions, "", func(s *SettingValues) *string { return s.AgentInstructions })

	showRunner := func(v Runner) string { return string(v) }
	e.Runner = resolveValue(r, KeyRunner, DefaultRunner, func(s *SettingValues) *Runner { return s.Runner }, showRunner)
	// The review runner defaults to the ant runner.
	e.ReviewRunner = resolveValue(r, KeyReviewRunner, e.Runner, func(s *SettingValues) *Runner { return s.ReviewRunner }, showRunner)
	e.Capacity = resolveValue(r, KeyCapacity, DefaultCapacity,
		func(s *SettingValues) *int { return s.Capacity }, strconv.Itoa)
	e.Reviewing = resolveValue(r, KeyReviewing, DefaultReviewing,
		func(s *SettingValues) *bool { return s.Reviewing }, strconv.FormatBool)
	if l.Global != nil && l.Global.Reviewing != nil && !*l.Global.Reviewing {
		e.Reviewing = false
		r.record(KeyReviewing, LayerGlobal, strconv.FormatBool(false))
	}
	dur := func(key string, def time.Duration, get func(*SettingValues) *time.Duration) time.Duration {
		return resolveValue(r, key, def, get, showDuration)
	}
	e.PollInterval = dur(KeyPollInterval, DefaultPollInterval, func(s *SettingValues) *time.Duration { return s.PollInterval })
	e.Lease = dur(KeyLease, DefaultLease, func(s *SettingValues) *time.Duration { return s.Lease })
	e.GracePeriod = dur(KeyGracePeriod, DefaultGracePeriod, func(s *SettingValues) *time.Duration { return s.GracePeriod })
	e.ExtensionWait = dur(KeyExtensionWait, DefaultExtensionWait, func(s *SettingValues) *time.Duration { return s.ExtensionWait })
	e.QuestionStaleness = dur(KeyQuestionStaleness, DefaultQuestionStaleness, func(s *SettingValues) *time.Duration { return s.QuestionStaleness })
	e.OTLPEndpoint = str(KeyOTLPEndpoint, "", func(s *SettingValues) *string { return s.OTLPEndpoint })
	e.SecretPatterns = resolveList(r, KeySecretPatterns, func(s *SettingValues) []string { return s.SecretPatterns })
	e.WorktreeRoot = resolveValue(r, KeyWorktreeRoot, "", func(s *SettingValues) *string { return s.WorktreeRoot },
		func(v string) string {
			if v == "" {
				return DefaultWorktreeRootText
			}
			return v
		})
	return e
}

// Defaults returns the built-in defaults: the effective configuration
// when no layer sets anything.
func Defaults() Effective {
	return ConfigLayers{}.Resolve()
}

// DefaultText is the default of a setting as docs/configuration.md shows
// it.
func DefaultText(key string) string {
	switch key {
	case KeyReviewRunner:
		return "same as runner"
	case KeyProvider, KeyRemote, KeyPath:
		return "required"
	}
	return Defaults().shown[key]
}

// ForItem applies an item's setting labels: agent: sets the runner,
// budget: the budget (capped at the maximum base budget) and base: the
// base branch. It returns the warnings for labels that were ignored or
// changed.
func (e Effective) ForItem(labels []string) (Effective, []LabelWarning) {
	item, warnings := ParseSettingLabels(labels, minutes(e.MaxBaseBudget))
	e.sources = maps.Clone(e.sources)
	e.shown = maps.Clone(e.shown)
	r := resolver{e: &e}
	if item.Agent != "" {
		if runner := Runner(strings.ToLower(item.Agent)); runner.Valid() {
			e.Runner = runner
			r.record(KeyRunner, LayerLabel, string(runner))
		} else {
			label := labelAgent + item.Agent
			warnings = append(warnings, LabelWarning{
				Label:   label,
				Reason:  WarnInvalid,
				Message: fmt.Sprintf("label %q ignored: unknown runner", label),
			})
		}
	}
	if item.BudgetMinutes > 0 {
		e.Budget = time.Duration(item.BudgetMinutes) * time.Minute
		r.record(KeyBudget, LayerLabel, showMinutes(item.BudgetMinutes))
	}
	if item.Base != "" {
		e.BaseBranch = item.Base
		r.record(KeyBaseBranch, LayerLabel, item.Base)
	}
	return e, warnings
}

// Source returns the layer a setting's value came from.
func (e Effective) Source(key string) Layer {
	return e.sources[key]
}

// SettingValue is one row of an inspection: a setting, its printed value
// and its source.
type SettingValue struct {
	Key    string
	Value  string
	Source Layer
}

// Inspect lists every setting with its effective value and source, in the
// order of AllSettings. The repository entry's identity is left out.
func (e Effective) Inspect() []SettingValue {
	var out []SettingValue
	for _, s := range settingInfos {
		source, ok := e.sources[s.Key]
		if !ok {
			continue
		}
		out = append(out, SettingValue{Key: s.Key, Value: e.shown[s.Key], Source: source})
	}
	return out
}

// BudgetPolicy returns the settings that shape every budget.
func (e Effective) BudgetPolicy() BudgetPolicy {
	return BudgetPolicy{
		WarningPercent: e.WarningThreshold,
		CeilingFactor:  e.Ceiling,
		Grace:          e.GracePeriod,
		AutoApprove:    e.ExtensionAllowance,
	}
}
