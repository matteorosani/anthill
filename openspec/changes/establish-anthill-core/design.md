# Design

## Context

Greenfield repository with no code yet. See proposal.md for motivation and specs/ for the behaviour contract. Constraints that shape the design:

- Anthill runs on **developer machines** (Windows, macOS, Linux), often several at once against the same trackers. There is no shared server, and machines sleep, go offline and are not reachable from the internet.
- Providers are GitHub and Azure DevOps, both reachable only by **polling** from a developer machine. Both have rate limits.
- Agents (Claude Code, Codex, OpenCode) are third-party CLIs with different capabilities: session resume, live input, hooks, MCP support.
- This is a personal/exploratory project, written in Go (D13).

## Goals / Non-Goals

**Goals:**
- A core domain that knows nothing about GitHub, Azure DevOps, git or any specific agent (hexagonal architecture), so providers, runners and UIs can be added as adapters.
- Correct coordination between instances without any shared infrastructure.
- Agents are given no provider credentials and perform no provider writes themselves: they decide, and Anthill executes through ant tools (D4, D5).
- Crash/restart safety: no duplicate ants, no lost or double-handled events.

**Non-Goals:**
- Webhook-based ingestion. It may come later as another driving adapter.
- A central coordination server.
- Sandboxing agents beyond what each runner offers (see Risks).
- Choosing the GUI technology.

## Decisions

### D1. Hexagonal layout

```
            driving adapters                         driven adapters
  ┌──────────────────────────┐   ┌──────────────┐   ┌─────────────────────────┐
  │ Scheduler (poll ticks)   │──▶│              │──▶│ TrackerPort: GitHub,ADO │
  │ Control API (CLI, GUI)   │──▶│  Application │──▶│ CodeHostPort: PRs       │
  │ Ant tools (MCP server)   │──▶│   services   │──▶│ ClaimStore (over above) │
  └──────────────────────────┘   │      +       │──▶│ AgentRunner: claude,    │
                                 │    Domain    │   │   codex, opencode       │
                                 │              │──▶│ SourceControl: git CLI  │
                                 │              │──▶│ StateStore: SQLite      │
                                 └──────────────┘──▶│ Clock, Notifier         │
                                                    └─────────────────────────┘
```

Domain: WorkItem, PullRequest, Prerequisite/EligibilityResult, Claim/Lease, Ant (state machine), Budget, ReviewRound/Finding, ChangeEvent, Command. Application services: `Ingestion`, `Eligibility`, `Claiming`, `AntSupervisor`, `Timekeeper`, `Shepherd`, `ChangeReactor`, `Conversation`.

Ports are split by role, not by provider. One provider adapter can implement several ports, because GitHub is both a tracker and a code host. This allows mixed setups, such as Azure DevOps boards with GitHub repositories.

*Alternative:* one `Provider` interface per vendor. Rejected: it couples ticket and PR concerns and makes mixed setups awkward.

### D2. Providers are the shared source of truth; local state is per machine

Everything other instances must see lives on the provider: claims, questions, findings, command acknowledgements, all as marker-tagged comments plus labels. The local SQLite store holds only machine-private state: sessions, worktrees, budgets, poll cursors and content hashes, the event outbox, and declined/snoozed proposals. Any instance can rebuild the shared picture by reading the providers.

*Alternative:* a shared database or small coordination service. It would give stronger atomicity, but needs hosting, so it is rejected for now. The `ClaimStore` port keeps this option open.

### D3. Claim protocol: post, re-read, earliest wins

GitHub has no compare-and-swap on labels, assignees or comments. A claim is therefore a marker comment, and the winner is the earliest live claim comment by creation order (spec: work-claiming). For Azure DevOps, the tag and assignment update additionally uses a JSON-Patch `test` on `/rev`, which gives true optimistic concurrency.

- Leases use **provider timestamps** (comment `updated_at`), never local clocks, so clock skew between machines does not matter.
- Renewal edits the claim comment. Edits do not notify watchers, which keeps the noise down.
- Re-read after posting waits a short configurable delay (default 3 s) to absorb read-after-write lag.

*Alternatives:* label-only (no way to resolve races) and assignee-only (cannot express machine or lease). Both rejected.

