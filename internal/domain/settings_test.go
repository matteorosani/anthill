package domain_test

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestParseSettingLabels(t *testing.T) {
	const maxBudget = 180
	tests := []struct {
		name     string
		labels   []string
		want     domain.ItemSettings
		warnings []domain.WarningReason
	}{
		{
			name:   "setting prefix in another case: Budget:30",
			labels: []string{"Anthill", "Budget:30"},
			want:   domain.ItemSettings{BudgetMinutes: 30},
		},
		{
			name:   "repo: values that differ only in case agree",
			labels: []string{"repo:Web", "repo:web"},
			want:   domain.ItemSettings{Repo: "Web"},
		},
		{
			name:   "no setting labels",
			labels: []string{"anthill", "bug"},
		},
		{
			name:   "label overrides repository default: agent:codex",
			labels: []string{"anthill", "agent:codex"},
			want:   domain.ItemSettings{Agent: "codex"},
		},
		{
			name:   "any agent name is accepted",
			labels: []string{"agent:my-runner"},
			want:   domain.ItemSettings{Agent: "my-runner"},
		},
		{
			name:   "budget label: budget:90",
			labels: []string{"budget:90"},
			want:   domain.ItemSettings{BudgetMinutes: 90},
		},
		{
			name:     "budget label above the maximum: budget:600",
			labels:   []string{"budget:600"},
			want:     domain.ItemSettings{BudgetMinutes: 180},
			warnings: []domain.WarningReason{domain.WarnCapped},
		},
		{
			name:     "invalid budget label: budget:abc",
			labels:   []string{"budget:abc"},
			warnings: []domain.WarningReason{domain.WarnInvalid},
		},
		{
			name:     "zero budget",
			labels:   []string{"budget:0"},
			warnings: []domain.WarningReason{domain.WarnInvalid},
		},
		{
			name:     "negative budget",
			labels:   []string{"budget:-5"},
			warnings: []domain.WarningReason{domain.WarnInvalid},
		},
		{
			name:   "ticket targets a release branch: base:release/2.4",
			labels: []string{"base:release/2.4"},
			want:   domain.ItemSettings{Base: "release/2.4"},
		},
		{
			name:     "invalid base branch",
			labels:   []string{"base:feature..x", "base:-x", "base:a b"},
			warnings: []domain.WarningReason{domain.WarnInvalid, domain.WarnInvalid, domain.WarnInvalid},
		},
		{
			name:   "repo tag",
			labels: []string{"repo:web"},
			want:   domain.ItemSettings{Repo: "web"},
		},
		{
			name:   "prefix in any case, value trimmed and keeps its case",
			labels: []string{"Agent: Codex", "BUDGET:45"},
			want:   domain.ItemSettings{Agent: "Codex", BudgetMinutes: 45},
		},
		{
			name:     "empty value",
			labels:   []string{"repo:"},
			warnings: []domain.WarningReason{domain.WarnInvalid},
		},
		{
			name:     "conflicting values are all ignored",
			labels:   []string{"budget:30", "budget:90", "agent:codex"},
			want:     domain.ItemSettings{Agent: "codex"},
			warnings: []domain.WarningReason{domain.WarnConflict},
		},
		{
			name:   "identical duplicates count as one",
			labels: []string{"budget:30", "budget:30"},
			want:   domain.ItemSettings{BudgetMinutes: 30},
		},
		{
			name:     "an invalid label does not conflict with a valid one",
			labels:   []string{"budget:abc", "budget:90"},
			want:     domain.ItemSettings{BudgetMinutes: 90},
			warnings: []domain.WarningReason{domain.WarnInvalid},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := domain.ParseSettingLabels(tt.labels, maxBudget)
			if got != tt.want {
				t.Errorf("settings = %+v, want %+v", got, tt.want)
			}
			if len(warnings) != len(tt.warnings) {
				t.Fatalf("warnings = %+v, want reasons %v", warnings, tt.warnings)
			}
			for i, w := range warnings {
				if w.Reason != tt.warnings[i] {
					t.Errorf("warning %d reason = %q, want %q", i, w.Reason, tt.warnings[i])
				}
				if w.Message == "" || w.Label == "" {
					t.Errorf("warning %d has no label or message: %+v", i, w)
				}
			}
		})
	}
}

func TestParseSettingLabelsWithoutMaximum(t *testing.T) {
	got, warnings := domain.ParseSettingLabels([]string{"budget:600"}, 0)
	if got.BudgetMinutes != 600 || len(warnings) != 0 {
		t.Errorf("got %+v, %+v; want 600 minutes and no warning", got, warnings)
	}
}
