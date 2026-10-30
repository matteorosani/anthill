package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The prefixes of the per-item setting labels.
const (
	labelAgent  = "agent:"
	labelBudget = "budget:"
	labelBase   = "base:"
	labelRepo   = "repo:"
)

// ItemSettings holds what the per-item setting labels set. An empty string
// or a zero budget means the label is absent, so the next source in each
// setting's order applies.
type ItemSettings struct {
	Agent         string
	BudgetMinutes int
	Base          string
	Repo          string
}

// WarningReason tells why a setting label was not used as written.
type WarningReason string

// The warning reasons.
const (
	// WarnInvalid: the value is not valid, so the label is ignored.
	WarnInvalid WarningReason = "invalid"
	// WarnConflict: labels of the same kind have different values, so all
	// of them are ignored.
	WarnConflict WarningReason = "conflict"
	// WarnCapped: the budget is above the maximum base budget, so the
	// maximum is used instead.
	WarnCapped WarningReason = "capped"
)

// LabelWarning is shown to the queen when a setting label was ignored or
// changed. The domain returns it instead of logging it.
type LabelWarning struct {
	Label   string
	Reason  WarningReason
	Message string
}

// ParseSettingLabels reads the per-item setting labels (agent:, budget:,
// base:, repo:) from an item's labels and ignores all others. Prefixes
// match in any case and values are trimmed. A budget above maxBudget
// minutes is capped at maxBudget; maxBudget of 0 means no maximum.
func ParseSettingLabels(labels []string, maxBudget int) (ItemSettings, []LabelWarning) {
	var warnings []LabelWarning
	// Valid values per prefix, in label order, without duplicates.
	values := map[string][]string{}
	sources := map[string][]string{}

	for _, label := range labels {
		prefix, value, ok := settingPrefix(label)
		if !ok {
			continue
		}
		if reason := validateSetting(prefix, value); reason != "" {
			warnings = append(warnings, LabelWarning{
				Label:   label,
				Reason:  WarnInvalid,
				Message: fmt.Sprintf("label %q ignored: %s", label, reason),
			})
			continue
		}
		sources[prefix] = append(sources[prefix], label)
		if !slices.ContainsFunc(values[prefix], func(v string) bool { return sameSetting(prefix, v, value) }) {
			values[prefix] = append(values[prefix], value)
		}
	}

	// pick returns the single value for a prefix, or "" with a warning
	// when the labels disagree.
	pick := func(prefix string) string {
		v := values[prefix]
		switch len(v) {
		case 0:
			return ""
		case 1:
			return v[0]
		}
		warnings = append(warnings, LabelWarning{
			Label:   strings.Join(sources[prefix], ", "),
			Reason:  WarnConflict,
			Message: fmt.Sprintf("conflicting labels %s ignored", strings.Join(sources[prefix], ", ")),
		})
		return ""
	}

	settings := ItemSettings{
		Agent: pick(labelAgent),
		Base:  pick(labelBase),
		Repo:  pick(labelRepo),
	}
	if budget := pick(labelBudget); budget != "" {
		// validateSetting already checked that it is a positive integer.
		minutes, _ := strconv.Atoi(budget)
		if maxBudget > 0 && minutes > maxBudget {
			warnings = append(warnings, LabelWarning{
				Label:   sources[labelBudget][0],
				Reason:  WarnCapped,
				Message: fmt.Sprintf("label %q capped at the maximum base budget of %d minutes", sources[labelBudget][0], maxBudget),
			})
			minutes = maxBudget
		}
		settings.BudgetMinutes = minutes
	}
	return settings, warnings
}

// sameSetting reports whether two values of a setting label name the same
// thing. Repository names and runner names ignore case on every provider;
// branch names do not.
func sameSetting(prefix, a, b string) bool {
	if prefix == labelBase {
		return a == b
	}
	return strings.EqualFold(a, b)
}

// settingPrefix returns the lowercase setting prefix and the trimmed value
// of a setting label, or false for any other label.
func settingPrefix(label string) (prefix, value string, ok bool) {
	for _, p := range []string{labelAgent, labelBudget, labelBase, labelRepo} {
		if len(label) >= len(p) && strings.EqualFold(label[:len(p)], p) {
			return p, strings.TrimSpace(label[len(p):]), true
		}
	}
	return "", "", false
}

// validateSetting returns why value is not valid for prefix, or "" when it
// is valid.
func validateSetting(prefix, value string) string {
	if value == "" {
		return "the value is empty"
	}
	switch prefix {
	case labelBudget:
		for _, c := range value {
			if c < '0' || c > '9' {
				return "the budget must be a whole number of minutes"
			}
		}
		minutes, err := strconv.Atoi(value)
		if err != nil {
			return "the budget is too large"
		}
		if minutes == 0 {
			return "the budget must be more than 0 minutes"
		}
	case labelBase:
		if err := validateRefName(value); err != nil {
			return err.Error()
		}
	}
	return ""
}

// validateRefName checks the git branch name rules that matter for a base
// branch or a branch prefix (see git check-ref-format).
func validateRefName(name string) error {
	switch {
	case name == "" || name == "@":
		return fmt.Errorf("%q is not a valid branch name", name)
	case strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/"):
		return fmt.Errorf("branch name %q must not start with %q", name, name[:1])
	case strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock"):
		return fmt.Errorf("branch name %q has an invalid ending", name)
	case strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{"):
		return fmt.Errorf("branch name %q contains an invalid sequence", name)
	}
	for _, c := range name {
		if c <= ' ' || c == 0x7f || strings.ContainsRune("~^:?*[\\", c) {
			return fmt.Errorf("branch name %q contains the invalid character %q", name, c)
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("branch name %q has a part starting with \".\"", name)
		}
	}
	return nil
}