### D4. Agents decide, Anthill executes provider and push operations

Ants edit files and commit inside their worktree. Everything that leaves the machine goes through an ant tool (D5) that Anthill executes: `push`, `open_pull_request`, `update_pull_request`, `comment`, `reply_to_thread`, `ask_question`, `submit_review`. Each agent role (task run, follow-up run, review agent, fix agent) gets only its own subset (spec: ant-tools). The agent keeps the decisions (PR title, body, draft or ready, what to say and where), while Anthill enforces the branching policy, adds the closing reference, label and marker, and applies comment safety in one code path.

Agents get no provider tokens: Anthill strips `GH_TOKEN`, `GITHUB_TOKEN`, `AZURE_DEVOPS_EXT_PAT` and similar from the environment, sets `GIT_TERMINAL_PROMPT=0` and disables the git credential helper for the worktree, so a direct `git push` fails. Where the runner supports hooks, a pre-tool hook refuses `git push`, `gh` and `az` with a pointer to the right tool.

This is a **guard, not a sandbox**. The agent runs as the developer's OS user, so a determined agent could still read the developer's `gh` config or OS credential store. The design stops accidents and casual prompt-injection, not a hostile agent (see Risks). The same holds for stopping a run (D6): on Unix, a process the agent starts in a process group or session of its own, with `setsid` for example, is out of reach of the stop and keeps running.

*Alternatives:* let agents use `gh`/`az` directly (simple, but the policy becomes advisory and comments lose markers and redaction), or have agents only report intent through `finish` while Anthill writes the PR itself (safe, but the agent loses control of the PR text and draft state). Both rejected.

### D5. Ant tools: a stdio MCP proxy over the control pipe

Each run gets an MCP server entry in the runner's configuration that launches `anthill mcp --ant <token>`. This small process speaks MCP over stdio to the agent and relays every call over the daemon's local control pipe (D10), where the token is checked against the one live run it belongs to. The tool set is defined in ant-tools. All three target runners support stdio MCP servers, so there is no HTTP listener and no port to secure.

*Alternative:* a CLI the agent calls from the shell (`anthill ask "…"`). It works with any runner, but tools are not discoverable by the agent, arguments are free text, and results are unstructured. Rejected; MCP only.

Delivering the **80% warning**, extension answers and live ticket updates:
1. Live input where the runner supports it (Claude Code streaming JSON input).
2. Otherwise a runner hook where available (Claude Code hooks can inject context on each tool use).
3. As a last resort, a notice appended to the result of the next ant-tool call, and the brief instructs the ant to call `time_remaining` periodically.

The hard limit never depends on the agent's cooperation: at budget plus grace, the process tree is killed.

*Alternative:* parse agent stdout for intents. Fragile across runners, so rejected.

### D6. AgentRunner port

`start(brief, worktree, budget, tools endpoint) → session`, `resume(session, input)`, `send(input)` (optional capability), `stop(grace)`, `capabilities()` (resume, live-input, hooks, usage), `usage(session)` (tokens and cost when the runner reports them; Claude Code and Codex print usage in their structured output, others may not, so totals are best-effort). Runners run as child processes in their own process group (a Job Object on Windows), so stopping kills the whole tree. Headless permission modes are configured per runner so agents can edit and run commands inside the worktree without prompts.

### D7. Polling, diffing and an event outbox

Each provider adapter polls with incremental filters: GitHub conditional requests with ETags and `since=` (304 responses do not consume the rate limit), and Azure DevOps WIQL on `System.ChangedDate` plus revision checks. Normalized items are hashed per field group. Differences become `ChangeEvent`s, written to a local **outbox** in the same transaction as the new hashes. Handlers (eligibility, change reaction, shepherd, commands) consume the outbox and mark events done. Provider writes are idempotent thanks to markers: before posting, check whether a comment with the same marker id exists. Together this gives the exactly-once handling the specs require across restarts.

*Alternative:* webhooks. Rejected because developer machines are not reachable. They could be added later as a driving adapter feeding the same outbox.

### D8. Relevance classification of comments

