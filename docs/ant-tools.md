# Ant tools

Agents do everything that leaves the machine through the ant tools (spec ant-tools, design D4 and D5). An agent decides; Anthill checks the call and executes it. This page describes every tool, the agent roles that have it, and the reasons a call is refused.

## How agents get the tools

When Anthill starts a run, or a review or fix agent, it adds one MCP server to the agent's configuration:

```
anthill mcp --ant <token>
```

This process speaks MCP over stdio with the agent. It holds no tool logic: at start it asks the daemon, over the local control interface, for the tools of its token, and then relays every call to the daemon. If the daemon is not running or does not know the token, `anthill mcp` exits with an error before the agent can do any work.

### Tool tokens

Each run, and each review or fix agent, gets its own token (`ant_` followed by 43 random characters). A token is valid only for that one run of that one ant, or for that one round's agent, and only until Anthill revokes it when the run ends. A call with an unknown or revoked token is refused and nothing is executed.

The daemon keeps tokens in memory only, never on disk. Restarting the daemon therefore revokes every token. This is safe because a restart also ends or resumes every run that was active (spec ant-execution, "Restart resilience"), and a resumed run gets a new token.

### Results and refusals

A successful call returns its result as JSON text, and as structured content for clients that read it. A refused or failed call returns an error result whose text is the reason, in words the agent can act on. A refused call has no effect.

Messages for the agent, such as a late extension decision, are attached to the result of its next call as notices that start with `Anthill notice:`.

## Roles

| Role | Tools |
|------|-------|
| task run | `get_task`, `time_remaining`, `request_extension`, `ask_question`, `report_progress`, `comment`, `reply_to_thread`, `push`, `open_pull_request`, `update_pull_request`, `finish` |
| follow-up run | the task run's tools without `open_pull_request` |
| review agent | `submit_review` |
| fix agent | `finish_fixes` |

An agent's tool list holds only its role's tools.

## Checks on every call

Anthill checks every call in this order and refuses it at the first check that fails:

| Check | Refusal |
|-------|---------|
| The token is known and not revoked. | `unknown or revoked tool token` |
| The tool exists. | `unknown tool <name>` |
| The tool belongs to the agent's role. | `<tool> is not available to the <role>`, for example `push is not available to the fix agent` |
| The arguments match the tool's input schema. | what is wrong, for example `minutes must be at least 1, not 0` |
| For a run: the ant is `starting` or `running`. While it is `awaiting-extension`, only `get_task` and `time_remaining` are allowed. | `waiting for the extension decision`; `the run is finishing: no more tool calls` when the ant is `finishing`; `the run has ended` in any other state |
| This instance still holds the claim: the task claim on the item for a run, the review claim on the PR for a review or fix agent. The check uses the local list of held claims, which the claim service keeps current. | `the claim on <ref> is no longer held by this instance: stop working` |

The calls of one ant are executed one at a time. A provider error makes the call fail with `the call failed: ` and the error.

## Tools

The tools marked *not implemented yet* are listed in the tool list of their roles, but a call to them is refused with `<tool> is not implemented yet` after the checks above.

### `get_task`

Roles: task run, follow-up run.

Reads the item fresh from the tracker, not from the brief, and the ant PR fresh from the code host.

