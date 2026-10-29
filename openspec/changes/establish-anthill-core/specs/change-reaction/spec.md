## Purpose

Defines how Anthill reacts when a ticket or pull request it is involved in changes, such as an edited description, a new comment, review feedback, or the opt-in label being removed. It decides whether an ant must act and routes the change to a running ant or to a resumed session.

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
On an ant-authored PR, shepherd findings with severity `blocking` or `suggestion`, of either classification, SHALL be actionable for the instance owning the ant. They SHALL start one follow-up run per round, covering those findings plus the round's `auto-fixable` nits. The run SHALL reply on each finding's thread, and its push starts the next round while rounds remain. Other nits SHALL be left to developers, and nits alone SHALL start no run.

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

### Requirement: Relevance classification
Each human change on a ticket or PR the instance is responsible for SHALL be classified as `actionable` (requires code or answer), `informational` (no action), or `command` (an `/anthill` command). Edits to title, body or acceptance criteria SHALL always be `actionable`. Comments SHALL be classified by an agent, or follow agent-conversation addressing rules when configured.

#### Scenario: Thank-you comment
- **WHEN** a developer comments "thanks, looks good" on an ant PR
- **THEN** the change is classified `informational`, and no ant is started

#### Scenario: Requirement changed
- **WHEN** the ticket body gets a new acceptance criterion while its PR is open
- **THEN** the change is classified `actionable`

### Requirement: Delivery to a running ant
An actionable change on an item whose ant is `running` SHALL be delivered to that ant during its run when the runner supports live input. Otherwise it SHALL be queued and delivered when the run ends, before the PR is opened.

#### Scenario: Ticket edited while ant runs
- **WHEN** the description of #12 is edited while its ant is running on a runner that supports live input
- **THEN** the ant receives a message with the diff of the description and continues with it

### Requirement: Resuming for follow-up work
An actionable change on an item with an open ant PR and no running ant SHALL start a follow-up ant run. The run resumes the prior session in the same worktree, gets a fresh time budget (default: half the base budget), and receives the change as its input. The follow-up SHALL push to the existing PR branch and SHALL NOT open a new PR.

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

### Requirement: Withdrawal of opt-in
If the opt-in marker is removed from a claimed item, or the item is closed, Anthill SHALL stop any running ant for it, keep the worktree, release the task claim, and notify the queen. An open ant PR SHALL be left to the developers.

#### Scenario: Label removed mid-run
- **WHEN** the `anthill` label is removed from #12 while its ant runs
- **THEN** the ant is stopped and `abandoned` with reason "opt-in withdrawn", and the claim is released

### Requirement: Changes are not lost across restarts
Change events not yet handled SHALL be persisted locally, and SHALL be handled after a daemon restart exactly once.

#### Scenario: Restart with a pending change
- **WHEN** the daemon restarts after detecting an actionable comment but before acting on it
- **THEN** after restart the comment is handled once, not zero or two times
