# Proposal

## Why

Development work is tracked across several issue trackers (mainly GitHub and Azure DevOps), and a growing share of it is small and well specified enough for a headless coding agent to do. Today each developer has to pick that work by hand, start an agent, watch its time, open the PR and chase reviews. Anthill automates the loop: it finds tickets that are ready for an agent, offers them to the developer (the **ant queen**), runs agents (**ants**) in isolated worktrees under a time budget, opens PRs, and reviews them (the **PR Shepherd**). Since this is a greenfield project, this change sets out the behavioural contract before any code exists. It fixes the vague parts of the idea: what "eligible" means, how several developer machines avoid working on the same item, branching rules, time budgets, and how agents react to updates.

## What Changes

- Introduce a provider-agnostic **work item** model and polling of GitHub Issues and Azure DevOps Work Items. Only items carrying a configurable opt-in label/tag are considered.
- Define **eligibility** precisely as a set of named, evaluable prerequisites, with a reason reported for every unmet one.
- Introduce a **distributed claim protocol** stored on the provider itself (labels/assignment and marker comments, with leases). Several Anthill instances on different developer machines can then run against the same trackers without duplicate work.
- Introduce **ants**: headless coding agents (Claude Code, Codex, OpenCode, …) behind one runner port. Each ant runs in its own git worktree and branch, and its session is kept so it can be resumed.
- Introduce **time budgets** for ants: a default of 60 minutes, a warning at 80%, extension requests, and a defined outcome when time runs out.
- Define **branching rules**: branch naming, base branch resolution, worktree placement, history rewriting limits, and which branches Anthill may push to.
- Introduce the **PR Shepherd**: polls open PRs, runs a limited, configurable number of agent review rounds, posts review comments, auto-fixes trivial findings where allowed, and honours developer requests for more rounds. It never merges.
- Define **update reaction**: when a ticket or PR changes (edits, new comments, review feedback), Anthill decides whether an ant must act. It delivers the change to a running ant or resumes the previous session.
- Define how agents and developers **talk through comments**: marker-tagged bot comments, questions that pause an ant, and `/anthill` commands.
- Introduce the **queen console**: a long-running local daemon plus a CLI for reviewing proposals, accepting work, approving extensions and watching ants. A GUI can be added later as another adapter.
- Define **configuration**: global, per-provider and per-repository settings, and credentials.
- Introduce **ant tools**: the local MCP tool set through which agents push, open PRs, comment, ask questions, request time and finish, so agents never hold provider or push credentials.
- Introduce **observability**: optional OpenTelemetry export of Anthill's traces, metrics and logs, linked to the agents' own telemetry, with content never exported.

Out of scope for this change: implementation tasks (this change refines the specs only; tasks will follow), the GUI, automatic merging, automatic acceptance of tasks without the queen, hierarchy-aware execution (opt-in inherited from parent items; only leaf items are eligible for now), and providers other than GitHub and Azure DevOps.

## Capabilities

### New Capabilities
- `work-item-sources`: polling providers for opted-in tickets and PRs, mapping them to a normalized model, and detecting changes between polls.
- `task-eligibility`: the prerequisites that make a work item eligible and the proposal list shown to the queen.
- `work-claiming`: the cross-machine claim/lease protocol for tasks and PR reviews, including release, expiry and takeover.
- `ant-execution`: starting, supervising, resuming and finishing headless agents in isolated worktrees, ending in a PR.
- `ant-timebox`: time budgets, the 80% warning, extension requests and the time-out outcome.
- `branching-policy`: branch naming, base branch resolution, worktree layout, commit/push and history rules.
- `pr-shepherd`: polling PRs, review rounds and their limits, review comments, auto-fix, and developer-requested rounds.
- `change-reaction`: detecting relevant updates on tickets and PRs and routing them to running or resumed ants.
- `agent-conversation`: comment conventions between Anthill and developers: markers, questions, answers, and `/anthill` commands.
- `queen-console`: the daemon and CLI the queen uses to see proposals, accept or decline work, and control ants.
- `anthill-configuration`: configuration layers, per-repository settings, defaults, and credential handling.
- `ant-tools`: the per-run MCP tools agents use for every provider and push operation, their checks, and how messages reach a running agent.
- `observability`: OpenTelemetry traces, metrics and logs, linking agent telemetry to Anthill runs, and what is never exported.

### Modified Capabilities
_None. There are no existing specs._

## Impact

- New codebase (greenfield). Hexagonal architecture: a core domain with ports for issue trackers, source control and PRs, agent runners, persistence, clock and notifications. Adapters for GitHub, Azure DevOps, git worktrees, and the Claude Code/Codex/OpenCode CLIs.
- External dependencies: GitHub REST/GraphQL API, Azure DevOps REST API, the local `git` CLI, and the agent CLIs installed on each developer machine.
- Shared state lives on the providers (labels, assignees, comments). Each machine keeps only a local store for sessions, budgets and poll cursors.
- Runs on developer machines (Windows, macOS, Linux). The implementation language is still open (Go or Rust for learning, C# as the fallback). The specs are language-neutral.
