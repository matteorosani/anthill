## Purpose

Defines the only channel between Anthill and the agents it runs: the ant tools. It covers how the tools are given to an agent, the set of tools and what each does, the policy checks Anthill applies to every call, and how Anthill delivers messages such as time warnings to a running agent.

## ADDED Requirements

### Requirement: Tools are delivered as a local stdio MCP server
Anthill SHALL give every ant and shepherd agent its tools as an MCP server it starts over stdio (`anthill mcp --ant <token>`). The server SHALL be added to the agent's own configuration when the agent is launched. It relays calls to the daemon over the local control interface. No other channel (CLI, HTTP) SHALL be offered.

#### Scenario: Tools available at start
- **WHEN** an ant starts with Claude Code
- **THEN** the Anthill tools appear in the agent's tool list with their descriptions and parameter schemas, and they are allowed without a permission prompt

#### Scenario: MCP server fails to start
- **WHEN** the Anthill MCP server cannot start for an ant
- **THEN** the run is aborted before the agent does any work, and the ant becomes `failed` with reason "ant tools unavailable"

### Requirement: Tool tokens are scoped to one run
Each run SHALL get its own token, valid only for that ant and that run. The token SHALL be revoked when the run ends, and a call with an unknown or revoked token SHALL be rejected.

#### Scenario: Token reused after the run
- **WHEN** a process calls a tool with the token of a run that has ended
- **THEN** the call is rejected and nothing is executed

### Requirement: Task and time tools
Ants SHALL have:
- `get_task`, returning the current item, its comments, the branch, the base branch and the budget, read fresh rather than from the brief;
- `time_remaining`, returning the remaining budget and any pending warning.

#### Scenario: Ticket changed since the brief
- **WHEN** an ant calls `get_task` after the ticket body was edited
- **THEN** it receives the edited body

### Requirement: Extension tool
Ants SHALL have `request_extension(minutes, reason)`, which applies the ant-timebox rules. When the queen must decide, the call SHALL wait up to the configured extension wait (default 5 minutes). It then returns `approved`, `reduced`, `denied` or `pending`. `pending` means the request stays open while the ant keeps working against its current budget. A later decision is applied as defined by ant-timebox.

#### Scenario: Queen answers within the wait
- **WHEN** an ant requests 30 minutes and the queen approves 20 within 2 minutes
- **THEN** the call returns `reduced` with 20 minutes, and the budget grows by 20

#### Scenario: No answer within the wait
- **WHEN** the queen does not answer within 5 minutes
- **THEN** the call returns `pending`, the ant is `running` again with its budget counting, and a later decision is delivered as a message

### Requirement: Conversation tools
Ants SHALL have:
- `ask_question(text, where)`, which posts a question on the task or the PR. When the call returns, Anthill SHALL stop the runner itself with the session saved, and the run is paused (agent-conversation);
- `report_progress(text)`, which updates the run's single status comment;
- `comment(where, body)` and `reply_to_thread(thread, body, resolve)` for agent-written comments.

#### Scenario: Agent replies to a review thread
- **WHEN** a follow-up run calls `reply_to_thread` with "Handled the empty-password case in 3f2a1c" and `resolve: true`
- **THEN** Anthill posts the reply with its marker, and resolves the thread

#### Scenario: Runner stopped after a question
- **WHEN** an ant calls `ask_question` and keeps generating output after the call returns
- **THEN** Anthill stops the runner with the session saved, and the ant is `awaiting-input`

### Requirement: Push tool
Ants SHALL push only through `push()`, which pushes the worktree's current branch under branching-policy and returns git's result, including rejections and conflicts. Agents SHALL NOT have git push credentials.

#### Scenario: Push rejected because the remote moved
- **WHEN** an ant calls `push` and the remote branch has commits the worktree lacks
- **THEN** the call returns the rejection, and the ant can merge and call `push` again

### Requirement: Pull request tools
Ants SHALL have `open_pull_request(title, body, draft)` and `update_pull_request(title, body, draft)`. The agent decides the title, the body and whether the PR is a draft. Anthill SHALL add the provider closing reference to the item and the `anthill` label. It SHALL force a draft when the ant reports `partial` or has timed out.

#### Scenario: Agent opens a ready PR
- **WHEN** an ant calls `open_pull_request` with its own title and body and `draft: false`
- **THEN** a ready PR is opened with that title, the body plus `Closes #12`, and the `anthill` label

