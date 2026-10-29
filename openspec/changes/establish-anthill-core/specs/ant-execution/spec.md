## Purpose

Defines the lifecycle of an ant: a headless coding agent that Anthill starts for an accepted work item, runs in an isolated worktree, supervises, can resume, and that ends its work by opening a pull request.

## ADDED Requirements

### Requirement: Agent runners are interchangeable
Anthill SHALL start ants through an agent runner abstraction. Claude Code, Codex and OpenCode SHALL be supported runners. The runner used SHALL be selected by repository configuration, then by an `agent:<name>` label on the item, then by the global default.

#### Scenario: Label overrides repository default
- **WHEN** a repository's default runner is Claude Code and the item has the label `agent:codex`
- **THEN** the ant for that item is started with Codex

#### Scenario: Runner not installed
- **WHEN** the selected runner's CLI is not available on the machine
- **THEN** acceptance fails with "runner codex not available", and no claim is taken

### Requirement: Starting an ant on acceptance
When the queen accepts an eligible item and capacity allows, Anthill SHALL, in this order: acquire the task claim, create the branch and worktree defined by branching-policy, and start the runner headlessly in that worktree with the task brief. If any step fails, the steps already done SHALL be undone.

#### Scenario: Claim lost at start
- **WHEN** claim acquisition fails because another instance won the race
- **THEN** no worktree or branch is created, and the queen is told who holds the item

#### Scenario: Queued for capacity
- **WHEN** the queen accepts an item while capacity is full
- **THEN** the item is queued, and it is claimed and started when a slot frees, if it is still eligible at that moment

### Requirement: Task brief
The ant SHALL receive a task brief containing: the item's title, body, acceptance criteria, comments, the item URL, the branch and base branch, the time budget, the repository's agent instructions if any, and the Anthill conventions it must follow: which ant tools to use to ask questions, request time, push, open the PR and finish.

#### Scenario: Brief includes discussion
- **WHEN** an item has three human comments clarifying scope
- **THEN** all three comments appear in the brief, and Anthill's own claim comments do not

### Requirement: Ant lifecycle states
Each ant SHALL be in exactly one state: `queued`, `starting`, `running`, `awaiting-input`, `awaiting-extension`, `finishing`, `succeeded`, `failed`, `timed-out` or `abandoned`. Every transition SHALL be recorded with a timestamp and a reason, and SHALL be visible in the queen console.

#### Scenario: Ant asks a question
- **WHEN** a running ant posts a question to the developer
- **THEN** the ant moves to `awaiting-input` and its time budget is paused

### Requirement: Sessions are persisted for resumption
Anthill SHALL record each ant's runner session identifier, worktree path and branch. A later ant for the same item SHALL resume that session when the runner supports it. Otherwise it SHALL start a new session with a brief that summarizes prior work (commits, PR, conversation).

#### Scenario: Resume after an answer
- **WHEN** a developer answers an ant's question
- **THEN** the same session is resumed in the same worktree with the answer as its next input

#### Scenario: Runner without resume support
- **WHEN** the runner cannot resume sessions
- **THEN** a new session starts in the existing worktree with a brief containing the earlier commits and the answer

### Requirement: Session index
Anthill SHALL keep a local record per work item with: provider and item id, runner, runner session id, worktree path, branch, PR link, ant state, budget used, and the history of runs. The session id SHALL be captured from the runner when the run starts. A change on a PR SHALL be matched to its record through the PR's closing reference to the item, or else through its `ant/` branch name.

#### Scenario: Comment on an ant PR finds its session
- **WHEN** a developer comments on the PR from `ant/gh-12-fix-login`, whose body contains `Closes #12`
- **THEN** Anthill finds the record for `gh-12` and resumes its session id in its worktree

#### Scenario: PR body reference removed
- **WHEN** a developer edits the PR body and removes `Closes #12`
- **THEN** the PR is still matched to `gh-12` through its branch name

### Requirement: Session index recovery
If the local record for an item claimed by this instance is missing, Anthill SHALL rebuild it from the provider and git: claim comments carrying this instance id, `ant/` branches, and their PRs. A rebuilt record has no runner session, so the next run SHALL start a new session with a brief summarizing the item, earlier commits, the PR and its conversation.

#### Scenario: Local database lost
- **WHEN** the local store is deleted while this instance holds a parked claim on #12 with an open ant PR
- **THEN** at startup the record for `gh-12` is rebuilt, the worktree is recreated from the pushed branch if missing, and the next follow-up starts a new session with a summary brief

### Requirement: Exit without finishing
If the runner exits or crashes without the ant calling `finish`, the ant SHALL become `failed`. Anthill SHALL push the branch when it has new commits, SHALL NOT open a PR, and SHALL post a failure summary with the agent's last message on the ticket. If an ant PR is already open, Anthill SHALL mark it as a draft. The worktree SHALL be kept, and the queen SHALL be able to retry, which resumes the session, or abandon.

#### Scenario: Commits but no finish
- **WHEN** an ant's process exits cleanly with two new commits and no `finish` call
- **THEN** the branch is pushed, no PR is opened, the ant is `failed` with reason "exited without finish", and the ticket gets the failure summary with the agent's last message

#### Scenario: Crash before any commit
- **WHEN** the runner crashes before the ant made any commit
- **THEN** nothing is pushed, the ant is `failed`, and the failure summary is posted on the ticket