A cheap, short agent call classifies human comments as actionable, informational or command, with the item context. Commands are recognised by parsing first, before any agent call. When classification fails, the default is `actionable`. Because follow-ups start automatically by default, a misclassification costs one unwanted run. That cost is bounded by the follow-up budget (half the base budget) and the follow-up limit (5 per PR), and repositories that find it too costly can set `auto-follow-up: false`.

### D9. Shepherd reviews in a detached worktree with structured output

A review round checks out the PR head in a temporary detached worktree. The review agent must return structured JSON findings: file, line range, severity, classification, message, suggested fix. Anthill de-duplicates findings against open threads by file, line-range proximity and a normalized message fingerprint, then posts them. Auto-fix runs only on opted-in human PRs (D19): a fix agent commits in a temporary worktree of the PR branch, then Anthill runs the configured verification command and pushes only if it passes.

### D10. Daemon and control interface

A single daemon per user and machine, enforced by a lock file plus a check that the process is alive. The control API runs over a Unix domain socket or a Windows named pipe, so it is local-only by construction, using a simple request/stream protocol (JSON-RPC). The CLI is a thin client, and a later GUI uses the same API. Desktop notifications go through a `Notifier` port.

### D11. Native links only for dependencies and hierarchy

Dependencies and parent/child relations are read only from provider-native links: GitHub "blocked by" relations and sub-issues, and Azure DevOps Predecessor and Child links. Eligibility then stays deterministic and cheap: no text parsing and no agent call, and a single source of truth that developers already edit in their tracker UI.

*Alternative:* also parse `Depends on #id` in bodies. Rejected: it creates a second, easily stale source and ambiguity when text and links disagree. Cost: dependencies across providers cannot be expressed.

### D12. Leaves only for now; inherited opt-in deferred

Only leaf items (no open children) are eligible, and opt-in is per item. Ants work on the smallest units, and parents are containers.

*Alternative (deferred, needs analysis):* labelling a parent opts in its children. Children would be proposed in dependency or backlog order, and the parent closed when all are done. Not done now because it raises unresolved questions: ordering between siblings, whether siblings share a branch or PR, who closes the parent, and how partial opt-in interacts with explicit labels. To be analysed in a later change. The `is-leaf` prerequisite and the parent/child links in the normalized model are the hooks it will build on.

### D13. Implementation language: Go

Anthill is written in Go (current stable release), as one module `github.com/matteorosani/anthill` that builds one `anthill` binary for Windows, macOS and Linux. Go fits the learning goal, has a good process and concurrency model for a supervisor daemon, and cross-compiles to static binaries.

Rust was rejected because its steeper curve slows down an exploratory project and async process supervision on Windows takes more effort. C# was rejected because it does not serve the learning goal.

Layout follows D1: `cmd/anthill` (CLI, daemon and `anthill mcp` entry points), `internal/domain` (no I/O), `internal/app` (application services), `internal/ports` (interfaces), `internal/adapters/<name>` (one package per adapter), and `internal/testkit` (fakes for every port).

Main libraries, chosen for being maintained and pure Go where possible:
- `google/go-github` for GitHub. Azure DevOps is called over its REST API with `net/http` and `encoding/json`. `microsoft/azure-devops-go-api` was rejected: its last release dates from 2023, its typed calls never expose the HTTP response, whose Date and `Retry-After` headers the adapter needs, and its per-area clients accept no injected transport for tests;
- `modernc.org/sqlite` for the local store, so no C compiler is needed;
- the official `modelcontextprotocol/go-sdk` for the ant tools;
- `spf13/cobra` for the CLI;
- `Microsoft/go-winio` for the Windows named pipe;
- `zalando/go-keyring` for the OS credential store;
- `go.yaml.in/yaml/v3` for the configuration files, because it is the maintained continuation of `gopkg.in/yaml.v3`, is pure Go, and gives a node tree to check keys before decoding;
- the OpenTelemetry Go SDK and OTLP exporters;
- `gen2brain/beeep` for desktop notifications.
- `golang.org/x/text` for Unicode normalization, so branch slugs turn accented letters into ASCII.

The marker is a hidden HTML comment holding a JSON object (`<!-- anthill {…} -->`). Both providers keep HTML comments in Markdown bodies without showing them.

