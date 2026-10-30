package app

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
)

// toolSpec is one ant tool: what the agent is told about it, its input
// schema, and the code that executes it once the call checks passed. A
// nil handler means the tool is not implemented yet.
type toolSpec struct {
	description string
	input       schema
	handler     func(t *AntTools, ctx context.Context, c *toolCall) (any, error)
}

// The values of the where arguments.
const (
	whereItem = "item"
	wherePR   = "pr"
)

// findingSchema is one finding of submit_review (spec pr-shepherd,
// "Findings are posted as PR comments").
var findingSchema = object(
	optional("file", str("Path of the file, relative to the repository root. Leave it out when the finding has no location.")),
	optional("start_line", integer("First line of the finding.", 1)),
	optional("end_line", integer("Last line of the finding, when it spans several lines.", 1)),
	required("severity", str("How serious the problem is.", string(domain.SeverityBlocking), string(domain.SeveritySuggestion), string(domain.SeverityNit))),
	required("classification", str("Whether an agent can fix it without a developer's decision.", string(domain.AutoFixable), string(domain.NeedsDeveloper))),
	required("message", str("The problem, in words a developer can act on.")),
	optional("suggested_fix", str("The replacement code for the lines, when the fix is clear.")),
)

// fixResultSchema is one result of finish_fixes (spec ant-tools, "Fix
// report tool").
var fixResultSchema = object(
	required("finding", str("The id of the finding, as given in the brief.")),
	required("status", str("fixed when a commit fixes it, skipped otherwise.", "fixed", "skipped")),
	optional("commit", str("The hash of the commit that fixes the finding. Required when fixed.")),
	optional("reason", str("Why the finding was not fixed. Required when skipped.")),
)

// toolCatalogue holds every ant tool (spec ant-tools). docs/ant-tools.md
// describes each one, and a test keeps the two lists equal.
var toolCatalogue = map[string]toolSpec{
	domain.ToolGetTask: {
		description: "Read the current item fresh from the tracker: its title, body, acceptance criteria and human comments, " +
			"with your branch, the base branch, your time budget and your pull request if you opened one. " +
			"Use it instead of the brief when the item may have changed.",
		input:   object(),
		handler: (*AntTools).getTask,
	},
	domain.ToolTimeRemaining: {
		description: "Tell how many minutes of your time budget remain, with any warning and your open extension request. " +
			"Call it from time to time: the budget is enforced, and work not committed when it runs out is lost.",
		input:   object(),
		handler: (*AntTools).timeRemaining,
	},
	domain.ToolRequestExtension: {
		description: "Ask for more time. Small requests may be approved at once; otherwise the call waits for the queen for a few minutes. " +
			"It returns approved, reduced (fewer minutes), denied, or pending when no decision came in time: keep working within your " +
			"remaining time then, and the decision is delivered to you later. Do not ask again while a request is pending.",
		input: object(
			required("minutes", integer("The extra minutes needed.", 1)),
			required("reason", str("Why the extra time is needed: what is done and what is left.")),
		),
		handler: (*AntTools).requestExtension,
	},
	domain.ToolAskQuestion: {
		description: "Ask the developers a question you cannot answer from the item, the code or the discussion. " +
			"The question is posted on the item, or on your pull request in a follow-up run. When the call returns, your run ends " +
			"with your session saved, and a later run resumes it with the answer. Commit your work before asking.",
		input: object(
			required("text", str("The question, with the options you see and what you would choose.")),
			optional("where", str("Where to post it: the item, or your pull request. The default is the item in a task run and the pull request in a follow-up run.", whereItem, wherePR)),
		),
	},
	domain.ToolReportProgress: {
		description: "Report your progress in the one status comment of this run, on the item. " +
			"Each call replaces the previous report.",
		input: object(required("text", str("What is done and what is next, in a few lines."))),
	},
	domain.ToolComment: {
		description: "Post a comment on the item or on your pull request. Anthill marks it as written by your ant and redacts secrets.",
		input: object(
			required("where", str("Where to post it.", whereItem, wherePR)),
			required("body", str("The comment, in Markdown.")),
		),
	},
	domain.ToolReplyToThread: {
		description: "Reply in a review thread of your pull request, and resolve it when the reply settles it.",
		input: object(
			required("thread", str("The id of the review thread.")),
			required("body", str("The reply, in Markdown, such as what you changed and in which commit.")),
			optional("resolve", boolean("Resolve the thread after replying. The default is false.")),
		),
	},
	domain.ToolPush: {
		description: "Push the current branch of your worktree. This is the only way to push: git push is disabled. " +
			"It never force-pushes. When the remote branch has commits you lack, they are fetched: merge them and push again.",
		input: object(),
	},
	domain.ToolOpenPullRequest: {
		description: "Open the pull request of your branch against the base branch, after pushing. Write your own title and body; " +
			"Anthill adds the closing reference to the item and the anthill label. Choose draft when the work is not ready for review.",
		input: object(
			required("title", str("The pull request's title.")),
			required("body", str("The pull request's description, in Markdown.")),
			optional("draft", boolean("Open it as a draft. The default is false.")),
		),
	},
	domain.ToolUpdatePullRequest: {
		description: "Change the title, the body or the draft state of your pull request. Give only what changes.",
		input: object(
			optional("title", str("The new title.")),
			optional("body", str("The new description, in Markdown.")),
			optional("draft", boolean("true to make it a draft, false to mark it ready for review.")),
		),
	},
	domain.ToolFinish: {
		description: "End your run. complete: the item is done. partial: some points stay open, listed in open_points. " +
			"no-change: the item needs no code change, explained in the summary. In a task run, complete and partial need new commits " +
			"and no-change allows none. Your run ends when the call returns.",
		input: object(
			required("status", str("How the run ends.", "complete", "partial", "no-change")),
			required("summary", str("What was done, or why nothing needed to change.")),
			optional("open_points", array("The points still open, for partial.", str("One open point."))),
		),
	},
	domain.ToolSubmitReview: {
		description: "Report the result of your review: a summary and the findings. This is the only way to report; " +
			"Anthill posts each finding as an inline comment and the summary as a comment.",
		input: object(
			required("summary", str("The overall assessment of the change.")),
			required("findings", array("The problems found; empty when there are none.", findingSchema)),
		),
	},
	domain.ToolFinishFixes: {
		description: "End your fix pass: for each finding you were given, report fixed with the commit that fixes it, or skipped with a reason. " +
			"Anthill then verifies the fixes, pushes them and replies on the threads.",
		input: object(
			required("results", array("One result for each finding you were given.", fixResultSchema)),
		),
	},
}
