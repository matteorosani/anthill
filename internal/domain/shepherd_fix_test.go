package domain_test

import (
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestCommitLink(t *testing.T) {
	gh := domain.PullRequest{Ref: domain.Ref{Repo: domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}, Kind: domain.KindPR, Number: 34},
		URL: "https://github.com/acme/shop/pull/34"}
	ado := domain.PullRequest{Ref: domain.Ref{Repo: domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "Shop", Name: "Web"}, Kind: domain.KindPR, Number: 34},
		URL: "https://dev.azure.com/contoso/Shop/_git/Web/pullrequest/34"}
	for _, c := range []struct {
		pr   domain.PullRequest
		want string
	}{
		{gh, "https://github.com/acme/shop/pull/34/commits/abc"},
		{ado, "https://dev.azure.com/contoso/Shop/_git/Web/commit/abc"},
		{domain.PullRequest{}, ""},
	} {
		if got := domain.CommitLink(c.pr, "abc"); got != c.want {
			t.Errorf("CommitLink(%s) = %q, want %q", c.pr.URL, got, c.want)
		}
	}
}

func TestFixTexts(t *testing.T) {
	if got := domain.FixedText("0123456789abcdef", "https://x/c"); got != "Fixed automatically in [`0123456789ab`](https://x/c)." {
		t.Errorf("FixedText = %q", got)
	}
	if got := domain.SkippedText("needs an API decision"); !strings.Contains(got, "needs an API decision") || !strings.Contains(got, "`needs-developer`") {
		t.Errorf("SkippedText = %q", got)
	}
	got := domain.FixFailedText(domain.FixVerifyFailed, "exit code 2")
	if !strings.Contains(got, "the verification command failed (exit code 2), so nothing was pushed") || !strings.Contains(got, "needs a developer") {
		t.Errorf("FixFailedText = %q", got)
	}
}

func TestFixBrief(t *testing.T) {
	pr := domain.PullRequest{Ref: domain.Ref{Repo: domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}, Kind: domain.KindPR, Number: 34}, Title: "Fix login"}
	brief := domain.FixBrief(pr, []domain.Finding{
		{Location: domain.Location{File: "src/auth.ts", StartLine: 3}, Severity: domain.SeverityNit, Classification: domain.AutoFixable, Message: "unused import", SuggestedFix: "remove it"},
		{Location: domain.Location{File: "src/auth.ts", StartLine: 9, EndLine: 11}, Severity: domain.SeverityNit, Classification: domain.AutoFixable, Message: "unused variable"},
	}, []string{"t-1", "t-2"})
	for _, want := range []string{"id t-1 (nit, src/auth.ts:3): unused import", "Suggested fix: remove it", "id t-2 (nit, src/auth.ts:9-11)", "finish_fixes", "one commit per fixed finding"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
}
