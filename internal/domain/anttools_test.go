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
