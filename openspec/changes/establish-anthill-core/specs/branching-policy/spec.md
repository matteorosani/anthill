## Purpose

Defines the git rules Anthill and its ants follow: where worktrees live, how branches are named and based, what may be pushed where, and when history may be rewritten. These rules keep automated work isolated and keep human branches safe.

## ADDED Requirements

### Requirement: One branch and worktree per work item
Each work item SHALL have exactly one ant branch and one git worktree on the claiming machine. Every run of the item's ant SHALL reuse them.

#### Scenario: Second run for the same item
- **WHEN** a follow-up run for #12 starts after a ticket update
- **THEN** it works in the existing worktree on the existing branch, and no new branch is created

### Requirement: Branch naming
Ant branches SHALL be named `<prefix>/<provider>-<id>-<slug>`. The prefix defaults to `ant`, the provider is `gh` or `ado`, and the slug is the title lowercased, made ASCII-safe, hyphenated and truncated to 40 characters. The prefix SHALL be configurable per repository.

#### Scenario: GitHub issue branch
- **WHEN** issue #12 titled "Fix login timeout on Safari" is started
- **THEN** the branch is `ant/gh-12-fix-login-timeout-on-safari`

#### Scenario: Name already taken remotely by someone else
- **WHEN** the branch name exists on the remote but its commits do not carry this instance's trailer for this item, for example a branch left by an instance whose claim expired
- **THEN** a numeric suffix is added (`…-2`) and the existing branch is left untouched

### Requirement: Base branch resolution
The base branch SHALL be resolved in this order: a `base:<branch>` label or tag on the item, then the repository's configured base branch, then the remote's default branch. The ant branch SHALL be created from the latest fetched commit of the base branch.

#### Scenario: Ticket targets a release branch
- **WHEN** an item has the label `base:release/2.4`
- **THEN** the ant branch is created from `origin/release/2.4`, and the PR targets `release/2.4`

#### Scenario: Base branch missing
- **WHEN** the resolved base branch does not exist on the remote
- **THEN** the ant does not start, and the queen sees "base branch release/2.4 not found"

### Requirement: Dependencies must be merged
Ant branches SHALL NOT be stacked on other unmerged ant branches. A dependency counts as satisfied only when its work is closed as completed, which in practice means its PR is merged into the base branch.

#### Scenario: Dependency PR still open
- **WHEN** #12 depends on #10, and #10's PR is open but not merged, so #10 is still open
- **THEN** #12 is not eligible

### Requirement: Worktree location
Worktrees SHALL be created under a configurable Anthill worktree root (default `<anthill-home>/worktrees/<repo>/<branch-name>`), outside the developer's own working copy. Anthill SHALL never modify the developer's main working copy, its checked-out branch or its uncommitted changes.

#### Scenario: Developer has local changes
- **WHEN** the developer's clone has uncommitted changes on `main`
- **THEN** starting an ant leaves that working copy exactly as it was

### Requirement: Push restrictions
All pushes SHALL be executed by Anthill, either through the `push` tool (ant-tools) or after a verified shepherd auto-fix (pr-shepherd). Anthill SHALL push only to branches it created for a work item, and to a human PR's branch only when that PR has opted in to shepherd auto-fix (pr-shepherd). It SHALL never push to the base branch, the default branch, or any branch matching the repository's protected patterns.

#### Scenario: Ant attempts to push to main
- **WHEN** an ant checks out `main` in its worktree and calls `push`
- **THEN** the `push` tool refuses with "push to main is not allowed", and nothing is pushed

### Requirement: History rewriting
Before its branch is first pushed, Anthill SHALL allow an ant to rebase or amend its own branch. After the first push, Anthill and its ants SHALL NOT rewrite published history. They SHALL integrate base-branch changes by merge and SHALL never force-push.

#### Scenario: Base moved while PR is open
- **WHEN** the base branch gets new commits that conflict with an open ant PR
- **THEN** the conflict is resolved with a merge commit on the ant branch, not a rebase plus force-push

#### Scenario: Amend after the first push
- **WHEN** an ant amends a commit it has already pushed and calls `push`
- **THEN** the push is rejected as non-fast-forward, and the ant is told to keep the published history and add a new commit

### Requirement: Commit identity and traceability
Ant commits SHALL use the developer's configured git identity, with trailers naming the item, the runner and the instance, so automated commits and their owner are identifiable.

#### Scenario: Commit trailer
- **WHEN** an ant commits on `ant/gh-12-…`
- **THEN** the commit message ends with `Anthill-Item: gh-12`, `Anthill-Agent: claude-code` and `Anthill-Instance: mrosani@LAPTOP-42`

### Requirement: Worktree cleanup
After an ant PR is merged or closed, or its ant is abandoned and the queen confirms cleanup, Anthill SHALL remove the worktree. It SHALL delete the local branch, and the remote branch only if the provider has not already deleted it and the PR is merged. Worktrees with unpushed commits SHALL NOT be removed without explicit confirmation.

#### Scenario: PR merged
- **WHEN** the PR for #12 is merged
- **THEN** the worktree and local branch for #12 are removed at the next poll

#### Scenario: Unpushed work
- **WHEN** cleanup is requested for a worktree with unpushed commits
- **THEN** Anthill asks the queen to confirm before deleting it
