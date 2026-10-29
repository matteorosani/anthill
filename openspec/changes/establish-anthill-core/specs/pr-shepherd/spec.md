## Purpose

Defines the PR Shepherd: the part of Anthill that watches open pull requests, reviews them with headless agents in a limited number of rounds, posts findings as PR comments, and fixes simple problems automatically where allowed. Merging is always left to humans.

## ADDED Requirements

### Requirement: Shepherd scope
The shepherd SHALL review open, non-draft PRs in configured repositories that match the repository's shepherd scope: `ant-authored` (default), `labelled` (PRs with the `anthill:review` label), or `all`. PRs outside the scope SHALL only be reviewed when a developer explicitly requests it.

#### Scenario: Human PR in ant-authored scope
- **WHEN** a developer opens a PR without the review label in a repository with scope `ant-authored`
- **THEN** the shepherd does not review it unless someone comments `/anthill review`

#### Scenario: Draft PR
- **WHEN** an in-scope PR is a draft
- **THEN** it is not reviewed until it is marked ready for review

### Requirement: Shepherd participation
Every instance SHALL take part in shepherd reviews by default, competing for review claims. A developer SHALL be able to turn reviewing off for their machine, or only for specific repositories, in local configuration. An instance that has reviewing off SHALL never take a review claim. Its own ant PRs are still reviewed by other instances.

#### Scenario: Machine opted out
- **WHEN** an instance has reviewing turned off and an in-scope PR appears
- **THEN** it does not claim or review the PR, and another participating instance does

#### Scenario: Opted out for one repository
- **WHEN** an instance has reviewing turned off only for `contoso/api`
- **THEN** it does not review PRs in `contoso/api`, and still reviews in-scope PRs in other repositories

### Requirement: Review runner
Review rounds and auto-fixes SHALL use the repository's `review-runner`, falling back to the repository's ant runner when none is set. An instance that does not have the review runner installed SHALL NOT take the review claim.

#### Scenario: Different runner for reviews
- **WHEN** a repository's ant runner is Claude Code and its `review-runner` is Codex
- **THEN** review rounds on its PRs run with Codex

#### Scenario: Review runner missing on this machine
- **WHEN** the review runner is not installed on an instance
- **THEN** that instance does not claim reviews for the repository, and another instance that has it does

### Requirement: Review rounds
A review round SHALL be one agent review of the PR's current head commit. It produces a summary comment and zero or more findings. Each round SHALL hold a `review` claim on the PR for its duration, so only one instance reviews at a time.

Every review round SHALL run in a new agent session, never resuming an earlier round's session, so that each review is independent of the previous one.

#### Scenario: Second round starts fresh
- **WHEN** round 2 starts on a PR
- **THEN** the review agent starts a new session, and receives the open findings from round 1 only as listed data

#### Scenario: Round on a new PR
- **WHEN** an in-scope PR is first seen
- **THEN** one instance claims it, runs round 1 against the head commit, posts the results, and releases the review claim

### Requirement: Round triggers
A new round SHALL start when the PR first enters scope, or when new commits are pushed after the last reviewed commit. In both cases rounds must remain. A round SHALL NOT start for a head commit that has already been reviewed.

#### Scenario: Developer pushes fixes
- **WHEN** a developer pushes commits after round 1 and the limit is 3
- **THEN** round 2 starts against the new head commit

#### Scenario: No new commits
- **WHEN** a PR was reviewed and nothing was pushed since
- **THEN** no further round starts automatically

### Requirement: Round limit
The number of automatic rounds per PR SHALL be limited by repository configuration (default 3). When the limit is reached, the shepherd SHALL post a comment saying so and how to request another round.

#### Scenario: Limit reached
- **WHEN** round 3 of 3 finishes and the developer pushes again
- **THEN** no round starts, and the PR has a comment explaining that `/anthill review` requests another round

### Requirement: Developer-requested rounds
A developer SHALL be able to request an extra round with `/anthill review` on the PR. Such a request SHALL start a round even when the limit is reached or the PR is out of scope. Requested rounds SHALL be counted separately from automatic rounds.

#### Scenario: Extra round after limit
- **WHEN** a developer comments `/anthill review` on a PR that used all automatic rounds
- **THEN** a round runs against the current head commit and is recorded as requested

### Requirement: Findings are posted as PR comments
Review agents SHALL report findings through `submit_review` (ant-tools). Each finding SHALL be posted as an inline review comment on the relevant lines when the location is known, otherwise in the round's summary comment. Each finding SHALL have a severity (`blocking`, `suggestion`, `nit`) and a classification (`auto-fixable` or `needs-developer`). The shepherd SHALL NOT approve or merge PRs.

#### Scenario: Blocking finding
- **WHEN** a round finds a null dereference at `src/auth.ts:42`
- **THEN** an inline comment on line 42 states the problem with severity `blocking`

#### Scenario: Findings handed to the owning ant
- **WHEN** a round on an ant-authored PR produces `blocking` or `suggestion` findings classified `needs-developer`
- **THEN** the round summary lists them as handed to the owning ant (change-reaction), and lists `nit` findings as left to developers

#### Scenario: Clean round
- **WHEN** a round finds nothing
- **THEN** the summary comment says no issues were found, and the PR is neither approved nor merged

### Requirement: Findings are not repeated
A round SHALL NOT post a finding that matches an earlier, still unresolved finding on the same PR. It SHALL instead reference the existing comment thread.

#### Scenario: Unchanged problem in round 2
- **WHEN** the problem from round 1 is still present in round 2
- **THEN** no new inline comment is posted, and the round 2 summary links to the existing thread as still open

### Requirement: Automatic fixes
The shepherd SHALL try to fix `auto-fixable` findings on ant PRs when the repository's `auto-fix` is enabled (default on). On human PRs it SHALL do so only after the PR opts in, by an `anthill:autofix` label or `/anthill fix` from the PR author or a write-access user. Each fix is pushed as a separate commit with a reply on its thread, and SHALL NOT trigger another automatic round.

#### Scenario: Auto-fix on an ant PR
- **WHEN** a round on an ant-authored PR finds an unused import classified `auto-fixable`
- **THEN** a commit removing it is pushed to the PR branch, the thread is answered with the commit link and resolved, and no new round starts because of that commit

#### Scenario: Human PR not opted in
- **WHEN** the same finding appears on a human PR without the `anthill:autofix` label or a `/anthill fix` request
- **THEN** the finding is posted with a suggested change and nothing is pushed

#### Scenario: Human PR opted in
- **WHEN** the PR author adds the `anthill:autofix` label to their PR
- **THEN** `auto-fixable` findings on that PR are fixed and pushed like on an ant PR

#### Scenario: Fork branch not pushable
- **WHEN** an opted-in human PR comes from a fork that does not allow maintainer edits
- **THEN** the findings are posted as suggested changes and nothing is pushed

### Requirement: Failed auto-fix falls back to developer
If an auto-fix does not apply cleanly or breaks the repository's configured verification command, the shepherd SHALL discard the fix, reclassify the finding as `needs-developer`, and say so on the thread.

#### Scenario: Fix breaks the build
- **WHEN** the auto-fix commit makes the verification command fail
- **THEN** nothing is pushed, and the thread says the automatic fix failed and needs a developer

### Requirement: PR closure is human
The shepherd SHALL stop all activity on a PR once it is merged or closed. It SHALL release any claims linked to it, and SHALL then trigger worktree cleanup as defined by branching-policy.

#### Scenario: PR merged by developer
- **WHEN** a developer merges an ant PR
- **THEN** the shepherd records the PR as done, releases the parked task claim on the ticket, and cleans up the worktree
