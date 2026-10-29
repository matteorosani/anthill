## Purpose

Defines how Anthill reacts when a ticket or pull request it is involved in changes, such as an edited description, a new comment, review feedback, or the opt-in label being removed. It decides whether an ant must act and routes the change to the ant's active run, to a follow-up run, or to the ant's next run.

## ADDED Requirements

### Requirement: Only the responsible instance reacts
Changes on a claimed ticket and on its ant PR SHALL be handled only by the instance that holds the ticket's task claim, whether live or parked. Changes on other in-scope PRs SHALL be handled by whichever instance acquires the `review` claim.

#### Scenario: Colleague's machine ignores the change
- **WHEN** a comment is added to #12, which instance A has claimed
- **THEN** only instance A evaluates the comment, and instance B takes no action

### Requirement: Anthill-authored changes are ignored
Change events flagged as Anthill-authored SHALL NOT trigger any reaction. This prevents feedback loops between instances and with Anthill itself. The only exception is shepherd findings on ant PRs, defined below.

#### Scenario: Status comment by another instance
- **WHEN** another instance posts a `status` or `claim` comment on an item this instance owns
- **THEN** no reaction is triggered

### Requirement: Shepherd findings on ant PRs
On an ant PR, shepherd findings with severity `blocking` or `suggestion`, of either classification, SHALL be actionable for the instance owning the ant. They SHALL start one follow-up run per round, covering those findings plus the round's `auto-fixable` nits. The run SHALL reply on each finding's thread, and its push starts the next round while rounds remain. Other nits SHALL be left to developers, and nits alone SHALL start no run.

#### Scenario: Blocking finding fixed by the ant
- **WHEN** round 1 on the ant PR for #12 posts a `blocking` finding
- **THEN** the owning instance starts a follow-up run, the run pushes a fix and replies on the thread, and round 2 reviews the new head commit

#### Scenario: Auto-fixable nit included in the run
- **WHEN** a round on an ant PR posts a `suggestion` finding and an `auto-fixable` `nit`
- **THEN** one follow-up run handles both, and no fix agent runs

#### Scenario: Nit left alone
- **WHEN** a round posts only `nit` findings on an ant PR
- **THEN** no follow-up run starts

#### Scenario: Round limit reached
- **WHEN** the follow-up run after the last allowed round pushes a fix
- **THEN** no further round starts automatically, and any finding still open stays with developers

### Requirement: Who can trigger work
Only changes by users with write access to the repository, and edits to a ticket by the ticket's author, SHALL be classified as `actionable` or `command`. All other changes SHALL be `informational`. Commands follow agent-conversation authorization.

#### Scenario: Outside commenter on a public repository
- **WHEN** a user without write access comments on a claimed ticket
- **THEN** the comment is `informational`, and no run starts

### Requirement: Relevance classification
Each human change on a ticket or PR the instance is responsible for SHALL be classified as `actionable` (requires code or answer), `informational` (no action), or `command` (an `/anthill` command). Edits to title, body or acceptance criteria SHALL always be `actionable`. Comments SHALL be classified by an agent.

#### Scenario: Thank-you comment
- **WHEN** a developer comments "thanks, looks good" on an ant PR
- **THEN** the change is classified `informational`, and no run starts

#### Scenario: Comment from a user without write access
- **WHEN** a user without write access comments "please also add dark mode" on the ant PR
- **THEN** the change is `informational`, and no run starts

#### Scenario: Ticket author edits without write access
- **WHEN** the author of #12, who has no write access, edits its body
- **THEN** the change is `actionable`

#### Scenario: Requirement changed
- **WHEN** the ticket body gets a new acceptance criterion while its PR is open
- **THEN** the change is classified `actionable`

### Requirement: Delivery to an active run
An actionable change on an item whose ant has an active run SHALL be delivered to that run through the means defined in ant-tools. A change detected after the run called `finish` SHALL be handled as for an ant without an active run.

#### Scenario: Ticket edited during a run
- **WHEN** the description of #12 is edited while its ant has an active run on a runner that supports live input
- **THEN** the run receives a message with the diff of the description and continues with it