### D14. Anthill acts as the developer

Comments, PRs and commits use the developer's own provider login (`gh`/`az` session or a stored token) and git identity. Anthill activity is told apart only by the visible 🐜 prefix, the hidden marker and the commit trailers, never by account. This is why work-item-sources decides authorship by marker alone.

*Alternative:* a shared bot account or GitHub App. It separates human and automated activity more clearly, but needs org-level setup and a secret distributed to every machine. Rejected for now. It can be added later as another credential source without changing the specs, except for authorship detection.

### D15. No claim transfer; takeover deferred

A parked claim stays with its instance for the life of the ant PR. If that machine disappears, developers take over the PR by hand.

*Alternative (deferred):* takeover after inactivity. If the holder has not renewed or acted for N hours (for example 24) and actionable work exists, another instance takes the claim, rebuilds the worktree from the pushed branch and starts a fresh, non-resumed session with a summary brief. Deferred because it needs a reliable "holder is gone" signal, a hand-over of follow-up and review state, and a rule against two machines both believing they own the PR.

### D16. No local verification before opening a PR

Anthill trusts the ant's `finish` status and opens the PR without running builds or tests. This keeps the finish path simple and runner-agnostic, and avoids duplicating CI. The repository's verification command is still used to guard shepherd auto-fixes (D9).

*Alternative:* run the verification command after `finish` and feed failures back to the ant within its budget. It would catch broken PRs earlier, but costs agent time and assumes a reliable local build on every machine. It can be added later as a repository option.

### D17. Session tracking

Runners store sessions themselves, keyed by working directory: Claude Code under `~/.claude/projects/<encoded cwd>`, Codex under `~/.codex/sessions`, OpenCode in its own data directory. Anthill therefore always starts and resumes a run with the worktree as working directory, captures the session id from the runner's structured output at start, and stores it in the local session index with the item, branch, PR and runner. Resuming passes that id back (`--resume <id>` or equivalent).

If the index is lost, it is rebuilt from claim comments and `ant/` branches (spec: ant-execution), but the session ids are not recoverable from providers. The next run then starts a new session with a summary brief built from the ticket, the PR and its threads. Review rounds never resume (spec: pr-shepherd).

### D18. OpenTelemetry, linked to agent telemetry

Anthill uses the OpenTelemetry SDK for its own traces, metrics and logs, exported over OTLP only when a local endpoint is configured (spec: observability). Each run is one trace. When export is on, Anthill also switches on the runner's own OTel export (for example `CLAUDE_CODE_ENABLE_TELEMETRY=1` with the same endpoint) and sets `OTEL_RESOURCE_ATTRIBUTES` with `anthill.item`, `anthill.ant_id` and `anthill.instance`, plus `TRACEPARENT` where the runner honours it. Agent spans and metrics can then be joined to the Anthill run even when the runner does not propagate trace context. Prompt and content capture in the runner stays off. The agent also gets the OTLP headers, through `OTEL_EXPORTER_OTLP_HEADERS`, so it can export to an endpoint that needs authentication. The agent can therefore read a credential, but one that is only for telemetry, never a provider or git credential (D4).

*Alternative:* scrape agent transcripts into Anthill's own telemetry. Duplicates what the runners already export and risks leaking content. Rejected.

### D19. Auto-fix only on opted-in human PRs; the fix agent never pushes

On ant PRs, every shepherd finding worth acting on goes to the owning ant's single follow-up run (spec: change-reaction). A separate fix agent on the reviewing machine could otherwise push to the same branch at the same time as the owning ant. Auto-fix is therefore kept for human PRs that opt in.

There, the fix agent only edits, commits and reports per finding through `finish_fixes`. Anthill runs the verification command, then pushes and replies with the commit links. A failed verification is discarded before anything reaches the branch, which matters because the branch belongs to a person and the `push` tool cannot force-push. A repository without a verification command gets suggested changes only.

*Alternative:* give the fix agent `push` and `reply_to_thread`. Rejected: verification would depend on the agent, and a bad push could not be taken back.

### D20. Run outcomes

