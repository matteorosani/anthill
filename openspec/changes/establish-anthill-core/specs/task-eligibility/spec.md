## Purpose

Defines precisely when a work item is eligible to be worked on by an ant. Prerequisites are named, independently evaluable checks, and every unmet one is reported, so the queen always knows why an item is or is not proposed.

## ADDED Requirements

### Requirement: Eligibility is the conjunction of named prerequisites
A work item SHALL be eligible only if every prerequisite in this specification holds. Each prerequisite SHALL have a stable identifier, and evaluation SHALL return the full list of unmet prerequisites, not only the first.

#### Scenario: Two prerequisites unmet
- **WHEN** an item is claimed by another machine and also has an open blocking dependency
- **THEN** evaluation reports it as not eligible with both `not-claimed` and `dependencies-satisfied` listed as unmet

### Requirement: Prerequisite opted-in
The item SHALL carry the configured opt-in label (`opted-in`).

#### Scenario: Opt-in label removed
- **WHEN** the `anthill` label is removed from a proposed item
- **THEN** the item leaves the proposal list at the next evaluation

### Requirement: Prerequisite open
The item's state SHALL be open (`open`).

#### Scenario: Closed item
- **WHEN** an item is closed as completed
- **THEN** it is not eligible, with `open` unmet

### Requirement: Prerequisite dependencies satisfied
Every dependency of the item SHALL be closed as completed (`dependencies-satisfied`). Dependencies come only from provider-native links: GitHub "blocked by" relations and Azure DevOps Predecessor links. Text in the body or comments SHALL NOT create dependencies. A dependency closed as not planned SHALL make the item blocked, not satisfied.

#### Scenario: Dependency still open
- **WHEN** issue #12 depends on #10 and #10 is open
- **THEN** #12 is not eligible, and the reason names #10

#### Scenario: Dependency abandoned
- **WHEN** #10 is closed as not planned
- **THEN** #12 is not eligible, with reason "dependency #10 closed as not planned — needs queen review"

#### Scenario: Dependency Anthill cannot read
- **WHEN** a native dependency link points to an item Anthill has no access to
- **THEN** the dependency counts as unsatisfied, with reason "dependency not resolvable"

#### Scenario: Dependency written in the body
- **WHEN** an issue body says "Depends on #10" but no native "blocked by" link exists
- **THEN** #10 is not treated as a dependency

#### Scenario: Cross-provider dependency
- **WHEN** an Azure DevOps item should depend on a GitHub issue
- **THEN** this cannot be expressed, because native links do not cross providers, and Anthill does not support it

### Requirement: Prerequisite leaf item
The item SHALL have no open child items (`is-leaf`): GitHub sub-issues or Azure DevOps Child links. Parent items are containers and SHALL NOT be proposed while any child is open. Parent/child links SHALL NOT count as dependencies.

#### Scenario: User story with open tasks
- **WHEN** an Azure DevOps User Story with the `anthill` tag has two open child Tasks
- **THEN** the story is not eligible, with `is-leaf` unmet, and the reason names the open children

#### Scenario: Labelled parent does not opt in children
- **WHEN** a GitHub issue with the `anthill` label has sub-issues without the label
- **THEN** the sub-issues are not considered by Anthill

### Requirement: Prerequisite not claimed
The item SHALL have no live claim held by another Anthill instance (`not-claimed`), as defined by the work-claiming capability. An expired claim SHALL count as no claim.

#### Scenario: Claimed by a colleague
- **WHEN** another developer's Anthill holds a live claim on the item
- **THEN** the item is not eligible on this machine, and the reason names the claimant

### Requirement: Prerequisite repository available locally
The item SHALL map to a repository that is configured and cloned on this machine (`repository-available`).

#### Scenario: Repository not cloned
- **WHEN** an item maps to repository `contoso/api` and there is no local clone configured
- **THEN** the item is not eligible, with reason "repository contoso/api not available on this machine"

### Requirement: Prerequisite no active work
The item SHALL have no open ant PR and no local ant in any state other than `done` or `abandoned` (`no-active-work`). Follow-up work on such an item goes through change-reaction, not through a new proposal.

#### Scenario: PR already open
- **WHEN** an ant already opened a PR for the item and the PR is still open
- **THEN** the item is not proposed again

#### Scenario: Failed ant awaiting retry
- **WHEN** the item's ant on this machine is `failed` and the queen has not yet retried or abandoned it
- **THEN** the item is not proposed, with `no-active-work` unmet

### Requirement: Prerequisite no pending question
The item SHALL have no unanswered question from an ant (`no-pending-question`), as defined by agent-conversation.

#### Scenario: Waiting for an answer
- **WHEN** an earlier ant asked a question on the ticket and no developer has answered
- **THEN** the item is not eligible, and the reason links to the question comment

### Requirement: Prerequisite readiness content
The item SHALL have a non-empty description (`has-description`). When a repository enables `require-acceptance-criteria`, the description SHALL also contain an acceptance criteria section (Azure DevOps: the Acceptance Criteria field is non-empty).

#### Scenario: Empty description
- **WHEN** an opted-in issue has only a title
- **THEN** it is not eligible, with `has-description` unmet

### Requirement: Prerequisite not excluded
The item SHALL NOT carry the hold label (`not-excluded`, default `anthill:hold`).

#### Scenario: Put on hold
- **WHEN** a developer adds `anthill:hold` to an eligible item
- **THEN** it leaves the proposal list

### Requirement: Eligibility is separate from capacity
Local capacity (the maximum number of active runs, default 2) and the availability of an agent runner SHALL NOT affect eligibility. They SHALL only gate starting an ant: eligible items stay proposed while capacity is full.

#### Scenario: Capacity full
- **WHEN** two ants have active runs and the capacity is 2
- **THEN** eligible items are still listed, marked "waiting for capacity", and accepting one queues it

### Requirement: Proposal list
Anthill SHALL keep a proposal list of eligible items for the queen, ordered by priority (provider priority field, then a configured priority label, then age), and SHALL re-evaluate eligibility after every poll. Items declined by the queen SHALL be hidden until they change or the queen un-hides them.

#### Scenario: Declined item changes
- **WHEN** the queen declined an item and later its description is edited
- **THEN** the item is proposed again if it is still eligible

### Requirement: Re-check at acceptance
Eligibility SHALL be re-evaluated against fresh provider data when the queen accepts an item. If the item is no longer eligible, acceptance SHALL fail with the unmet prerequisites.

#### Scenario: Became ineligible in the meantime
- **WHEN** the queen accepts an item that was closed a minute earlier
- **THEN** no ant is started, and the queen sees "no longer eligible: open"