- Arguments: none.
- Result: `item` (`ref`, `url`, `title`, `body`, `acceptance_criteria`, `labels`), `comments` (`author`, `body`, `created_at` of each human comment; Anthill's own comments are left out, as in the brief), `branch`, `base`, `budget` (`total_minutes`, `remaining_minutes`) and, once the PR is open, `pr` (`url`, `draft`).
- Refusals: only the checks on every call. It is allowed while the ant is `awaiting-extension`.

### `time_remaining`

Roles: task run, follow-up run.

- Arguments: none.
- Result: `remaining_minutes`, `total_minutes` and `used_minutes`, in whole minutes rounded down. `warning` holds the warning text once the warning threshold is reached, such as `12 minutes remaining: wrap up and open a PR, or request more time`. `open_extension` (`minutes`, `reason`, `requested_at`) is the request still waiting for the queen.
- Refusals: only the checks on every call. It is allowed while the ant is `awaiting-extension`.

### `request_extension`

Roles: task run, follow-up run.

Applies the extension rules of ant-timebox:

- a request that would take the budget over its ceiling is denied with `maximum budget reached`;
- a request within the repository's auto-approve allowance is approved at once;
- otherwise the ant is `awaiting-extension` while the call waits for the queen, up to the configured `extension-wait` (default 5 minutes). Its budget does not count meanwhile. When the queen decides, the call returns the decision. When the wait ends without a decision, the call returns `pending`: the ant is `running` again with its budget counting, and the request stays open for the queen. The queen's later decision is delivered as a notice.

While a request is open, a new call does not make a second request; it waits for the open one again.

- Arguments: `minutes` (integer, at least 1), `reason` (string).
- Result: `status` (`approved`, `reduced`, `denied` or `pending`), `granted_minutes`, `remaining_minutes` and `message`.
- Refusals: the checks on every call. While it waits, other calls of the same run are refused with `waiting for the extension decision`, except `get_task` and `time_remaining`.

### `ask_question`

Roles: task run, follow-up run. *Not implemented yet.*

Posts a question on the item, or on the ant PR. When the call returns, Anthill stops the runner with the session saved, and the ant is `awaiting-input` until a developer answers.

- Arguments: `text` (string); `where` (`item` or `pr`), by default the item in a task run and the PR in a follow-up run.

### `report_progress`

Roles: task run, follow-up run. *Not implemented yet.*

Writes the run's single status comment on the item. The first call posts it and later calls replace its text.

- Arguments: `text` (string).

### `comment`

Roles: task run, follow-up run. *Not implemented yet.*

Posts a comment with the ant's marker on the item or the ant PR, with secrets redacted.

- Arguments: `where` (`item` or `pr`), `body` (string).

### `reply_to_thread`

Roles: task run, follow-up run. *Not implemented yet.*

Replies in a review thread of the ant PR, with the ant's marker, and resolves the thread when asked.

- Arguments: `thread` (string), `body` (string); `resolve` (boolean, default false).

### `push`

Roles: task run, follow-up run. *Not implemented yet.*

Pushes the worktree's current branch under the branching policy, never with force. It returns git's result, including rejections and conflicts.

- Arguments: none.

### `open_pull_request`

Roles: task run. *Not implemented yet.*

Opens the ant PR against the base branch. The agent writes the title and body and chooses draft or ready; Anthill adds the closing reference to the item and the `anthill` label.

- Arguments: `title` (string), `body` (string); `draft` (boolean, default false).

### `update_pull_request`

Roles: task run, follow-up run. *Not implemented yet.*

Changes the ant PR's title, body or draft state.

- Arguments: at least one of `title` (string), `body` (string), `draft` (boolean).

### `finish`

Roles: task run, follow-up run. *Not implemented yet.*

Ends the run with its status.

- Arguments: `status` (`complete`, `partial` or `no-change`), `summary` (string); `open_points` (array of strings).

### `submit_review`

Roles: review agent. *Not implemented yet.*

Reports a review round's result. It is the review agent's only tool.

- Arguments: `summary` (string); `findings`, each with `severity` (`blocking`, `suggestion` or `nit`), `classification` (`auto-fixable` or `needs-developer`) and `message`, and optionally `file`, `start_line`, `end_line` and `suggested_fix`.

### `finish_fixes`

Roles: fix agent. *Not implemented yet.*

Ends a fix pass with one result for each finding the agent was given. It is the fix agent's only tool.

- Arguments: `results`, each with `finding` (the finding's id) and `status` (`fixed` or `skipped`), plus `commit` for a fixed finding or `reason` for a skipped one.