An ant is the worker for one item and lives until its PR is merged or closed (`done`) or it is abandoned. A run is one agent process of that ant. Every exit without `finish`, clean or crashed, is `failed`. Anthill pushes the branch so the work is safe but opens no PR, and turns an open PR into a draft so the shepherd does not review half-done work. The queen retries or abandons. A stop by the queen or `/anthill stop` ends the run, pushes nothing and parks the claim, so the ant is `stopped` until a retry. When an ant finds that nothing needs changing, `finish(no-change)` posts its explanation, releases the claim and adds the hold label, leaving the ticket to a human. An extension request pauses the budget only while the tool waits. After that the run goes on, and a late approval on a timed-out ant acts as "continue".

## Risks / Trade-offs

- **Prompt injection through ticket and PR content** (anyone who can comment can write text an agent reads) → Only items opted in by a user with write/triage permission count, because adding labels needs it. Only answers and commands from write-access users are acted on. Agents are given no credentials (D4, with its limits), and all pushes go through the `push` tool and the branching policy.
- **Agents running unattended with broad local permissions** → Run only inside the ant worktree with each runner's most restrictive headless mode that still allows edits and tests. Document that ants run with the developer's OS user. Container sandboxing is a later option.
- **Claim races on GitHub due to eventual consistency** → Re-read delay, an earliest-comment rule based on server ordering, and lease expiry as the backstop. A lost race can still waste a few seconds of setup, which is acceptable.
- **Rate limits multiply with the number of machines** → Conditional requests, a longer default poll interval for PR review threads, and back-off on reported limits. Monitor this through the health command.
- **Comment noise on tickets and PRs** → Renewals edit instead of posting, statuses are folded into one comment per run, and the visible prefix makes Anthill comments easy to filter.
- **Polling latency (minutes, not seconds)** → Acceptable for this kind of work. The interval is configurable.
- **Agent cost** → Concurrency limit, time budgets, round and follow-up limits, the smaller follow-up budget, and queen consent for new tasks. Automatic follow-ups can be turned off per repository.
- **Orphaned claims from machines that never come back** → Leases expire for active claims. Parked claims are never transferred (D15), so their PRs fall back to developers. `/anthill release` and the holder shown in the claim comment let humans intervene.
- **Forged markers** (anyone who can comment can paste a marker) → Claims and acknowledgements count only when the comment's author has write access to the repository. A forged marker from an outside user is treated as a human comment.
- **Broken PRs without local verification (D16)** → CI and the shepherd catch them, and follow-up runs fix them.
- **Agents reaching the developer's credentials** (same OS user, D4) → Tokens stripped from the environment, credential helper disabled, hooks refusing `git push`/`gh`/`az`, and the brief tells the agent to use the tools. A hostile or injected agent could still read credential files. Running agents under a separate OS user or in a container is the later fix.
- **Telemetry leaking content** → Export is off by default, Anthill exports no ticket, comment or code content, and the runners' content capture is forced off. The endpoint lives only in local configuration, and its headers in the OS credential store.

## Migration Plan

Not applicable (greenfield). Rollout: one developer first, with shepherd scope `ant-authored`, overriding the default with `auto-follow-up: false` while trialling. Then enable more machines on the same repositories to exercise claiming.

## Open Questions

- Hierarchy-aware execution (inherited opt-in, sibling ordering, parent closure). See D12. Needs its own analysis and change.
- Claim takeover after holder inactivity. See D15.
- Optional local verification before opening a PR. See D16.
- GUI technology. Deferrable: the GUI is just another client of the control API.
- Whether stacked ant branches (working on top of an unmerged dependency PR) should be supported later. The current specs forbid it.
- Per-runner details such as headless permission flags, which hooks are used, how session ids are captured (D17) and which OTel variables each runner honours (D18). Fixed for Claude Code in `docs/runners.md`; for Codex and OpenCode when their adapters are built.
- Stronger agent isolation (separate OS user or container) to close the credential gap in D4.
- Agents left running after the daemon itself crashes or is killed. On Windows, the Job Object ends them when the daemon's handle to it closes. On Unix, the process group survives the daemon, so an agent could keep working while restart reconciliation (ant-execution) resumes its session or marks it failed. Reconciliation should find and end such processes first, for example from a process group id recorded with the run.
