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
		{"git \\\n  push", domain.ToolPush}, // a backslash at the line end continues the line
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

// The PowerShell commands of Claude Code's PowerShell tool on Windows
// are refused as well (issue #146, smoke check C2), read with
// PowerShell's quoting.
func TestRefusePowerShellCommand(t *testing.T) {
	tests := []struct {
		command string
		tool    string // the tool the message names; empty: not refused
	}{
		// The commands of smoke check C2.
		{"gh pr create --fill", domain.ToolOpenPullRequest},
		{"git push origin HEAD", domain.ToolPush},
		// A backslash is an ordinary character, so a path stays whole.
		{`C:\tools\gh.exe pr create`, domain.ToolOpenPullRequest},
		{`& "C:\Program Files\GitHub CLI\gh.exe" pr create --fill`, domain.ToolOpenPullRequest},
		{`& 'C:\Program Files\Git\cmd\git.exe' push`, domain.ToolPush},
		{`Set-Location C:\src\shop; git push`, domain.ToolPush},
		{"go test ./... && git push", domain.ToolPush},
		{"git status | Out-Null; git push", domain.ToolPush},
		{"if ($?) { git push }", domain.ToolPush},
		{"$out = $(git push 2>&1)", domain.ToolPush},
		{"g`it push", domain.ToolPush}, // the backtick escapes
		{"git `\n  push", domain.ToolPush},
		{`Invoke-Expression "git push origin main"`, domain.ToolPush},
		{"iex 'gh pr create --fill'", domain.ToolOpenPullRequest},
		{"Start-Process -FilePath git -ArgumentList push -Wait", domain.ToolPush},
		{"Start-Process gh -ArgumentList 'pr','create'", "ant tools"},
		{"Invoke-Command -ScriptBlock { git push }", domain.ToolPush},
		{`. az repos pr create --title x`, domain.ToolOpenPullRequest},
		{`pwsh -Command "git push"`, domain.ToolPush},
		{`powershell -NoProfile -Command "& 'C:\gh\gh.exe' pr create"`, domain.ToolOpenPullRequest},
		{`bash -c "git push"`, domain.ToolPush},
		{"az login", "ant tools"},
		{"Start-Sleep -Seconds 20", ""},
		{"go test ./...", ""},
		{`git commit -m "push the fix"`, ""},
		{"Write-Output 'git push'", ""},
		{"Write-Output \"`$(git push)\"", ""}, // an escaped $ does not substitute
		{`Get-ChildItem C:\src\gh-pages`, ""},
		{"", ""},
	}
	for _, tt := range tests {
		msg, refused := domain.RefusePowerShellCommand(tt.command)
		if refused != (tt.tool != "") {
			t.Errorf("%q: refused = %v, want %v", tt.command, refused, tt.tool != "")
			continue
		}
		if refused && !strings.Contains(msg, tt.tool) {
			t.Errorf("%q: message %q does not name %s", tt.command, msg, tt.tool)
		}
	}
	// The same path read as a POSIX command loses its backslashes, which
	// is why the PowerShell tool needs its own reading.
	if _, refused := domain.RefuseCommand(`C:\tools\gh.exe pr create`); refused {
		t.Error(`RefuseCommand read C:\tools\gh.exe as gh`)
	}
}
