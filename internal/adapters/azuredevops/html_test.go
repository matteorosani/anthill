package azuredevops

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"empty-looking div", "<div><br></div>", ""},
		{"empty-looking with spaces and entities", "<div>&nbsp;</div><p> </p><div><b><br/></b></div>", ""},
		{"plain paragraphs", "<div>Login fails on Safari.</div><div>Steps follow.</div>", "Login fails on Safari.\nSteps follow."},
		{"entities", "<p>a &lt; b &amp;&amp; c &gt; d &quot;e&quot;</p>", `a < b && c > d "e"`},
		{
			"acceptance criteria heading",
			"<div>Fix the timeout.</div><h2>Acceptance criteria</h2><ul><li>Login works on Safari.</li><li>No regressions.</li></ul>",
			"Fix the timeout.\n## Acceptance criteria\n- Login works on Safari.\n- No regressions.",
		},
		{
			"acceptance criteria bold line",
			"<div>Fix it.</div><div><b>Acceptance criteria:</b></div><div>Works on Safari.</div>",
			"Fix it.\n**Acceptance criteria:**\nWorks on Safari.",
		},
		{"script dropped", "<div>a</div><script>alert(1)</script><style>p{}</style><div>b</div>", "a\nb"},
		{"comment dropped", "<div>a<!-- hidden --></div>", "a"},
		{"source line breaks are spaces", "<div>one\ntwo\r\n  three</div>", "one two three"},
		{"uppercase tags", "<DIV>a</DIV><BR><P>b</P>", "a\nb"},
		{"lone angle bracket", "<div>a</div> 1 < 2", "a\n1 < 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := htmlToText(tt.in); got != tt.want {
				t.Errorf("htmlToText(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// The domain's readiness check sees through the conversion: empty-looking
// HTML is an empty description, and an acceptance criteria heading or
// bold line is a section (spec task-eligibility, "Prerequisite readiness
// content").
func TestHTMLToTextMeetsReadinessCheck(t *testing.T) {
	ctx := domain.EligibilityContext{RequireAcceptanceCriteria: true}
	unmet := func(body string) string {
		item := domain.WorkItem{Body: htmlToText(body)}
		for _, c := range domain.Evaluate(item, ctx).Unmet() {
			if c.Prerequisite == domain.PrereqHasDescription {
				return c.Reason
			}
		}
		return ""
	}
	if got := unmet("<div><br></div>"); got != "the description is empty" {
		t.Errorf("empty-looking HTML: %q", got)
	}
	for _, body := range []string{
		"<div>Fix it.</div><h3>Acceptance Criteria</h3><ul><li>Works on Safari.</li></ul>",
		"<div>Fix it.</div><div><strong>Acceptance criteria</strong></div><div>Works on Safari.</div>",
	} {
		if got := unmet(body); got != "" {
			t.Errorf("%q: has-description unmet: %q", body, got)
		}
	}
}
