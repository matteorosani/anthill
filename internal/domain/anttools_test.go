package domain_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// Spec ant-tools, "Tool sets per agent role".
func TestToolsFor(t *testing.T) {
	task := []string{
		"get_task", "time_remaining", "request_extension",
		"ask_question", "report_progress", "comment", "reply_to_thread",
		"push", "open_pull_request", "update_pull_request", "finish",
	}
	followUp := slices.DeleteFunc(slices.Clone(task), func(s string) bool { return s == "open_pull_request" })
	for _, tt := range []struct {
		role domain.AgentRole
		want []string
	}{
		{domain.RoleTaskRun, task},
		{domain.RoleFollowUpRun, followUp},
		{domain.RoleReviewAgent, []string{"submit_review"}},
		{domain.RoleFixAgent, []string{"finish_fixes"}},
		{"queen", nil},
	} {
		if got := domain.ToolsFor(tt.role); !slices.Equal(got, tt.want) {
			t.Errorf("ToolsFor(%s) = %v, want %v", tt.role, got, tt.want)
		}
	}
	// Every tool belongs to some role.
	for _, tool := range domain.AntTools {
		if !slices.ContainsFunc(domain.AgentRoles, func(r domain.AgentRole) bool { return slices.Contains(domain.ToolsFor(r), tool) }) {
			t.Errorf("%s is in no role", tool)
		}
	}
}

// Spec ant-tools, "Fix agent cannot push".
func TestCheckToolRole(t *testing.T) {
	for _, tt := range []struct {
		role domain.AgentRole
		tool string
		want string
	}{
		{domain.RoleTaskRun, domain.ToolPush, ""},
		{domain.RoleFixAgent, domain.ToolPush, "push is not available to the fix agent"},
		{domain.RoleReviewAgent, domain.ToolComment, "comment is not available to the review agent"},
		{domain.RoleFollowUpRun, domain.ToolOpenPullRequest, "open_pull_request is not available to the follow-up run"},
		{domain.RoleTaskRun, domain.ToolSubmitReview, "submit_review is not available to the task run"},
		{domain.RoleTaskRun, "deploy", "unknown tool deploy"},
	} {
		err := domain.CheckToolRole(tt.role, tt.tool)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("CheckToolRole(%s, %s) = %v, want nil", tt.role, tt.tool, err)
		case tt.want != "" && (err == nil || err.Error() != tt.want || !errors.Is(err, domain.ErrToolRefused)):
			t.Errorf("CheckToolRole(%s, %s) = %v, want the refusal %q", tt.role, tt.tool, err, tt.want)
		}
	}
}

// Spec ant-tools, "Call while waiting for an extension".
func TestCheckToolState(t *testing.T) {
	for _, s := range domain.AntStates {
		for _, tool := range []string{domain.ToolPush, domain.ToolGetTask, domain.ToolTimeRemaining} {
			want := ""
			switch {
			case s == domain.AntStarting || s == domain.AntRunning:
			case s == domain.AntAwaitingExtension && tool != domain.ToolPush:
			case s == domain.AntAwaitingExtension:
				want = "waiting for the extension decision"
			case s == domain.AntFinishing:
				want = "the run is finishing: no more tool calls"
			default:
				want = "the run has ended"
			}
			err := domain.CheckToolState(s, tool)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != want {
				t.Errorf("CheckToolState(%s, %s) = %q, want %q", s, tool, got, want)
			}
		}
	}
}

func TestAgentRoleWords(t *testing.T) {
	for _, r := range domain.AgentRoles {
		if !r.Valid() || r.Words() == string(r) {
			t.Errorf("role %s: valid %v, words %q", r, r.Valid(), r.Words())
		}
	}
	if domain.AgentRole("queen").Valid() {
		t.Error("queen is not an agent role")
	}
}