#### Scenario: Follow-up run exits without finish
- **WHEN** a follow-up run on the open ant PR for #12 crashes after one new commit
- **THEN** the commit is pushed to the PR branch, the PR is marked as a draft so no review round starts, and the ant is `failed`

#### Scenario: Retry after crash
- **WHEN** the queen retries a failed ant
- **THEN** the same worktree is reused and the session is resumed with a note about the failure

### Requirement: Agent process boundary
Agents SHALL run as separate processes in their worktree, holding no provider tokens and no git push credentials. Anthill SHALL launch them with provider credentials removed from the environment and the git credential helper disabled, and SHALL install the redirect hooks from ant-tools where supported. This is a guard, not a sandbox: the agent still runs as the developer's OS user.

#### Scenario: Stray git push
- **WHEN** an ant runs `git push` directly on a runner without hooks
- **THEN** the push fails for lack of credentials, and nothing reaches the remote

### Requirement: Finishing with a pull request
An ant SHALL finish successfully by pushing its branch and opening a PR against the base branch through the ant tools, then calling `finish`. The agent writes the PR title and body and chooses whether it is a draft. Anthill adds the closing reference (`Closes #12` / `AB#12`) and the `anthill` label. If the ant calls `finish` with new commits but no PR, Anthill SHALL push and open the PR itself from the finish summary.

#### Scenario: Successful finish
- **WHEN** an ant completes item #12, calls `push` and `open_pull_request`, then `finish(complete)`
- **THEN** a PR from `ant/gh-12-…` to the base branch exists with the agent's title and body plus `Closes #12`, the ant is `succeeded`, and the task claim is parked

#### Scenario: Finish without opening a PR
- **WHEN** an ant calls `finish(complete)` with new commits but never called `open_pull_request`
- **THEN** Anthill pushes the branch and opens a PR whose title follows the ticket and whose body is the finish summary

### Requirement: The ant decides when work is done
Anthill SHALL rely on the status the ant reports with `finish` (`complete`, `partial` or `no-change`). It SHALL NOT run a build, tests or a verification command before or after the PR is opened. CI on the PR and the PR Shepherd are the checks.

#### Scenario: Complete without local verification
- **WHEN** an ant opens a ready PR for #12 and calls `finish(complete)`
- **THEN** Anthill runs no build or tests, and the PR stays ready for review

### Requirement: No change needed
When an ant concludes that its item needs no code change, it SHALL call `finish(no-change)` with an explanation and no new commits. On a task run, Anthill SHALL post the explanation on the ticket, add the exclusion marker (default `anthill:hold`), release the task claim, and set the ant to `succeeded` with outcome no-change. A human then closes the ticket or removes the marker.

#### Scenario: Bug already fixed
- **WHEN** the ant for #12 finds that the reported bug was already fixed on the base branch and calls `finish(no-change)` with the reason
- **THEN** no PR is opened, the explanation is posted on #12, `anthill:hold` is added, the claim is released, and the ant is `succeeded` with outcome no-change

#### Scenario: Follow-up needing no change
- **WHEN** a follow-up run for a reviewer's question replies on the thread and calls `finish(no-change)`
- **THEN** nothing is pushed, the PR is left as it is, no marker is added, and the claim stays parked

### Requirement: Draft PRs for incomplete work
The agent's draft choice SHALL be honoured, except that when an ant reports partial completion, or its time budget runs out (ant-timebox), Anthill SHALL make the PR a draft regardless of the agent's choice and list the open points in the PR body. When a later run for the same item finishes with status `complete`, Anthill SHALL mark the draft PR ready for review.

#### Scenario: Partial completion
- **WHEN** the ant finishes with status "partial" and two unresolved points
- **THEN** a draft PR is opened listing both points

#### Scenario: Draft completed by a later run
- **WHEN** a resumed run for an item with a draft ant PR finishes with status `complete`
- **THEN** its commits are pushed to the same PR, the PR is marked ready for review, and the shepherd picks it up

### Requirement: Abandoning an ant
The queen SHALL be able to abandon an ant in any non-terminal state. Abandoning stops the runner process and releases the task claim. The worktree SHALL be kept until the queen explicitly cleans it up.

#### Scenario: Abandon a running ant
- **WHEN** the queen abandons a running ant
- **THEN** the agent process is terminated within 10 seconds, the claim is released, and the ant is `abandoned`

### Requirement: Usage is recorded
For every ant run and shepherd round, Anthill SHALL record duration and, when the runner reports them, tokens and cost. When the runner does not report them, they SHALL be recorded as unknown. Usage records SHALL be stored locally, leave the machine only through observability export when it is on, and SHALL NOT limit or stop runs.

#### Scenario: Runner reports cost
- **WHEN** an ant run ends and the runner reported 1.2M tokens and $3.40
- **THEN** the run's usage record holds those values

#### Scenario: Runner without usage data
- **WHEN** a runner reports no token or cost data
- **THEN** the run's tokens and cost are recorded as unknown, and its duration is still recorded

### Requirement: Restart resilience
After the daemon restarts, Anthill SHALL reconcile ants that were not in a terminal state using the recorded sessions, worktrees and the provider state. It SHALL resume or mark them failed, and SHALL NOT start duplicate work.

#### Scenario: Daemon restarted mid-run
- **WHEN** the daemon restarts while an ant was `running`
- **THEN** the ant is handled as an exit without finishing with reason "interrupted by restart", or resumed automatically if the repository sets `auto-resume` (anthill-configuration), and its claim is kept