#### Scenario: Second PR for the same item
- **WHEN** an ant calls `open_pull_request` while its item already has an open ant PR
- **THEN** the call is rejected, and the ant is told to use `update_pull_request`

### Requirement: Finish tool
Ants SHALL end a run with `finish(status, summary, open_points)`, where status is `complete`, `partial` or `no-change`. Anthill SHALL then end the session, record the outcome, and park or release the claim. On a task run, `complete` and `partial` SHALL require new commits and `no-change` SHALL allow none. A `finish` without a PR on a branch with unpushed commits SHALL make Anthill push and open a PR (ant-execution).

#### Scenario: Finish after opening the PR
- **WHEN** an ant has opened its PR and calls `finish(complete, …)`
- **THEN** the ant becomes `succeeded`, and the task claim is parked

#### Scenario: Complete without commits
- **WHEN** a task ant calls `finish(complete, …)` and its branch has no new commits
- **THEN** the call is refused with "no commits on the branch: finish with no-change, or ask a question"

#### Scenario: No-change with commits
- **WHEN** a task ant calls `finish(no-change, …)` and its branch has new commits
- **THEN** the call is refused with "the branch has new commits: finish with complete or partial"

### Requirement: Review tool
Review agents SHALL report a round's result only through `submit_review(summary, findings)`, where each finding has file, line range, severity, classification, message and an optional suggested fix. Anthill SHALL post the findings as defined by pr-shepherd.

#### Scenario: Structured findings
- **WHEN** a review agent calls `submit_review` with two findings
- **THEN** Anthill posts two inline comments and one summary, each with its marker

### Requirement: Fix report tool
Fix agents SHALL end a fix pass with `finish_fixes(results)`, giving for each finding either `fixed` with the commit that fixes it, or `skipped` with a reason. Anthill SHALL then verify, push and reply as defined by pr-shepherd.

#### Scenario: One fix skipped
- **WHEN** a fix agent fixes one of two findings and calls `finish_fixes` with the second marked `skipped` with "needs an API decision"
- **THEN** Anthill handles the first as a fix to verify and push, and reclassifies the second as `needs-developer` with that reason

### Requirement: Tool sets per agent role
Anthill SHALL offer each agent only the tools of its role, and SHALL refuse calls to any other tool:
- task ant: the task, time, extension, conversation, push, pull request and finish tools;
- follow-up ant: the same, without `open_pull_request`;
- review agent: `submit_review` only;
- fix agent: `finish_fixes` only.

#### Scenario: Fix agent cannot push
- **WHEN** a fix agent calls `push`
- **THEN** the call is refused with "not available to the fix agent", and nothing is pushed

#### Scenario: Review agent tool list
- **WHEN** a review agent starts
- **THEN** its tool list contains only `submit_review`

### Requirement: Every call is checked and returns a reason
Before executing a tool call, Anthill SHALL check it against the ant's state, its claim and branching-policy. A refused call SHALL return a reason the agent can act on, and SHALL have no effect. Anthill SHALL add the marker and secret redaction to all text posted on behalf of the ant.

#### Scenario: Comment containing a secret
- **WHEN** an ant calls `comment` with a body containing a string that matches a secret pattern
- **THEN** the comment is posted with the value redacted

#### Scenario: Call while paused
- **WHEN** a paused ant's process calls `push`
- **THEN** the call is refused with "run is paused"

### Requirement: Messages to a running agent
Anthill SHALL deliver time warnings, extension decisions and ticket updates to a running agent through the first means the runner supports: a live input message, a hook that adds context after each tool use, or a notice attached to the result of the agent's next ant-tool call.

#### Scenario: Runner without live input
- **WHEN** an ant runs on a runner without live input or hooks and reaches 80% of its budget
- **THEN** the warning is attached to the result of its next ant-tool call

### Requirement: Provider commands are redirected to tools
When a runner supports hooks, Anthill SHALL install hooks that refuse `git push` and provider CLI commands (`gh`, `az`) run by the agent. The refusal message SHALL name the ant tool to use instead.

#### Scenario: Agent tries gh
- **WHEN** an ant on Claude Code runs `gh pr create`
- **THEN** the hook blocks the command and tells the agent to use `open_pull_request`
