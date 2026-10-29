# Anthill glossary

Every spec, design and task uses these terms with exactly one meaning. When a new concept appears, add it here before using it in a spec. Spec names in parentheses point to where the term is specified in detail.

## People

- **Developer**: any person who works on the repositories and trackers Anthill watches. Developers comment, review, push and merge.
- **Queen** (the ant queen): the developer who operates one Anthill instance from its queen console. A queen accepts or declines proposals, answers questions, decides on extension requests, and stops, retries, continues or abandons ants. Each instance has exactly one queen, and a queen controls only the ants of their own instance. Colleagues running their own instances are other queens.
- **Write-access user**: a developer with write access to the repository. Only write-access users can trigger work and run commands, with the exceptions in change-reaction and agent-conversation.
- **Ticket author** and **PR author**: the person who created the ticket or the PR.
- **Human**: anything not done by Anthill. A change is human when it carries no marker, whatever account made it.

## Anthill itself

- **Anthill**: the whole system: the daemon, the CLI and the ant tools.
- **Instance**: one Anthill daemon on one machine. Its **instance id** is the developer's provider identity plus a machine id, for example `mrosani@LAPTOP-42`. One developer with two machines runs two instances.
- **Daemon**: the long-running local process of an instance. It polls, evaluates eligibility, renews claims, supervises runs and runs the shepherd (queen-console).
- **Queen console**: the CLI, and later a GUI, that the queen uses to talk to the daemon over the **control interface**, a local-only pipe (queen-console).

## Providers and items

- **Provider**: GitHub or Azure DevOps. A provider plays two roles: **tracker** (tickets) and **code host** (repositories and PRs).
- **Item** (also **work item** or **ticket**): a GitHub issue or an Azure DevOps work item. "Task" never means an item; it appears only in the fixed terms task claim, task run and task brief.
- **Label**: a GitHub label or an Azure DevOps tag. Anthill uses these labels:
  - **opt-in label** (default `anthill`): the item may be worked on by Anthill;
  - **hold label** (default `anthill:hold`): Anthill does not start any work on the item while it is present;
  - **claim label** (default `anthill:claimed`): part of a task or review claim;
  - **auto-fix label** (`anthill:autofix`) and **review label** (`anthill:review`): opt a human PR in to shepherd auto-fix or review;
  - **per-item setting labels**: `agent:<name>`, `budget:<minutes>`, `base:<branch>`, `repo:<name>`.
- **Target repository**: the one code repository an item maps to. An item without one is **unmapped** (work-item-sources).
- **Dependency**: a provider-native "blocked by" or Predecessor link. **Parent** and **child**: provider-native sub-issue or Child links. A **leaf** item has no open children.
- **Poll**: one request cycle to a provider. Anthill learns about everything by polling.

## Proposals

- **Prerequisite**: one named check an item must pass, such as `open` or `not-claimed` (task-eligibility).
- **Eligible**: an item that passes every prerequisite.
- **Proposal**: an eligible item offered to the queen. The **proposal list** holds them all, in priority order.
- **Accept**: the queen takes a proposal. Anthill then creates an ant for it.
- **Decline**: the queen hides a proposal on their machine only, until the item changes or they un-hide it. **Snooze** hides it for a set time.
- **Follow-up proposal**: a follow-up run offered to the queen instead of started automatically (`auto-follow-up: false`).

## Claims

- **Claim**: a record on the provider saying which instance owns a piece of work. It is a claim comment with a marker, plus the claim label and, for tickets, an assignee (work-claiming).
  - **Task claim**: a claim on a ticket, held by the instance whose ant works on it.
  - **Review claim**: a claim on a PR, held for one shepherd round and its fix pass.
- **Lease**: how long a claim stays valid without renewal (default 15 minutes), counted from the provider's timestamp of the claim comment. The holder **renews** it by editing the comment.
- **Live**: a claim whose lease has not expired, or that is parked.
- **Parked**: a task claim held without renewal while its ant is between runs. Other instances treat it as live.
- **Expired**: a claim whose lease ran out without renewal. It counts as released.
- **Released**: a claim that is given up. Anthill removes the claim label and marks the comment as released. A developer can **force-release** any claim.
- **Capacity**: the maximum number of active runs on one instance at the same time (default 2). Ants between runs do not use capacity.

## Ants and runs

- **Ant**: the worker an instance keeps for one accepted item, from acceptance until it is `done` or `abandoned`. It owns the ant branch, the worktree, the session, the time budget and the task claim (ant-execution).
- **Run**: one agent process Anthill starts for an ant. A run ends when the agent calls `finish`, exits or crashes, or when Anthill ends it (after a question, at time-out, on stop or abandon). An ant does anything only through a run, so "the ant calls `push`" means its active run does.
  - **Active run**: a run whose process is alive.
  - **Task run**: a run before the ant's PR exists.
  - **Follow-up run**: a run started for changes after the ant's PR is open. Retries keep the kind of the run they replace.
- **Ant states** (ant-execution):
  - with an active run: `starting`, `running`, `awaiting-extension`, `finishing`;
  - between runs: `queued` (a run is due, but capacity is full), `awaiting-input`, `stopped`, `failed`, `timed-out`, `succeeded` (PR open);
  - final: `done` (PR merged or closed, or no change needed) and `abandoned`.
