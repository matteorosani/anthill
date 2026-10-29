## Purpose

Defines the time budget of every ant run: how time is counted, the early warning, how an ant asks for more time, and what happens when the budget runs out.

## ADDED Requirements

### Requirement: Every run has a time budget
Every ant run SHALL have a base budget, taken in this order: the queen's override at acceptance, a `budget:<minutes>` label or tag on the item, the repository setting, the global default (60 minutes). A label above the repository's maximum base budget (default 180 minutes) SHALL be capped at that maximum. The ant SHALL be told its budget in the task brief.

#### Scenario: Default budget
- **WHEN** an ant starts in a repository without a budget setting, on an item without a budget label
- **THEN** its budget is 60 minutes and the brief states it

#### Scenario: Budget label
- **WHEN** an item carries `budget:90`
- **THEN** its ant gets a 90-minute base budget, and the extension ceiling is 3 × 90 minutes

#### Scenario: Budget label above the maximum
- **WHEN** an item carries `budget:600` and the maximum base budget is 180 minutes
- **THEN** its ant gets 180 minutes, and the queen is told the label was capped

#### Scenario: Invalid budget label
- **WHEN** an item carries `budget:abc`
- **THEN** the label is ignored, the next source in the order applies, and the queen is warned

### Requirement: Budget counts active time only
The budget SHALL count wall-clock time only while the ant is `running` or `finishing`. Time spent in `queued`, `awaiting-input` or `awaiting-extension` SHALL NOT count.

#### Scenario: Paused while waiting for an answer
- **WHEN** an ant has used 30 minutes and then waits 2 hours for a developer's answer
- **THEN** after resuming it still has 30 minutes left

### Requirement: Warning threshold
When an ant reaches the warning threshold (default 80% of its budget), Anthill SHALL deliver a warning to the ant, by the means defined in ant-tools, that states the remaining time and the options to wrap up or request more time. The queen SHALL be notified at the same moment.

#### Scenario: 80% reached
- **WHEN** an ant with a 60-minute budget has run for 48 minutes
- **THEN** the ant receives "12 minutes remaining: wrap up and open a PR, or request more time"
- **AND** the queen console shows the ant as near its limit

### Requirement: Remaining time is queryable
A running ant SHALL be able to ask Anthill for its remaining time at any moment through the `time_remaining` tool.

#### Scenario: Ant checks its budget
- **WHEN** an ant queries its remaining time after 20 of 60 minutes
- **THEN** it is told 40 minutes remain

### Requirement: Extension requests
A running ant SHALL be able to request an extension through the `request_extension` tool, stating the extra minutes needed and a justification. A request within the repository's auto-approve allowance SHALL be approved immediately. Otherwise the ant SHALL be `awaiting-extension` until the queen approves, reduces or denies it. The tool's wait and `pending` result are defined in ant-tools.

#### Scenario: Auto-approved
- **WHEN** auto-approve allows up to 15 extra minutes once and the ant requests 10
- **THEN** the budget grows by 10 minutes, the ant continues without pausing, and the extension is logged

#### Scenario: Queen approves a smaller extension
- **WHEN** an ant requests 45 minutes and the queen approves 20
- **THEN** the budget grows by 20 minutes and the ant resumes with that answer

#### Scenario: Queen denies
- **WHEN** the queen denies the request
- **THEN** the ant resumes with the instruction to wrap up within the remaining time

### Requirement: Maximum total budget
The total budget of one run, extensions included, SHALL NOT exceed the configured maximum (default 3× the base budget). Requests beyond it SHALL be rejected automatically.

#### Scenario: Ceiling reached
- **WHEN** an ant with a 60-minute base has already received 120 extra minutes and requests 10 more
- **THEN** the request is rejected with "maximum budget reached"

### Requirement: Budget exhaustion
When the budget is exhausted, Anthill SHALL tell the ant to stop and give it a grace period (default 3 minutes) to commit work in progress. Anthill SHALL then stop the runner, push the branch, open a draft PR whose body holds the progress summary and the open points, comment on the ticket with a link to it, and set the ant to `timed-out`.

#### Scenario: Time runs out
- **WHEN** an ant's budget reaches zero and the grace period ends
- **THEN** the agent process is stopped, the branch with the WIP commits is pushed, a draft PR with the progress summary is opened, the ticket gets a comment linking to it, the ant is `timed-out`, and the claim is parked

#### Scenario: Time runs out with no commits
- **WHEN** an ant's budget is exhausted and the branch has no commits
- **THEN** no PR is opened, and only the progress comment is posted on the ticket

#### Scenario: Queen continues a timed-out ant
- **WHEN** the queen chooses to continue a timed-out ant with a new budget of 30 minutes
- **THEN** the session is resumed in the same worktree with a 30-minute budget, and its commits are pushed to the existing draft PR
