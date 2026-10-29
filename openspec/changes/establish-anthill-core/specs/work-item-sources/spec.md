## Purpose

Defines how Anthill discovers tickets and pull requests from external providers (GitHub, Azure DevOps). Covers mapping them to one provider-independent model and detecting what changed between polls.

## ADDED Requirements

### Requirement: Supported providers
Anthill SHALL support GitHub (Issues and Pull Requests) and Azure DevOps (Work Items and Pull Requests) as providers. Each configured provider connection SHALL be polled independently, and the failure of one provider SHALL NOT stop polling of the others.

#### Scenario: One provider unavailable
- **WHEN** the Azure DevOps API returns errors during a poll cycle while GitHub responds normally
- **THEN** GitHub items are still ingested and updated in that cycle
- **AND** the Azure DevOps failure is recorded and shown in the queen console with the time of the last successful poll

### Requirement: Opt-in marking of tickets
A ticket SHALL be considered by Anthill only if it carries the configured opt-in label: a label on GitHub, a tag on Azure DevOps (default `anthill`). Tickets without it SHALL be ignored for proposals.

#### Scenario: Ticket without opt-in label
- **WHEN** a GitHub issue in a configured repository has no `anthill` label
- **THEN** the issue does not appear in the proposal list and no ant is ever started for it

#### Scenario: Opt-in label added later
- **WHEN** the `anthill` label is added to an existing issue
- **THEN** the issue is ingested at the next poll and evaluated for eligibility

### Requirement: Normalized work item model
Every ticket SHALL be mapped to a normalized work item. It SHALL hold: provider, stable identifier, web URL, repository, title, body, type (task/bug/feature/other), state (open/closed-completed/closed-not-planned), labels, assignees, dependency links, parent and child links, comments, and a provider revision or last-updated timestamp. Dependency and hierarchy links SHALL come only from provider-native links.

#### Scenario: Azure DevOps bug mapped
- **WHEN** an Azure DevOps Bug with the `anthill` tag is ingested
- **THEN** it is represented with type `bug`, its predecessor links as dependency links, and its `System.Rev` as the revision

#### Scenario: GitHub issue with dependencies and sub-issues
- **WHEN** a GitHub issue is "blocked by" #10 and has sub-issues #20 and #21
- **THEN** its dependency links contain #10, and its child links contain #20 and #21

#### Scenario: GitHub issue closed as not planned
- **WHEN** a GitHub issue is closed with reason "not planned"
- **THEN** its normalized state is `closed-not-planned`, distinct from `closed-completed`

### Requirement: Repository mapping
Every work item SHALL be resolved to exactly one target code repository. A GitHub issue maps to its own repository unless a repository override is set. An Azure DevOps work item maps through per-project configuration or an explicit `repo:<name>` tag. An item that cannot be resolved SHALL be reported as unmapped.

#### Scenario: Azure DevOps item with no repository
- **WHEN** an Azure DevOps work item has the `anthill` tag but its project has several repositories, no default, and no `repo:` tag
- **THEN** the item is shown as unmapped with the reason "cannot determine target repository" and is not eligible

### Requirement: Pull request ingestion
Anthill SHALL poll open pull requests in configured repositories. Each PR SHALL be mapped to a normalized model with: identifier, URL, source and target branches, author, head commit, draft flag, labels, review threads and comments, and a link to the originating work item when it can be detected.

#### Scenario: Ant PR linked to its ticket
- **WHEN** a PR's body contains the ticket reference that Anthill wrote when opening it
- **THEN** the normalized PR is linked to that work item and marked as an ant PR

### Requirement: Polling cadence
Polling SHALL run on a configurable interval per provider (default 2 minutes). It SHALL respect provider rate limits. When a provider reports that the rate limit is exhausted, polling of that provider SHALL wait until the reported reset time instead of failing repeatedly.

#### Scenario: Rate limit exhausted
- **WHEN** GitHub responds that the rate limit is exhausted until 10:42
- **THEN** no further GitHub requests are sent before 10:42
- **AND** the queen console shows that GitHub polling is paused for rate limiting

### Requirement: Change detection
Between two polls, Anthill SHALL detect for each work item and PR whether its content changed. Content means title, body, state, labels, assignees, dependency links, comments, review threads and head commit. Each change SHALL be emitted as a typed change event, and an unchanged item SHALL emit none.

#### Scenario: New comment on a ticket
- **WHEN** a developer adds a comment to an ingested issue between two polls
- **THEN** exactly one `comment-added` change event is emitted for that issue, carrying the comment author and body

#### Scenario: No change
- **WHEN** an item's revision and content are identical to the previous poll
- **THEN** no change event is emitted

### Requirement: Self-authored changes are distinguishable
Change events SHALL record whether the change was made by Anthill, from any machine, or by a human. Because Anthill acts under each developer's own account, this SHALL be determined only by the Anthill marker, never by the author's account.

#### Scenario: Anthill's own comment
- **WHEN** a poll finds a new comment that carries the Anthill marker
- **THEN** the emitted change event is flagged as Anthill-authored

#### Scenario: Developer comments from the same account
- **WHEN** the developer whose credentials Anthill uses types a comment by hand on an ant PR
- **THEN** the comment has no marker, and the change event is flagged as human