- **Between runs**: an ant that has no active run and is not final. It has no process at all: it is only a record in the session index. Anthill decides from its polls and from queen actions when to start the next run. Any "waiting" is done by the daemon, never by an agent.
- **Due**: a run Anthill has decided to start, because of an answer, a change, a retry, a continue or an acceptance.
- **Queen actions on an ant**:
  - **stop**: end the active run and keep the claim; the ant is `stopped`;
  - **retry**: start a new run for a `failed` or `stopped` ant, resuming its session with the remaining budget;
  - **continue**: start a new run for a `timed-out` ant with a new budget;
  - **abandon**: end any active run, release the claim, and make the ant `abandoned`;
  - **clean up**: remove an ant's worktree and local branch.
- **Finish**: the `finish` tool call that ends a run. Its **status** is `complete`, `partial` or `no-change`. The **outcome** of a run is how it ended: a finish status, failed, stopped or timed out.
- **Hold**: an item carrying the hold label. A held item is not proposed, and no run starts for its ant (change-reaction).

## Agents and sessions

- **Agent**: the AI coding assistant inside a run or a shepherd round.
- **Runner**: the CLI that hosts an agent: Claude Code, Codex or OpenCode. The **review runner** is the runner used for shepherd rounds and fix passes.
- **Agent role**: what an agent is started for: task run, follow-up run, review agent or fix agent. Each role gets its own set of ant tools (ant-tools).
- **Session**: the runner's saved conversation for an ant. Later runs of the same ant **resume** it. Shepherd rounds never resume a session.
- **Session index**: Anthill's local record per item: runner, session id, worktree, branch, PR, ant state, budget used and run history.
- **Task brief**: the input of an ant's first run: the item, its discussion, the branch, the budget and the conventions to follow.
- **Ant tools**: the MCP tools through which agents do everything that leaves the machine. Each run gets its own **tool token**.

## Time

- **Budget**: the minutes an ant may spend in active runs. It belongs to the ant and carries over between runs. Only a follow-up run and a continue set a new budget (ant-timebox).
  - **Base budget**: the budget at acceptance (default 60 minutes).
  - **Follow-up budget**: the budget of a follow-up run (default half the base budget).
  - **Warning threshold**: the share of the budget at which the ant is warned (default 80%).
  - **Extension**: extra minutes granted on request. The **ceiling** is the most one budget can grow to, extensions included (default 3× the base budget).
  - **Grace period**: the time an ant gets to commit after its budget runs out (default 3 minutes).
  - **Time-out**: the budget is used up; the ant becomes `timed-out`.

## Git

- **Ant branch**: the branch an ant works on, named `ant/<provider>-<id>-<slug>`.
- **Base branch**: the branch an ant branch starts from and its PR targets.
- **Worktree**: the git worktree where an ant's runs work. Shepherd rounds and fix passes use temporary worktrees.
- **Protected branch**: a branch matching the repository's protected patterns. Anthill never pushes to it.
- **Verification command**: the repository's command that checks a fix pass before Anthill pushes it (pr-shepherd).

## Pull requests and the shepherd

- **Ant PR**: a PR opened by an ant. **Human PR**: any other PR.
- **Shepherd**: the part of Anthill that reviews PRs (pr-shepherd). **Shepherd scope** decides which PRs it reviews on its own: `ant-authored` (ant PRs), `labelled` or `all`.
- **Round**: one review of one head commit by a review agent. An **automatic round** starts on its own; a **requested round** starts on `/anthill review`. The **round limit** caps automatic rounds per PR.
- **Review agent**: the agent of a round. It reports only through `submit_review`.
- **Finding**: one problem a round reports, with a **severity** (`blocking`, `suggestion`, `nit`) and a **classification** (`auto-fixable`, `needs-developer`).
- **Auto-fix**: fixing `auto-fixable` findings on a human PR that opted in. A **fix agent** makes the commits in a **fix pass**; Anthill verifies, pushes and replies.

## Changes and comments

- **Change**: a human edit, comment or review on an item or PR. Anthill records each as a **change event** and classifies it as `actionable`, `informational` or `command` (change-reaction).
- **Stored change**: an actionable change kept for the ant's next run because the ant is between runs.
- **Marker**: the hidden machine-readable part of every Anthill comment. It names the instance, the comment kind and the item or PR. "Marker" never means a label.
- **Comment kinds**: `claim`, `question`, `status`, `summary`, `finding`, `reply` (agent-conversation).
- **Question**: a `question` comment an ant posts with `ask_question`. It is **pending** until answered, and **stale** after the question timeout (default 3 days).
- **Command**: an `/anthill <name>` line in a human comment (agent-conversation).

## Configuration

- **Global configuration**: the user's machine-wide settings.
- **Local repository entry**: the user's setting for one repository on this machine, including its clone path.
- **Repository file**: `.anthill.yml` committed in the target repository, holding team-wide settings.

## Writing rules

- "Start", "end", "resume" and "stop" apply to runs. Ants are created, are between runs, and become final.
- Never write that an ant or an agent waits, sleeps or wakes up. Only Anthill schedules runs. Use "waits" only for Anthill itself, for a tool call such as `request_extension`, or for people.
- "Running" is used only as the state name `running`. Say "has an active run" otherwise.
- Use "item" for the tracked work, never "task".
- Use "label" for labels and tags, and "marker" only for the hidden comment part.
- Use "ant PR" and "human PR", not "ant-authored PR". `ant-authored` is only a shepherd scope value.
