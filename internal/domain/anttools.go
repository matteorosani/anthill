package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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

// FinishStatus is the status an ant reports with finish (glossary).
type FinishStatus string

// The finish statuses.
const (
	FinishComplete FinishStatus = "complete"
	FinishPartial  FinishStatus = "partial"
	FinishNoChange FinishStatus = "no-change"
)

// Valid reports whether s is one of the finish statuses.
func (s FinishStatus) Valid() bool {
	return s == FinishComplete || s == FinishPartial || s == FinishNoChange
}

// The refusals of finish on a task run (spec ant-tools, "Finish tool").
const (
	MessageFinishWithoutCommits = "no commits on the branch: finish with no-change, or ask a question"
	MessageNoChangeWithCommits  = "the branch has new commits: finish with complete or partial"
)

// CheckFinish applies the finish rules: on a task run, complete and
// partial need new commits on the branch and no-change allows none. A
// follow-up run may finish with any status.
func CheckFinish(r AgentRole, s FinishStatus, newCommits int) error {
	if !s.Valid() {
		return RefuseTool("finish status must be complete, partial or no-change, not %q", s)
	}
	if r != RoleTaskRun {
		return nil
	}
	switch {
	case s == FinishNoChange && newCommits > 0:
		return RefuseTool("%s", MessageNoChangeWithCommits)
	case s != FinishNoChange && newCommits == 0:
		return RefuseTool("%s", MessageFinishWithoutCommits)
	}
	return nil
}

// FixResult is what a fix agent reports for one finding with
// finish_fixes (spec ant-tools, "Fix report tool").
type FixResult struct {
	// Finding is the id of the finding the fix agent was given.
	Finding string
	Fixed   bool
	// Commit is the full hash of the commit that fixes it, when Fixed.
	Commit string
	// Reason is why it was skipped, when not Fixed.
	Reason string
}

// minCommitPrefix is the shortest abbreviated hash a fix result may
// give.
const minCommitPrefix = 7

// ResolveFixResults checks a fix agent's results against the findings it
// was given and the commits of its fix pass, oldest first: every finding
// is answered exactly once, a fixed one names one of those commits (in
// full or by a prefix of at least 7 characters), and a skipped one gives
// a reason. It returns the results in the order of given, with full
// commit hashes.
func ResolveFixResults(given []string, results []FixResult, commits []string) ([]FixResult, error) {
	byID := map[string]FixResult{}
	for _, r := range results {
		switch {
		case !slices.Contains(given, r.Finding):
			return nil, RefuseTool("finding %q was not given to this fix pass", r.Finding)
		case byID[r.Finding].Finding != "":
			return nil, RefuseTool("finding %s has more than one result", r.Finding)
		}
		if r.Fixed {
			full, err := matchCommit(r.Commit, commits)
			if err != nil {
				return nil, RefuseTool("finding %s: %s", r.Finding, err)
			}
			r.Commit, r.Reason = full, ""
		} else {
			if r.Reason == "" {
				return nil, RefuseTool("finding %s is skipped without a reason", r.Finding)
			}
			r.Commit = ""
		}
		byID[r.Finding] = r
	}
	out := make([]FixResult, 0, len(given))
	var missing []string
	for _, id := range given {
		r, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		out = append(out, r)
	}
	if len(missing) > 0 {
		return nil, RefuseTool("no result for finding %s: report each finding as fixed or skipped", strings.Join(missing, ", "))
	}
	return out, nil
}

// matchCommit finds the commit that hash names.
func matchCommit(hash string, commits []string) (string, error) {
	switch {
	case hash == "":
		return "", errors.New("a fixed finding needs the commit that fixes it")
	case len(hash) < minCommitPrefix:
		return "", fmt.Errorf("commit %q is too short: give at least %d characters", hash, minCommitPrefix)
	}
	var found string
	for _, c := range commits {
		if strings.HasPrefix(c, strings.ToLower(hash)) {
			if found != "" {
				return "", fmt.Errorf("commit %q is ambiguous", hash)
			}
			found = c
		}
	}
	if found == "" {
		return "", fmt.Errorf("commit %s is not a new commit of this fix pass", hash)
	}
	return found, nil
}

// ClosingReference is the text that makes the code host close item when
// the PR from repo merges (spec ant-execution, "Finishing with a pull
// request"): Closes #12, Closes owner/other#12 across GitHub
// repositories, or AB#12 for an Azure DevOps work item.
func ClosingReference(item Ref, repo Repository) string {
	if item.Repo.Provider == AzureDevOps {
		return fmt.Sprintf("AB#%d", item.Number)
	}
	return "Closes " + item.Display(repo)
}

// WithClosingReference returns body with the closing reference on its
// own last line, unless body already has it.
func WithClosingReference(body string, item Ref, repo Repository) string {
	ref := ClosingReference(item, repo)
	if strings.Contains(body, ref) {
		return body
	}
	body = strings.TrimRight(body, " \t\r\n")
	if body == "" {
		return ref
	}
	return body + "\n\n" + ref
}

// RewritePaths takes absolute local paths out of text Anthill posts (spec
// agent-conversation, "Comment content safety"): the worktree becomes "."
// and the user's home directory "~". Both forms of separator are
// matched, since agents on Windows write either. Empty paths are
// skipped.
func RewritePaths(text, worktree, home string) string {
	for _, p := range []struct{ path, with string }{{worktree, "."}, {home, "~"}} {
		if len(strings.Trim(p.path, `/\`)) == 0 {
			continue
		}
		path := strings.TrimRight(p.path, `/\`)
		for _, form := range []string{path, strings.ReplaceAll(path, `\`, "/"), strings.ReplaceAll(path, "/", `\`)} {
			text = strings.ReplaceAll(text, form, p.with)
		}
	}
	return text
}
