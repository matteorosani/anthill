## Purpose

Defines how Anthill, its ants and developers talk to each other through comments on tickets and pull requests. Covers how Anthill comments are marked, how ants ask questions and receive answers, and the `/anthill` command set developers use to steer it.

## ADDED Requirements

### Requirement: Anthill comments are marked
Every comment Anthill posts SHALL begin with a visible 🐜 prefix naming the instance and the ant or shepherd role. It SHALL contain a hidden machine-readable marker with the instance id, the comment kind (`claim`, `question`, `status`, `summary`, `finding`, `reply`) and the related item or PR.

#### Scenario: Status comment
- **WHEN** Anthill posts a progress summary on #12
- **THEN** the comment starts with "🐜 Ant (mrosani@LAPTOP-42)" and contains a hidden marker of kind `status` for `gh-12`

### Requirement: Ants ask questions through the ticket
When an ant needs information it cannot find, it SHALL ask through the `ask_question` tool (ant-tools), which posts a `question` comment on the ticket, or on the PR for follow-up runs. All other ant-written comments SHALL also go through the ant tools. Anthill SHALL then stop the runner with the session saved and move the ant to `awaiting-input`, and the queen SHALL be notified.

#### Scenario: Ambiguous requirement
- **WHEN** an ant cannot tell whether "timeout" means 30 or 60 seconds
- **THEN** a question comment is posted on #12, the ant is `awaiting-input`, and its time budget is paused

### Requirement: Answers resume the session
A non-Anthill comment that replies to a pending question SHALL be treated as its answer. On a threaded provider, a reply in the thread counts. Otherwise, the first human comment after the question that quotes it or mentions `@anthill` counts. Anthill SHALL then start a run that resumes the ant's session with the answer, and the question SHALL be marked answered.

#### Scenario: Developer replies in thread
- **WHEN** a developer replies "60 seconds" in the PR thread of an ant's question
- **THEN** a new run resumes the ant's session with "60 seconds" as input

#### Scenario: Unrelated comment
- **WHEN** a developer posts an unrelated comment on the ticket without quoting the question or mentioning `@anthill`
- **THEN** the question stays pending

### Requirement: Question timeout
A question not answered within the configured time (default 3 days) SHALL be reported to the queen as stale. No run SHALL start on its own.

#### Scenario: No answer for three days
- **WHEN** a question has been pending for 3 days
- **THEN** the queen console flags it as stale, and the queen can abandon the ant or answer it from the console

### Requirement: Command set
Anthill SHALL recognise these commands in human comments on tickets and PRs, each on its own line:
- `/anthill review`: request a shepherd round
- `/anthill fix`: fix the referenced thread
- `/anthill stop`: stop the ant's active run, keeping its claim
- `/anthill release`: force-release claims
- `/anthill retry`: retry a failed or stopped ant
- `/anthill help`: list commands

Unknown commands SHALL be answered with the help text.

#### Scenario: Fix on an ant PR
- **WHEN** a developer comments `/anthill fix` on a thread of an ant PR
- **THEN** the owning ant starts a follow-up run for that thread

#### Scenario: Fix on a human PR
- **WHEN** a developer comments `/anthill fix` on a finding thread of a human PR
- **THEN** the PR is opted in to shepherd auto-fix for that thread

#### Scenario: Unknown command
- **WHEN** a developer comments `/anthill deploy`
- **THEN** Anthill replies with the list of valid commands

#### Scenario: Stop command
- **WHEN** a developer comments `/anthill stop` on #12 while its ant has an active run
- **THEN** the ant is `stopped` with its claim parked, and Anthill replies confirming the stop and how to retry

### Requirement: Commands are authorized
Commands SHALL be executed only when the commenter has write access to the repository. The one exception is `/anthill fix` on a PR, which the PR's author SHALL also be allowed to use. Commands from other users SHALL be ignored, with a reply stating that permission is missing.

#### Scenario: External contributor
- **WHEN** a user without write access comments `/anthill review`
- **THEN** no round starts, and Anthill replies that the command requires write access

#### Scenario: PR author opts in
- **WHEN** the author of a human PR, without write access, comments `/anthill fix` on a finding thread of their PR
- **THEN** the shepherd tries to fix that finding on the PR branch

### Requirement: Each command is executed once
A command comment SHALL be executed at most once across all instances. Anthill acknowledges it with a `reply` comment, and only the instance responsible for the item, as defined by change-reaction, executes it.

#### Scenario: Two instances see the same command
- **WHEN** two instances poll the same PR with an unacknowledged `/anthill review`
- **THEN** exactly one round runs and exactly one acknowledgement is posted

### Requirement: Comment content safety
Anthill comments SHALL NOT include secrets, environment variables, absolute local paths outside the worktree, or raw agent transcripts. Summaries SHALL be limited to the configured length (default 4,000 characters), and longer content SHALL be truncated with a note.

#### Scenario: Agent output contains a token
- **WHEN** an ant's final message contains a string matching a secret pattern (anthill-configuration)
- **THEN** the posted summary has the value redacted
