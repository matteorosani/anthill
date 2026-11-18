package domain_test

import (
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestWithOpenPoints(t *testing.T) {
	section := domain.OpenPointsSection([]string{"Firefox", "Tests"})
	other := domain.OpenPointsSection([]string{"Docs"})
	tests := []struct {
		name, body string
		points     []string
		want       string
	}{
		{"added at the end", "Fixes Safari.\n\nCloses #12\n", []string{"Firefox", "Tests"}, "Fixes Safari.\n\nCloses #12\n\n" + section},
		{"empty body", "", []string{"Firefox", "Tests"}, section},
		{"replaced, not added twice", "Fixes Safari.\n\n" + other + "\n\nCloses #12", []string{"Firefox", "Tests"}, "Fixes Safari.\n\n" + section + "\n\nCloses #12"},
		{"removed when none are left", "Fixes Safari.\n\n" + other + "\n\nCloses #12", nil, "Fixes Safari.\n\nCloses #12"},
		{"no section and no points", "Fixes Safari.", nil, "Fixes Safari."},
		{"unterminated section is left alone", "A <!-- anthill:open-points --> B", []string{"X"}, "A <!-- anthill:open-points --> B\n\n" + domain.OpenPointsSection([]string{"X"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.WithOpenPoints(tt.body, tt.points); got != tt.want {
				t.Errorf("WithOpenPoints() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestOpenPointsSection(t *testing.T) {
	if got := domain.OpenPointsSection(nil); got != "" {
		t.Errorf("no points = %q", got)
	}
	got := domain.OpenPointsSection([]string{" Firefox ", "Tests"})
	if !strings.Contains(got, "### Open points\n\n- Firefox\n- Tests\n") {
		t.Errorf("section = %q", got)
	}
}

func TestFinishPRBody(t *testing.T) {
	if got := domain.FinishPRBody(" Raised the timeout. \n", nil); got != "Raised the timeout." {
		t.Errorf("body = %q", got)
	}
	got := domain.FinishPRBody("Half done.", []string{"Firefox"})
	if !strings.HasPrefix(got, "Half done.\n\n") || !strings.Contains(got, "- Firefox") {
		t.Errorf("body = %q", got)
	}
}

func TestMessageNoChangeExplanation(t *testing.T) {
	got := domain.MessageNoChangeExplanation("Already fixed in abc123.", "anthill:hold")
	for _, want := range []string{"Already fixed in abc123.", "`anthill:hold`", "released"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q lacks %q", got, want)
		}
	}
}

func TestWrapUpComment(t *testing.T) {
	tests := []struct {
		name string
		note domain.WrapUpNote
		want []string
		not  []string
	}{
		{
			name: "pushed, no PR",
			note: domain.WrapUpNote{Reason: "exited without finish", Text: "Last message: running the tests", Branch: "ant/gh-12-fix", Pushed: true},
			want: []string{"exited without finish", "Last message: running the tests", "pushed to `ant/gh-12-fix`"},
			not:  []string{"pull request"},
		},
		{
			name: "nothing pushed",
			note: domain.WrapUpNote{Reason: "exited without finish", Branch: "ant/gh-12-fix"},
			want: []string{"Nothing was pushed"},
		},
		{
			name: "open PR made a draft",
			note: domain.WrapUpNote{Reason: "exited without finish", Branch: "b", Pushed: true, PR: "https://x/pull/3", Draft: true},
			want: []string{"https://x/pull/3 is a draft"},
		},
		{
			name: "draft PR opened",
			note: domain.WrapUpNote{Reason: "budget used up", Branch: "b", Pushed: true, PR: "https://x/pull/3", Draft: true, Opened: true},
			want: []string{"A draft pull request was opened: https://x/pull/3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.WrapUpComment(tt.note)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("comment %q lacks %q", got, w)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(got, n) {
					t.Errorf("comment %q has %q", got, n)
				}
			}
		})
	}
}
