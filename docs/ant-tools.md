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

Roles: task run, follow-up run.

Posts a `question` comment on the item, or on the ant PR, and notifies the queen. When the call returns, the ant is `awaiting-input` with its budget paused, and Anthill stops the runner with the session saved. A later run resumes the session with the answer.

- Arguments: `text` (string); `where` (`item` or `pr`), by default the item in a task run and the PR in a follow-up run.
- Result: `url` of the question and `message`.
- Refusals: the checks on every call; `you have no pull request yet: push and open one with open_pull_request first` for `where: pr` before the PR is open.

### `report_progress`

Roles: task run, follow-up run.

Writes the run's single `status` comment on the item. The first call of a run posts it and later calls replace its text. The text is cut to the summary length (4,000 characters).

- Arguments: `text` (string).
- Result: `url` of the status comment.

### `comment`

Roles: task run, follow-up run.

Posts a `reply` comment on the item or the ant PR.

- Arguments: `where` (`item` or `pr`), `body` (string).
- Result: `url` of the comment.
- Refusals: the checks on every call; the missing PR refusal of `ask_question` for `where: pr`.

### `reply_to_thread`

Roles: task run, follow-up run.

Replies in a review thread of the ant PR with a `reply` comment, and resolves the thread when asked.

- Arguments: `thread` (string, the thread's id), `body` (string); `resolve` (boolean, default false).
- Result: `url` of the reply and `resolved`.
- Refusals: the checks on every call; the missing PR refusal. An unknown thread makes the call fail.

### Text Anthill posts for the ant

Every comment, reply, question and status comment posted on behalf of an ant starts with the visible `🐜 Ant (<instance>)` prefix and carries the hidden marker with the matching kind (`question`, `status` or `reply`). Before Anthill posts any text for an ant, PR titles and bodies included, it makes the text safe (spec agent-conversation, "Comment content safety"):

- the absolute path of the worktree becomes `.`, and the user's home directory `~`;
- every string matching a secret pattern, built-in or added in the configuration, becomes `[REDACTED]`;
- summaries and `report_progress` are cut to the summary length with a note; comments, replies and PR bodies are not cut.

### `push`

Roles: task run, follow-up run.

Pushes the worktree's current branch, never with force. Anthill first applies the branching policy: only the ant's own branch, never the base branch, the remote's default branch or a protected branch, and never from a detached HEAD. It then runs `git push` from the ant's worktree with the developer's credentials and the repository's own hooks given back for that one command, so the repository's pre-push hook checks the ant's code and can stop the push.

- Arguments: none.
- Result: `status` `pushed` and git's `output`.
- Refusals: the checks on every call;
  - `push to <branch> is not allowed`, with the reason, from the branching policy;
  - `push rejected: the remote branch has commits this worktree lacks; they were fetched as origin/<branch>: merge them (git merge origin/<branch>) and call push again`, followed by git's output. Merge, then push again;
  - `push rejected: the push was rejected as non-fast-forward: the branch rewrites commits that were already pushed. Keep the published history and add a new commit`, after an amend or a rebase of pushed commits;
  - `push failed: …` with git's output, for anything else, such as a pre-push hook that refused.

### `open_pull_request`

Roles: task run.

Opens the ant PR from the ant branch to the base branch. The agent writes the title and body and chooses draft or ready. Anthill adds the closing reference on its own line (`Closes #12`, `Closes owner/repo#12` across repositories, or `AB#12` for Azure DevOps) and the opt-in label (default `anthill`). It opens a draft whatever the agent chose when the ant's last ended run finished `partial` or timed out and no run finished `complete` since.

- Arguments: `title` (string), `body` (string); `draft` (boolean, default false).
- Result: `url`, `title`, `draft`, and `forced_draft` with the reason when the draft was forced.
- Refusals: the checks on every call;
  - `the item already has an open ant PR <url>: use update_pull_request`, when the ant has a PR or an open PR comes from the ant branch;
  - `the branch <branch> is not on the remote: call push first`;
  - `the branch has <n> commits that are not pushed: call push first`.

### `update_pull_request`

Roles: task run, follow-up run.

Changes the ant PR's title, body or draft state. A new body keeps the closing reference.

- Arguments: at least one of `title` (string), `body` (string), `draft` (boolean).
- Result: as for `open_pull_request`.
- Refusals: the checks on every call; `give at least one of title, body and draft`; the missing PR refusal; `the PR stays a draft until a run finishes complete: …` for `draft: false` while the draft is forced.

### `finish`

Roles: task run, follow-up run.

Ends the run with its status. In a task run, `complete` and `partial` need new commits on the branch after the base branch, and `no-change` allows none; a follow-up run may use any status. The ant becomes `finishing`, and Anthill ends the run and acts on the outcome: it parks or releases the claim, opens or updates the PR, and marks a draft ready after `complete` (spec ant-execution).

- Arguments: `status` (`complete`, `partial` or `no-change`), `summary` (string); `open_points` (array of strings).
- Result: `status` `accepted` and `message`.
- Refusals: the checks on every call; `no commits on the branch: finish with no-change, or ask a question`; `the branch has new commits: finish with complete or partial`.

### `submit_review`

Roles: review agent.

Reports a review round's result. It is the review agent's only tool, and it can be called once. Anthill posts each finding with a known location as an inline comment and the rest in the round's summary (spec pr-shepherd).

- Arguments: `summary` (string); `findings`, each with `severity` (`blocking`, `suggestion` or `nit`), `classification` (`auto-fixable` or `needs-developer`) and `message`, and optionally `file`, `start_line`, `end_line` and `suggested_fix`.
- Result: `status` `submitted` and `message`.
- Refusals: the checks on every call; `findings[<i>]: …` for a finding with lines but no file or an end line before its start line; `already reported: this agent's result was handed over` for a second call.

### `finish_fixes`

Roles: fix agent.

Ends a fix pass with one result for each finding the agent was given. It is the fix agent's only tool, and it can be called once. Anthill then runs the verification command, pushes the fixes and replies on the threads, and reclassifies skipped findings as `needs-developer` (spec pr-shepherd).

- Arguments: `results`, each with `finding` (the finding's id) and `status` (`fixed` or `skipped`), plus `commit` for a fixed finding (its hash, or at least its first 7 characters) or `reason` for a skipped one.
- Result: `status` `submitted` and `message`.
- Refusals: the checks on every call;
  - `no result for finding <id>: report each finding as fixed or skipped`;
  - `finding <id> has more than one result`, `finding "<id>" was not given to this fix pass`;
  - `finding <id>: commit <hash> is not a new commit of this fix pass`, or too short, or ambiguous;
  - `finding <id> is skipped without a reason`;
  - `already reported: this agent's result was handed over` for a second call.
