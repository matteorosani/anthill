## Purpose

Defines how the developer (the ant queen) operates Anthill. A long-running local daemon does the polling and supervision, and a CLI talks to it to review proposals, accept or decline work, answer ants and control them. A GUI can be added later on the same interface.

## ADDED Requirements

### Requirement: Daemon runs the loops
A local daemon SHALL run polling, eligibility evaluation, claim renewal, ant supervision and the PR shepherd. These SHALL continue when no CLI or GUI is attached. Only one daemon per user and machine SHALL run at a time.

#### Scenario: Second daemon start
- **WHEN** the user starts the daemon while one is already running
- **THEN** the second process exits with "daemon already running (pid 4242)"

### Requirement: Local control interface
The daemon SHALL expose a local-only control interface that is not reachable from other machines. The CLI and any future GUI SHALL use it, and it SHALL offer every console operation defined in this specification.

#### Scenario: Remote connection attempt
- **WHEN** another host on the network tries to connect to the control interface
- **THEN** the connection is refused

### Requirement: Viewing proposals
The CLI SHALL list current proposals with item reference, title, repository, type, priority, age and the runner that would be used. On request, it SHALL show for a given item the full prerequisite evaluation, met and unmet.

#### Scenario: Why is an item not proposed
- **WHEN** the queen asks why #12 is not in the list
- **THEN** the CLI prints each prerequisite with its status and reason, for example "dependencies-satisfied: unmet (#10 open)"

### Requirement: Accepting and declining
The queen SHALL be able to accept a proposal, optionally overriding the runner, the time budget and the base branch. The queen SHALL also be able to decline a proposal (hiding it on this machine only) or snooze it for a duration.

#### Scenario: Accept with a longer budget
- **WHEN** the queen accepts #12 with a 90-minute budget
- **THEN** the ant for #12 is created with a 90-minute budget, and its first run starts

#### Scenario: Decline is local
- **WHEN** the queen declines #12
- **THEN** it is hidden on this machine, and it stays proposed on colleagues' machines

### Requirement: Monitoring ants
The CLI SHALL list ants with item, state, runner, elapsed and remaining budget, and branch. It SHALL also stream the activity log of an ant's active run on request.

#### Scenario: Follow an ant
- **WHEN** the queen follows the ant for #12
- **THEN** the agent's activity is streamed until the queen detaches, and the ant is not affected by detaching

### Requirement: Controlling ants
The queen SHALL be able, from the console, to: list and answer pending questions, list and approve, reduce or deny open extension requests, stop or abandon an ant, retry a failed or stopped ant, continue a timed-out ant with a new budget, accept or decline a follow-up proposal, un-hide a declined item, and clean up a worktree.

#### Scenario: Answer from the console
- **WHEN** the queen answers in the CLI a question that the ant asked on its PR
- **THEN** the answer is posted as a reply where the question was asked, attributed to the queen, and a new run resumes the ant's session

### Requirement: Notifications
The daemon SHALL raise a local desktop notification for: a new proposal, an ant question, a stale question, an extension request, an ant at its warning threshold, an ant succeeded, failed, timed out or done with no change, a follow-up started or proposed, a follow-up limit reached, a change stored for a waiting ant, a lost claim, and provider polling failures. Each kind SHALL be possible to turn off.

#### Scenario: Extension request
- **WHEN** an ant requests an extension that is not auto-approved
- **THEN** a desktop notification appears with the ant's justification and requested minutes

### Requirement: Usage reporting
The CLI SHALL show recorded usage (duration, tokens, cost) per ant run, per work item, and per day, totalled over runs and shepherd rounds. Unknown values SHALL be shown as unknown and marked as missing from the totals. No usage limit SHALL be enforced.

#### Scenario: Daily usage
- **WHEN** the queen asks for today's usage
- **THEN** the CLI lists each run and round with its tokens and cost, and shows a total that states how many runs had unknown cost

### Requirement: Health and status
The CLI SHALL report daemon health: uptime, each provider's last successful poll and error, rate limit state, running and queued ants, held claims, and the shepherd's active rounds.

#### Scenario: Status overview
- **WHEN** the queen runs the status command
- **THEN** the output includes, for each provider, the time of its last successful poll and, for each held claim, its item and lease expiry