#### Scenario: Runner without live input or hooks
- **WHEN** the description of #12 is edited during a run on a runner without live input or hooks
- **THEN** the diff is attached to the result of the run's next ant-tool call

### Requirement: Changes while the ant waits
An actionable change on an item whose ant is `queued`, `awaiting-input`, `stopped`, `failed` or `timed-out` SHALL start no run. It SHALL be stored, the queen SHALL be notified, and it SHALL be added to the input of the ant's next run: the start, the resumption after an answer, a retry or a continue.

#### Scenario: Comment on a timed-out ant's draft PR
- **WHEN** a developer comments on the draft PR of a timed-out ant before the queen decides
- **THEN** no run starts, the queen is notified, and the comment is part of the input if the queen continues the ant

#### Scenario: Ticket edited while waiting for an answer
- **WHEN** the body of #12 is edited while its ant is `awaiting-input`
- **THEN** the edit is given, together with the answer, to the run that resumes the session

### Requirement: Resuming for follow-up work
An actionable change on an item whose ant is `succeeded` with an open ant PR SHALL start a follow-up run. The run resumes the prior session in the same worktree, gets a fresh time budget (default: half the base budget), and receives as input all actionable changes stored since the last run. It SHALL push to the existing PR branch and SHALL NOT open a new PR.

#### Scenario: Two comments in one poll
- **WHEN** two actionable comments on the ant PR for #12 are detected in the same poll
- **THEN** one follow-up run starts with both comments as input

#### Scenario: Reviewer asks for a change
- **WHEN** a developer comments on the ant PR for #12, "please also handle the empty-password case"
- **THEN** the previous session is resumed, it commits the fix to the same branch, and it replies on the comment with what changed

### Requirement: Follow-ups start automatically by default
Follow-up runs SHALL start automatically when an actionable change is detected, and the queen SHALL be notified with the triggering change. When a repository sets `auto-follow-up: false`, follow-up runs SHALL instead be proposed to the queen and started only after acceptance.

#### Scenario: Auto-follow-up enabled (default)
- **WHEN** an actionable PR comment arrives in a repository with default settings
- **THEN** the follow-up run starts without waiting for the queen, and the queen is notified

#### Scenario: Auto-follow-up disabled
- **WHEN** an actionable PR comment arrives in a repository with `auto-follow-up: false`
- **THEN** a follow-up proposal appears in the queen console, and no agent starts until the queen accepts

### Requirement: Follow-up limits
The number of follow-up runs per PR SHALL be limited by configuration (default 5). When the limit is reached, actionable changes SHALL only be reported to the queen.

#### Scenario: Limit reached
- **WHEN** a PR has had 5 follow-up runs and another actionable comment arrives
- **THEN** no run is proposed automatically, and the queen is notified that the follow-up limit was reached

### Requirement: Hold freezes the ant
When the hold label (default `anthill:hold`) is added to an item whose ant is not final, an active run SHALL finish normally. While the label is present, no run SHALL start for the item, the claim SHALL stay, and actionable changes SHALL be stored. When the label is removed, stored changes SHALL be handled as if they had just arrived.

#### Scenario: Hold added during a run
- **WHEN** a developer adds `anthill:hold` to #12 while its ant has an active run
- **THEN** the run continues to its end, and no follow-up run starts afterwards while the label is present

#### Scenario: Retry while held
- **WHEN** the queen retries a failed ant whose item carries `anthill:hold`
- **THEN** the retry is refused with "item is on hold"

### Requirement: Withdrawal of opt-in
If the opt-in label is removed from a claimed item, or the item is closed, Anthill SHALL end any active run for it, keep the worktree, release the task claim, and notify the queen. An open ant PR SHALL be left to the developers.

#### Scenario: Label removed mid-run
- **WHEN** the `anthill` label is removed from #12 while its ant has an active run
- **THEN** the run is ended, the ant is `abandoned` with reason "opt-in withdrawn", and the claim is released

### Requirement: Changes are not lost across restarts
Change events not yet handled SHALL be persisted locally, and SHALL be handled after a daemon restart exactly once.

#### Scenario: Restart with a pending change
- **WHEN** the daemon restarts after detecting an actionable comment but before acting on it
- **THEN** after restart the comment is handled once, not zero or two times
