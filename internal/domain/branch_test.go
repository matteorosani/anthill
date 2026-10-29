package domain_test

import (
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestBranchName(t *testing.T) {
	gh12 := domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 12}
	ado7 := domain.Ref{Repo: adoProject, Kind: domain.KindItem, Number: 7}
	tests := []struct {
		name   string
		prefix string
		ref    domain.Ref
		title  string
		want   string
	}{
		{"github issue branch", "ant", gh12, "Fix login timeout on Safari", "ant/gh-12-fix-login-timeout-on-safari"},
		{"azure devops item", "ant", ado7, "Add export", "ant/ado-7-add-export"},
		{"configured prefix", "bots/ant", gh12, "Fix it", "bots/ant/gh-12-fix-it"},
		{"accented title", "ant", gh12, "Corrégé l'été à Zürich", "ant/gh-12-correge-l-ete-a-zurich"},
		{"title with no ascii left has no slug", "ant", gh12, "修复登录超时", "ant/gh-12"},
		{"empty title", "ant", gh12, "", "ant/gh-12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.BranchName(tt.prefix, tt.ref, tt.title)
			if err != nil {
				t.Fatalf("BranchName: %v", err)
			}
			if got != tt.want {
				t.Errorf("BranchName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBranchNameErrors(t *testing.T) {
	gh12 := domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 12}
	pr := domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 34}
	for _, prefix := range []string{"", "-ant", "ant/", "a..b", "a b", ".ant"} {
		if _, err := domain.BranchName(prefix, gh12, "x"); err == nil {
			t.Errorf("BranchName accepted prefix %q", prefix)
		}
	}
	if _, err := domain.BranchName("ant", pr, "x"); err == nil {
		t.Error("BranchName accepted a pull request reference")
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"lowercased and hyphenated", "Fix Login Timeout", "fix-login-timeout"},
		{"punctuation runs become one hyphen", "Fix: login -- timeout!!", "fix-login-timeout"},
		{"leading and trailing separators trimmed", "  [WIP] fix  ", "wip-fix"},
		{"digits kept", "Upgrade to Go 1.27", "upgrade-to-go-1-27"},
		{"non-ascii: diacritics removed", "Crème brûlée façade", "creme-brulee-facade"},
		{"non-ascii: special letters", "Straße Æsir Œuvre Øre Đak Łódź Þing", "strasse-aesir-oeuvre-ore-dak-lodz-thing"},
		{"non-ascii: capital special letters", "ØRE", "ore"},
		{"non-ascii: other scripts dropped", "Fix 登录 timeout", "fix-timeout"},
		{"non-ascii: emoji dropped", "🐜 Ant colony", "ant-colony"},
		{"40-character limit", "This title is much longer than forty characters in total", "this-title-is-much-longer-than-forty-cha"},
		{"40-character limit drops a trailing hyphen", "abcdefghij abcdefghij abcdefghij abcdefg xyz", "abcdefghij-abcdefghij-abcdefghij-abcdefg"},
		{"no ascii left", "登录", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.Slug(tt.title)
			if got != tt.want {
				t.Errorf("Slug(%q) = %q, want %q", tt.title, got, tt.want)
			}
			if len(got) > domain.SlugMaxLength {
				t.Errorf("Slug(%q) has %d characters, more than %d", tt.title, len(got), domain.SlugMaxLength)
			}
			if strings.HasSuffix(got, "-") || strings.HasPrefix(got, "-") {
				t.Errorf("Slug(%q) = %q starts or ends with a hyphen", tt.title, got)
			}
		})
	}
}

func TestWithSuffix(t *testing.T) {
	const branch = "ant/gh-12-fix-login"
	tests := []struct {
		n    int
		want string
	}{
		{0, branch},
		{1, branch},
		{2, branch + "-2"},
		{3, branch + "-3"},
	}
	for _, tt := range tests {
		if got := domain.WithSuffix(branch, tt.n); got != tt.want {
			t.Errorf("WithSuffix(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
