package domain

import (
	"errors"
	"fmt"
	"slices"
)

// AgentRole is what an agent is started for (glossary). Each role gets its
// own set of ant tools (spec ant-tools, "Tool sets per agent role").
type AgentRole string

// The agent roles.
const (
	RoleTaskRun     AgentRole = "task-run"
	RoleFollowUpRun AgentRole = "follow-up-run"
	RoleReviewAgent AgentRole = "review-agent"
	RoleFixAgent    AgentRole = "fix-agent"
)

// AgentRoles lists every agent role.
var AgentRoles = []AgentRole{RoleTaskRun, RoleFollowUpRun, RoleReviewAgent, RoleFixAgent}

// Valid reports whether r is one of the agent roles.
func (r AgentRole) Valid() bool {
	return slices.Contains(AgentRoles, r)
}

// IsRun reports whether r is the role of an ant's run, as opposed to a
// shepherd round's review or fix agent.
func (r AgentRole) IsRun() bool {
	return r == RoleTaskRun || r == RoleFollowUpRun
}

// Words names the role as a refusal says it, such as "fix agent".
func (r AgentRole) Words() string {
	switch r {
	case RoleTaskRun:
		return "task run"
	case RoleFollowUpRun:
		return "follow-up run"
	case RoleReviewAgent:
		return "review agent"
	case RoleFixAgent:
		return "fix agent"
	}
	return string(r)
}

// The ant tools (spec ant-tools).
const (
	ToolGetTask           = "get_task"
	ToolTimeRemaining     = "time_remaining"
	ToolRequestExtension  = "request_extension"
	ToolAskQuestion       = "ask_question"
	ToolReportProgress    = "report_progress"
	ToolComment           = "comment"
	ToolReplyToThread     = "reply_to_thread"
	ToolPush              = "push"
	ToolOpenPullRequest   = "open_pull_request"
	ToolUpdatePullRequest = "update_pull_request"
	ToolFinish            = "finish"
	ToolSubmitReview      = "submit_review"
	ToolFinishFixes       = "finish_fixes"
)

// AntTools lists every ant tool.
var AntTools = []string{
	ToolGetTask, ToolTimeRemaining, ToolRequestExtension,
	ToolAskQuestion, ToolReportProgress, ToolComment, ToolReplyToThread,
	ToolPush, ToolOpenPullRequest, ToolUpdatePullRequest, ToolFinish,
	ToolSubmitReview, ToolFinishFixes,
}

// taskRunTools are the task, time, extension, conversation, push, pull
// request and finish tools.
var taskRunTools = []string{
	ToolGetTask, ToolTimeRemaining, ToolRequestExtension,
	ToolAskQuestion, ToolReportProgress, ToolComment, ToolReplyToThread,
	ToolPush, ToolOpenPullRequest, ToolUpdatePullRequest, ToolFinish,
}

// ToolsFor lists the tools of a role, in the order of AntTools: a
// follow-up run has the task run's tools without open_pull_request, a
// review agent only submit_review and a fix agent only finish_fixes.
func ToolsFor(r AgentRole) []string {
	switch r {
	case RoleTaskRun:
		return slices.Clone(taskRunTools)
	case RoleFollowUpRun:
		return slices.DeleteFunc(slices.Clone(taskRunTools), func(t string) bool { return t == ToolOpenPullRequest })
	case RoleReviewAgent:
		return []string{ToolSubmitReview}
	case RoleFixAgent:
		return []string{ToolFinishFixes}
	}
	return nil
}

// ErrToolRefused is returned, wrapped in a *ToolRefusedError, for every
// ant-tool call Anthill refuses.
var ErrToolRefused = errors.New("tool call refused")

// ToolRefusedError says why a tool call was refused, in words the agent
// can act on (spec ant-tools, "Every call is checked and returns a
// reason").
type ToolRefusedError struct {
	Reason string
}

func (e *ToolRefusedError) Error() string { return e.Reason }

// Is makes errors.Is(err, ErrToolRefused) true.
func (e *ToolRefusedError) Is(target error) bool { return target == ErrToolRefused }

// RefuseTool returns a *ToolRefusedError with the formatted reason.
func RefuseTool(format string, args ...any) error {
	return &ToolRefusedError{Reason: fmt.Sprintf(format, args...)}
}

// The refusals of the call checks.
const (
	MessageUnknownToken        = "unknown or revoked tool token"
	MessageWaitingForExtension = "waiting for the extension decision"
	MessageRunFinishing        = "the run is finishing: no more tool calls"
	MessageRunEnded            = "the run has ended"
)

// CheckToolRole refuses a tool that is not in the role's set (spec
// ant-tools, "Fix agent cannot push").
func CheckToolRole(r AgentRole, tool string) error {
	if !slices.Contains(AntTools, tool) {
		return RefuseTool("unknown tool %s", tool)
	}
	if !slices.Contains(ToolsFor(r), tool) {
		return RefuseTool("%s is not available to the %s", tool, r.Words())
	}
	return nil
}

// readOnlyTools only read, so they may run while another call of the same
// run waits for an extension decision.
var readOnlyTools = []string{ToolGetTask, ToolTimeRemaining}

// CheckToolState refuses a run's tool call that the ant's state does not
// allow. Calls are allowed while the ant is starting or running. While it
// is awaiting-extension, only get_task and time_remaining are (spec
// ant-tools, "Call while waiting for an extension"). Every other state
// means the run is finishing or has ended.
func CheckToolState(s AntState, tool string) error {
	switch s {
	case AntStarting, AntRunning:
		return nil
	case AntAwaitingExtension:
		if slices.Contains(readOnlyTools, tool) {
			return nil
		}
		return RefuseTool("%s", MessageWaitingForExtension)
	case AntFinishing:
		return RefuseTool("%s", MessageRunFinishing)
	}
	return RefuseTool("%s", MessageRunEnded)
}

// MessageClaimNotHeld is the refusal of a call whose claim this instance
// no longer holds.
func MessageClaimNotHeld(ref Ref) string {
	return fmt.Sprintf("the claim on %s is no longer held by this instance: stop working", ref)
}