// Spec ant-tools, "Complete without commits" and "No-change with
// commits".
func TestCheckFinish(t *testing.T) {
	for _, tt := range []struct {
		role    domain.AgentRole
		status  domain.FinishStatus
		commits int
		want    string
	}{
		{domain.RoleTaskRun, domain.FinishComplete, 0, "no commits on the branch: finish with no-change, or ask a question"},
		{domain.RoleTaskRun, domain.FinishPartial, 0, "no commits on the branch: finish with no-change, or ask a question"},
		{domain.RoleTaskRun, domain.FinishNoChange, 2, "the branch has new commits: finish with complete or partial"},
		{domain.RoleTaskRun, domain.FinishComplete, 1, ""},
		{domain.RoleTaskRun, domain.FinishNoChange, 0, ""},
		{domain.RoleFollowUpRun, domain.FinishNoChange, 3, ""},
		{domain.RoleFollowUpRun, domain.FinishComplete, 0, ""},
		{domain.RoleTaskRun, "done", 1, `finish status must be complete, partial or no-change, not "done"`},
	} {
		err := domain.CheckFinish(tt.role, tt.status, tt.commits)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tt.want {
			t.Errorf("CheckFinish(%s, %s, %d) = %q, want %q", tt.role, tt.status, tt.commits, got, tt.want)
		}
	}
}

func TestResolveFixResults(t *testing.T) {
	commits := []string{"3f2a1c0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "3f2a1c0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "9c8d7e6fffffffffffffffffffffffffffffffff"}
	given := []string{"f1", "f2"}
	got, err := domain.ResolveFixResults(given, []domain.FixResult{
		{Finding: "f2", Reason: "needs an API decision", Commit: "ignored"},
		{Finding: "f1", Fixed: true, Commit: "9C8D7E6", Reason: "ignored"},
	}, commits)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.FixResult{{Finding: "f1", Fixed: true, Commit: commits[2]}, {Finding: "f2", Reason: "needs an API decision"}}
	if !slices.Equal(got, want) {
		t.Errorf("ResolveFixResults = %+v, want %+v", got, want)
	}
	for _, tt := range []struct {
		results []domain.FixResult
		want    string
	}{
		{[]domain.FixResult{{Finding: "f1", Fixed: true, Commit: "9c8d7e6"}}, "no result for finding f2: report each finding as fixed or skipped"},
		{[]domain.FixResult{{Finding: "f1", Reason: "r"}, {Finding: "f1", Reason: "r"}, {Finding: "f2", Reason: "r"}}, "finding f1 has more than one result"},
		{[]domain.FixResult{{Finding: "f9", Reason: "r"}}, `finding "f9" was not given to this fix pass`},
		{[]domain.FixResult{{Finding: "f1", Fixed: true}}, "finding f1: a fixed finding needs the commit that fixes it"},
		{[]domain.FixResult{{Finding: "f1", Fixed: true, Commit: "3f2a"}}, `finding f1: commit "3f2a" is too short: give at least 7 characters`},
		{[]domain.FixResult{{Finding: "f1", Fixed: true, Commit: "3f2a1c0"}}, `finding f1: commit "3f2a1c0" is ambiguous`},
		{[]domain.FixResult{{Finding: "f1", Fixed: true, Commit: "1234567"}}, "finding f1: commit 1234567 is not a new commit of this fix pass"},
		{[]domain.FixResult{{Finding: "f1"}}, "finding f1 is skipped without a reason"},
	} {
		_, err := domain.ResolveFixResults(given, tt.results, commits)
		if err == nil || err.Error() != tt.want || !errors.Is(err, domain.ErrToolRefused) {
			t.Errorf("ResolveFixResults(%+v) = %v, want %q", tt.results, err, tt.want)
		}
	}
}

// Spec agent-conversation, "Comment content safety": no absolute local
// paths in posted text.
func TestRewritePaths(t *testing.T) {
	const worktree, home = "/home/ann/.anthill/worktrees/gh-acme-shop/ant/gh-12-fix", "/home/ann"
	for _, tt := range []struct{ in, want string }{
		{worktree + "/src/auth.go:42", "./src/auth.go:42"},
		{"see /home/ann/.netrc and " + worktree, "see ~/.netrc and ."},
		{"/etc/hosts", "/etc/hosts"},
	} {
		if got := domain.RewritePaths(tt.in, worktree, home); got != tt.want {
			t.Errorf("RewritePaths(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	const winTree, winHome = `C:\Users\ann\.anthill\worktrees\w`, `C:\Users\ann`
	if got := domain.RewritePaths(`C:\Users\ann\.anthill\worktrees\w\a.go and C:/Users/ann/.ssh`, winTree, winHome); got != `.\a.go and ~/.ssh` {
		t.Errorf("Windows paths = %q", got)
	}
	if got := domain.RewritePaths("/a/b", "", "/"); got != "/a/b" {
		t.Errorf("empty or root paths must be skipped, got %q", got)
	}
}
