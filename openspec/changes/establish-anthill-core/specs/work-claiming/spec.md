## Purpose

Defines how several Anthill instances, running on different developer machines against the same trackers, agree that only one of them works on a given ticket or reviews a given PR. The protocol uses claims with leases stored on the provider itself.

## ADDED Requirements

### Requirement: Instance identity
Each Anthill instance SHALL have a stable instance identifier made of the developer's provider identity and a machine id (for example `mrosani@LAPTOP-42`). All claims and Anthill comments SHALL carry it.

#### Scenario: Same developer, two machines
- **WHEN** one developer runs Anthill on a laptop and a desktop
- **THEN** the two instances have different identifiers and compete for claims like any two instances

### Requirement: Claims are stored on the provider
A claim SHALL be recorded on the provider. It consists of a claim comment with a machine-readable Anthill marker (instance id, claim kind, lease duration), an `anthill:claimed` label or tag, and, for tickets, assignment to the claiming developer. No shared server or database SHALL be required.

#### Scenario: Claim visible to humans
- **WHEN** an instance claims issue #12
- **THEN** the issue shows a comment such as "🐜 Claimed by mrosani@LAPTOP-42 until 11:30 UTC", the `anthill:claimed` label, and the developer as assignee

#### Scenario: Forged claim marker
- **WHEN** a user without write access to the repository posts a comment containing a copied claim marker
- **THEN** the comment is not treated as a claim

### Requirement: Claim acquisition resolves races deterministically
An instance SHALL acquire a claim in this order: check that there is no live claim, post its claim comment, then re-read the item's claim comments. The earliest live claim comment by provider creation order (comment id as tie-breaker) wins. Only the winner SHALL then add the claim label and, for tickets, the assignee. A losing instance SHALL retract its comment, touch nothing else, and treat the claim as failed.

#### Scenario: Two instances claim simultaneously
- **WHEN** instances A and B both post claim comments on #12, and A's comment was created first
- **THEN** A holds the claim and adds the label and assignee, B retracts its comment without touching the label, and B's queen is told "claimed by A"

#### Scenario: Azure DevOps optimistic concurrency
- **WHEN** a claim is written to an Azure DevOps work item
- **THEN** the claim tag and assignment update are sent with a revision precondition, and a revision conflict counts as a lost race

### Requirement: Claim kinds
Claims SHALL have a kind. `task` is a claim on a ticket for implementation. `review` is a claim on a PR for one shepherd review or fix activity. A ticket's `task` claim and its PR's `review` claim are independent.

#### Scenario: Different machine reviews the PR
- **WHEN** instance A's ant opened a PR for #12
- **THEN** instance B may claim that PR for review while A still holds the task claim on #12

### Requirement: Leases and renewal
Every claim SHALL have a lease (default 15 minutes). A lease SHALL expire at the provider's last-update timestamp of the claim comment plus the lease duration in its marker; local clocks SHALL NOT be used. The holder SHALL renew it while work is active by editing its claim comment. A claim whose lease has expired SHALL count as released.

#### Scenario: Machine goes offline
- **WHEN** instance A's laptop sleeps and its claim on #12 is not renewed for longer than the lease
- **THEN** other instances treat #12 as unclaimed for eligibility

#### Scenario: Holder returns after expiry
- **WHEN** instance A wakes after its lease expired and another instance has claimed #12 meanwhile
- **THEN** A ends any active run for #12, sets its ant to `abandoned` with reason "claim lost", keeps its local worktree, and tells its queen

### Requirement: Long-lived task claims
While a ticket's ant is between runs (`awaiting-input`, `stopped`, `failed`, `timed-out`, or `succeeded` with its PR open), the task claim SHALL stay held without active lease renewal. It is marked `parked` in the claim comment, and other instances SHALL treat a parked claim as live.

#### Scenario: Ant finished, PR open
- **WHEN** an ant's run opened a PR and called `finish`
- **THEN** the claim on the ticket becomes parked, and the ticket is not proposed to other queens

### Requirement: No claim transfer
A task claim SHALL never be taken over by another instance, however long its holder has been inactive. Ant PRs stay tied to the instance that created them. While that instance is offline, no other instance SHALL react to changes on the ticket or its ant PR on its behalf.

#### Scenario: Owner offline with an open ant PR
- **WHEN** instance A holds a parked claim on #12, A has been offline for a week, and a developer comments on the ant PR
- **THEN** no other instance starts a follow-up run, and developers handle the PR by hand

#### Scenario: Release does not hand over the PR
- **WHEN** a developer runs `/anthill release` on #12 while its ant PR is still open
- **THEN** the claim is released, and #12 is still not proposed, because `no-active-work` is unmet while the PR is open

### Requirement: Release
An instance SHALL release a claim when its work ends: the ticket is closed, the PR is merged or closed, the queen abandons the ant, the ant finishes with `no-change`, or a review activity finishes. Releasing removes the claim label and marks the claim comment as released. It SHALL unassign the developer only if Anthill made that assignment.

#### Scenario: Queen abandons an ant
- **WHEN** the queen abandons the ant working on #12
- **THEN** the claim on #12 is released and #12 can be proposed to other queens again

### Requirement: Manual override
A developer SHALL be able to force-release any claim with the `/anthill release` command or by removing the `anthill:claimed` label. The holder SHALL notice at its next poll and stop the affected work.

#### Scenario: Stuck claim released by a colleague
- **WHEN** a developer comments `/anthill release` on #12, which a machine that has been offline for days still holds as parked
- **THEN** all instances treat #12 as unclaimed from the next poll
