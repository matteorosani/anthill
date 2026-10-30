package domain_test

import (
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// Spec ant-tools, "Agent tries gh" and the redirect hooks: git push, gh
// and az are refused wherever they appear, with the ant tool to use.
func TestRefuseCommand(t *testing.T) {
	tests := []struct {
		command string
		tool    string // the tool the message names; empty: not refused
	}{
		{"git push", domain.ToolPush},
		{"git push origin HEAD --force", domain.ToolPush},
		{"git -C ../repo push", domain.ToolPush},
		{"git -c credential.helper= push origin", domain.ToolPush},
		{"cd sub && git push", domain.ToolPush},
		{"make test; git push", domain.ToolPush},
		{"/usr/bin/git push", domain.ToolPush},
		{`C:\Program Files\Git\cmd\git.exe push`, ""}, // unquoted path with a space: the words split
		{`"C:\Program Files\Git\cmd\git.exe" push`, domain.ToolPush},
		{"GIT_TRACE=1 git push", domain.ToolPush},
		{"env GIT_TRACE=1 git push", domain.ToolPush},
		{"sudo git push", domain.ToolPush},
		{`bash -c "git push origin main"`, domain.ToolPush},
		{`sh -c 'cd x && git push'`, domain.ToolPush},
		{"echo $(git push)", domain.ToolPush},
		{"echo `git push`", domain.ToolPush},
		{"(git push)", domain.ToolPush},
		{"eval git push", domain.ToolPush},
		{"gh pr create --fill", domain.ToolOpenPullRequest},
		{"gh pr create --title 'Fix login' --body \"x\"", domain.ToolOpenPullRequest},
		{"gh pr edit 34 --title x", domain.ToolUpdatePullRequest},
		{"gh pr ready 34", domain.ToolUpdatePullRequest},
		{"gh issue comment 12 --body hi", domain.ToolComment},
		{"gh pr comment 34 -b hi", domain.ToolComment},
		{"az repos pr create --title x", domain.ToolOpenPullRequest},
		{"az repos pr update --id 3 --draft false", domain.ToolUpdatePullRequest},
		{"az boards work-item update --id 12 --discussion hi", domain.ToolComment},
		{"gh.exe pr create", domain.ToolOpenPullRequest},
		{"git status && go test ./... | tee out.txt", ""},
		{"git log --format=%s push", ""},
		{"echo 'git push'", ""},
		{"echo git push", ""},
		{"grep -r gh .", ""},
		{"git commit -m 'push the fix'", ""},
		{"ls ./gh-pages", ""},
		{"", ""},
	}
	for _, tt := range tests {
		msg, refused := domain.RefuseCommand(tt.command)
		if refused != (tt.tool != "") {
			t.Errorf("%q: refused = %v, want %v", tt.command, refused, tt.tool != "")
			continue
		}
		if refused && !strings.Contains(msg, "the "+tt.tool+" tool") {
			t.Errorf("%q: message %q does not name %s", tt.command, msg, tt.tool)
		}
	}
}

// Other gh and az commands are refused with the list of ant tools.
func TestRefuseOtherProviderCommands(t *testing.T) {
	for _, c := range []string{"gh api repos/acme/shop", "az login", "gh auth token"} {
		msg, refused := domain.RefuseCommand(c)
		if !refused || !strings.Contains(msg, domain.ToolOpenPullRequest) || !strings.Contains(msg, domain.ToolComment) {
			t.Errorf("%q: refused %v, message %q", c, refused, msg)
		}
	}
}
