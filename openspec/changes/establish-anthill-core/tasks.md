# Tasks

Terms follow `openspec/glossary.md`. Every group lands its own tests and docs. Unit tests use fakes from `internal/testkit`; tests that touch git use temporary repositories; no task needs a real provider or a real agent unless it says so.

## 1. Project setup

- [x] 1.1 Create the Go module `github.com/matteorosani/anthill` with the D13 layout (`cmd/anthill`, `internal/domain`, `internal/app`, `internal/ports`, `internal/adapters`, `internal/testkit`), and verify `go build ./...` and `go vet ./...` pass
- [x] 1.2 Add a `golangci-lint` configuration and a `Makefile` (or `magefile`) with `build`, `test`, `lint` targets, and verify `make lint test` passes on the empty project
- [x] 1.3 Add a GitHub Actions workflow that builds, lints and runs `go test -race ./...` on Linux, macOS and Windows, and verify it is green on `main`
- [x] 1.4 Add a developer section to `README.md` (prerequisites, build, test), and verify the documented commands run as written

## 2. Domain: items, labels and markers

- [x] 2.1 Add the normalized work item and pull request types, item references (`gh-12`, `ado-12`) and provider-independent states (`open`, `closed-completed`, `closed-not-planned`), and verify unit tests cover parsing and printing of references
- [x] 2.2 Parse per-item setting labels (`agent:`, `budget:`, `base:`, `repo:`), including invalid values that are ignored with a warning, and verify table tests cover the `budget:abc` and `budget:600` scenarios
- [x] 2.3 Implement the marker as a hidden HTML comment with JSON (instance id, comment kind, item or PR, lease fields for claims) and the visible 🐜 prefix, and verify round-trip tests and a test that text without a marker parses as human
- [x] 2.4 Implement secret redaction with the built-in patterns plus added local patterns, and summary truncation (default 4,000 characters), and verify tests for a GitHub token, a cloud key, a private key, a local pattern and truncation with a note
- [x] 2.5 Implement branch naming and the slug rules, and verify tests for the "Fix login timeout on Safari" scenario, non-ASCII titles and the 40-character limit

## 3. Domain: eligibility, claims, ants and budgets

- [x] 3.1 Implement each prerequisite as a named check returning met/unmet with a reason, and evaluation returning all unmet prerequisites, and verify one test per task-eligibility scenario
- [x] 3.2 Implement claims: kinds, lease expiry from the provider timestamp plus the lease in the marker, live/parked/expired/released, earliest-live-claim resolution with comment id tie-break, and ignoring claims by users without write access, and verify tests for every work-claiming scenario that needs no provider
- [x] 3.3 Implement the ant state machine with all states of ant-execution, allowed transitions, and a recorded timestamp and reason per transition, and verify table tests for every allowed transition and that every other transition is refused
- [x] 3.4 Implement the budget: base budget sources and cap, counting only `starting`, `running` and `finishing`, carry-over between runs, follow-up budget, warning threshold, extensions with auto-approve and ceiling, grace period, and late decisions, and verify tests with a fake clock for every ant-timebox scenario
- [x] 3.5 Implement findings (severity, classification, location) and the de-duplication fingerprint from D9, and verify tests that an unchanged problem matches its earlier finding and a moved one by a few lines still matches
- [x] 3.6 Implement change events, the "who can trigger work" rule and the `actionable`/`informational`/`command` rule for edits, and verify tests for the outside commenter and ticket author scenarios
- [x] 3.7 Implement `/anthill` command parsing (one per line, unknown commands answered with help) and command authorization, and verify tests for every command, `/anthill deploy`, and `/anthill fix` by a PR author without write access

## 4. Configuration and credentials

- [x] 4.1 Implement configuration layers (item labels, `.anthill.yml`, local repository entry, global configuration, defaults) with the effective value and its source, and verify the "repository file overrides global default" test
- [x] 4.2 Implement the `.anthill.yml` schema with the allowed keys only, rejecting credentials, OTLP settings and secret patterns, and verify tests for the `token` key and an OTLP endpoint
- [x] 4.3 Implement the global configuration and local repository entries (clone path, runner, capacity, reviewing off per repository or machine, OTLP endpoint, added secret patterns), rejecting OTLP headers in files, and verify tests for each rejection message
- [x] 4.4 Implement the built-in defaults from anthill-configuration, and verify the "empty configuration" test lists every default
- [x] 4.5 Implement validation at startup and reload, where an invalid repository configuration disables only that repository, and verify the "warning threshold out of range" test and a test that one bad repository leaves the others running
- [x] 4.6 Implement credential sources (OS credential store through `go-keyring`, `gh` and `az` CLI sessions) behind a port, and verify with a fake store that no credential value reaches logs or errors
- [x] 4.7 Write `docs/configuration.md` with every setting, its layer and default, and verify it matches the defaults test by a test that reads the doc's settings table

## 5. Ports, local store and testkit

- [x] 5.1 Define the ports from D1 (`TrackerPort`, `CodeHostPort`, `ClaimStore`, `AgentRunner`, `SourceControl`, `StateStore`, `Clock`, `Notifier`, credential source), and verify they compile with no adapter imports in `internal/domain` or `internal/app` (enforced by a lint rule)
- [x] 5.2 Add fakes for every port in `internal/testkit`, including a fake provider that can simulate races, rate limits and errors, and verify each fake has its own tests
- [x] 5.3 Implement the SQLite store (`modernc.org/sqlite`) with migrations for the session index, runs, usage, budgets, poll cursors and hashes, the outbox, stored changes, questions, extension requests, and declined or snoozed proposals, and verify migration and repository tests on a temporary database
- [x] 5.4 Implement the outbox: change events written in the same transaction as the new hashes, handlers marking them done, and restart replay, and verify a test that a crash between detection and handling handles the event exactly once

## 6. Git and worktrees

- [x] 6.1 Implement the git CLI adapter: fetch, base branch resolution (`base:` label, repository setting, remote default), and branch creation from the fetched base, and verify tests on temporary repositories including "base branch missing"
- [x] 6.2 Create worktrees under the worktree root and never touch the developer's working copy, and verify a test that a clone with uncommitted changes is unchanged after starting
- [x] 6.3 Detect a foreign branch with the same name through the `Anthill-Instance` trailer and add a numeric suffix, and verify the "name already taken" test
- [x] 6.4 Add the commit trailers (`Anthill-Item`, `Anthill-Agent`, `Anthill-Instance`) through a worktree-local `prepare-commit-msg` hook, and verify that a commit made in the worktree carries all three
- [x] 6.5 Configure the worktree so agent pushes fail: credential helper disabled, `GIT_TERMINAL_PROMPT=0`, provider tokens removed from the agent environment, and verify a test that `git push` from that environment fails
- [x] 6.6 Implement the push policy: only Anthill-created branches or opted-in human PR branches, never base, default or protected branches, no force-push, rebase and amend allowed only before the first push, and verify tests for each branching-policy scenario
- [x] 6.7 Implement worktree cleanup (local branch, remote branch only when the PR is merged and the provider kept it, confirmation for unpushed commits), and verify tests for "PR merged" and "unpushed work"

## 7. GitHub adapter

- [ ] 7.1 Implement authentication from the credential store or the `gh` session, and verify with `httptest` that requests carry the token and that the token is never logged
- [ ] 7.2 Implement issue polling with ETags and `since`, mapping labels, assignees, "blocked by" links, sub-issues, comments and `closed-not-planned`, and verify tests against recorded API fixtures
- [ ] 7.3 Implement pull request polling with head commit, draft flag, review threads and the link to the item, and verify fixture tests including the "PR body reference removed" case
- [ ] 7.4 Implement writes: comments with markers (idempotent by marker id), comment edits, labels, assignees, open and update PR with closing reference and `anthill` label, draft and ready, review comments, thread replies and resolution, and verify fixture tests for each call
- [ ] 7.5 Implement the write-access lookup with caching, and verify tests for a collaborator, a non-collaborator and a cache hit
- [ ] 7.6 Handle rate limits by pausing until the reported reset, and verify the "rate limit exhausted" test with no requests sent before the reset

## 8. Azure DevOps adapter

- [ ] 8.1 Implement authentication from the credential store or the `az` session, and verify with `httptest` that the token is sent and never logged
- [ ] 8.2 Implement work item polling with WIQL on `System.ChangedDate`, tags, Predecessor and Child links, `System.Rev` and the Acceptance Criteria field, and verify the "Azure DevOps bug mapped" fixture test
- [ ] 8.3 Implement repository mapping per project and the `repo:` tag, with unmapped items, and verify the "Azure DevOps item with no repository" test
- [ ] 8.4 Implement pull request polling and writes, including `AB#12` closing references and thread replies, and verify fixture tests for each call
- [ ] 8.5 Implement claim writes with a JSON-Patch `test` on `/rev`, treating a conflict as a lost race, and verify the "optimistic concurrency" fixture test

## 9. Ingestion and change detection

- [ ] 9.1 Implement the poll scheduler per provider connection with independent failures and the last successful poll time, and verify the "one provider unavailable" test with fakes
- [ ] 9.2 Implement hashing per field group, typed change events into the outbox, and the Anthill-authored flag by marker only, and verify the "new comment", "no change" and "developer comments from the same account" tests
- [ ] 9.3 Apply the opt-in label filter and ingest items when the label is added, and verify the opt-in scenarios of work-item-sources

## 10. Claiming

- [ ] 10.1 Implement claim acquisition: check, post, re-read after the delay, earliest live claim wins, only the winner adds the claim label and assignee, the loser retracts its comment, and verify the "two instances claim simultaneously" test with the racing fake provider
- [ ] 10.2 Implement renewal by editing the claim comment, parking while the ant is between runs, release with unassignment only when Anthill assigned, and verify tests for each work-claiming release case
- [ ] 10.3 Detect force-release (`/anthill release` or removed claim label) and a lost claim, ending any active run and making the ant `abandoned` with reason "claim lost", and verify the "holder returns after expiry" and "stuck claim released" tests

## 11. Daemon, control interface and notifications

- [ ] 11.1 Implement the daemon process with the single-instance lock, and verify the "second daemon start" test
- [ ] 11.2 Implement the JSON-RPC control interface over a Unix socket and a Windows named pipe (`go-winio`), local-only, with streaming responses, and verify a client round-trip test on each OS in CI
- [ ] 11.3 Implement the CLI skeleton with `cobra` as a thin client, with `anthill daemon` and `anthill status`, and verify an end-to-end test that `status` reports uptime from a running daemon
- [ ] 11.4 Implement the `Notifier` port with desktop notifications (`beeep`) and a per-kind switch, and verify with a fake notifier that each notification kind of queen-console is raised and can be turned off

## 12. Ant tools

- [ ] 12.1 Implement `anthill mcp --ant <token>` as a stdio MCP server (`modelcontextprotocol/go-sdk`) that relays calls to the daemon, and verify a test with an MCP client listing the tools of each role
- [ ] 12.2 Implement tool tokens per run, revoked when the run ends, and verify the "token reused after the run" test
- [ ] 12.3 Implement `get_task`, `time_remaining` and `request_extension` (waiting up to the extension wait, returning `approved`, `reduced`, `denied` or `pending`), and verify the ant-tools scenarios for each
- [ ] 12.4 Implement `ask_question`, `report_progress`, `comment` and `reply_to_thread`, with markers and redaction on all posted text, and verify the "agent replies to a review thread" and "comment containing a secret" tests
- [ ] 12.5 Implement `push`, `open_pull_request` and `update_pull_request` through the push policy and the code host, and verify the "push rejected because the remote moved", "agent opens a ready PR" and "second PR for the same item" tests
- [ ] 12.6 Implement `finish` with status checks against new commits, `submit_review` and `finish_fixes`, and verify the "complete without commits" and "no-change with commits" tests
- [ ] 12.7 Implement role tool sets and the check of every call against the ant's state, claim and the branching policy, and verify the "fix agent cannot push", "review agent tool list" and "call while waiting for an extension" tests
- [ ] 12.8 Implement message delivery (live input, hook, or notice on the next ant-tool result), and verify with fakes that each runner capability gets the right means
- [ ] 12.9 Write `docs/ant-tools.md` describing every tool, its role and its refusals, and verify every tool in the code is documented by a test that compares the two lists

## 13. Agent runners

- [ ] 13.1 Implement the `AgentRunner` process layer: a process group on Unix and a Job Object on Windows, stop with grace then kill of the whole tree, and verify with the fake agent that stopping leaves no child process on each OS in CI
- [ ] 13.2 Build the fake agent in `internal/testkit` that follows a script (call tools, commit, print usage, exit or crash), and verify it runs headless and connects to `anthill mcp`
- [ ] 13.3 Implement the Claude Code adapter: headless streaming JSON, session id capture, resume, live input, MCP server injection, hooks that refuse `git push`, `gh` and `az`, usage parsing and telemetry variables, and verify tests against recorded output plus a manual smoke run documented in `docs/runners.md`
- [ ] 13.4 Implement the Codex adapter with the same capabilities where Codex supports them, and verify tests against recorded output plus a documented manual smoke run
- [ ] 13.5 Implement the OpenCode adapter with the same capabilities where OpenCode supports them, and verify tests against recorded output plus a documented manual smoke run
- [ ] 13.6 Implement runner selection (repository, `agent:` label, global default) and the availability check, and verify the "label overrides repository default" and "runner not installed" tests

## 14. Ant supervision

- [ ] 14.1 Implement acceptance: re-check eligibility on fresh data, claim, branch and worktree, start the first run, and undo every done step on failure, and verify the "claim lost at start" and "became ineligible" tests
- [ ] 14.2 Implement capacity and the scheduling of due runs, with `queued` ants started when a slot frees if the run is still due, and verify the "queued for capacity" and "answer arrives while capacity is full" tests
- [ ] 14.3 Implement the task brief and the summary brief for a new session, excluding Anthill comments, and verify the "brief includes discussion" test
- [ ] 14.4 Implement finish handling: `complete`, `partial` (forced draft with open points), `no-change` (explanation, hold label, release, `done`), and opening the PR when the agent did not, and verify each ant-execution finish scenario
- [ ] 14.5 Implement exit without finishing: `failed`, push when there are new commits, no PR, draft an open PR, failure summary on the ticket, and verify the three exit scenarios
- [ ] 14.6 Implement stop, retry, continue and abandon, and verify the "stop and retry later", "retry after crash" and "abandon an ant with an active run" tests
- [ ] 14.7 Implement the timekeeper: warning delivery, extension requests and late decisions, budget exhaustion with grace period, draft PR and `timed-out`, and verify each ant-timebox scenario end to end with the fake agent and a fake clock
- [ ] 14.8 Implement session index recovery from claim comments, `ant/` branches and PRs, and restart reconciliation with `auto-resume`, and verify the "local database lost" and "daemon restarted mid-run" tests
- [ ] 14.9 Record usage per run and per shepherd round, with unknown values kept as unknown, and verify the "runner reports cost" and "runner without usage data" tests

## 15. Conversation and change reaction

- [ ] 15.1 Implement questions: posting, pending state, answer detection (thread reply, quote or `@anthill`), a run that resumes the session with the answer, and stale questions, and verify each agent-conversation question scenario
- [ ] 15.2 Implement command execution once across instances through the acknowledgement `reply`, by the responsible instance only, and verify the "two instances see the same command" test
- [ ] 15.3 Implement relevance classification: rules first, then a short agent call, with `actionable` as the fallback, and verify tests with a fake runner for the thank-you comment and a classifier failure
- [ ] 15.4 Implement routing: delivery to an active run, stored changes for ants between runs, follow-up runs for `succeeded` ants with batching, follow-up budget and limit, and follow-up proposals when `auto-follow-up` is off, and verify each change-reaction routing scenario
- [ ] 15.5 Implement hold (finish the active run, start no run, keep the claim, store changes, refuse retries) and withdrawal of opt-in, and verify the "hold added during a run", "retry while held" and "label removed mid-run" tests
- [ ] 15.6 Ensure only the responsible instance reacts and Anthill-authored changes are ignored, except shepherd findings on ant PRs, and verify the "colleague's machine ignores the change" test with two instances on one fake provider

## 16. PR Shepherd

- [ ] 16.1 Implement shepherd scope, participation settings and review runner selection, and verify the scope, "machine opted out" and "review runner missing" tests
- [ ] 16.2 Implement round triggers, the round limit with its comment, and requested rounds counted separately, and verify each round scenario
- [ ] 16.3 Implement a round: review claim, temporary detached worktree, new session, `submit_review`, de-duplication against open threads, inline and summary comments, and verify the "blocking finding", "clean round" and "unchanged problem in round 2" tests
- [ ] 16.4 Hand findings on ant PRs to the owning ant's follow-up run as defined by change-reaction, and verify the "findings handed to the owning ant" and "auto-fixable nit included in the run" tests
- [ ] 16.5 Implement auto-fix on opted-in human PRs: opt-in by label or `/anthill fix`, fix pass in a temporary worktree, `finish_fixes`, verification command, push, replies and resolution, and verify the "auto-fix on an opted-in human PR" and "verified fixes" tests
- [ ] 16.6 Implement the auto-fix fallbacks (no verification command, fork not pushable, failed verification, PR branch moved) as all or nothing, and verify each fallback scenario
- [ ] 16.7 Handle PR merge or close: stop shepherd activity, release claims, make the ant `done`, clean up the worktree, and verify the "PR merged by developer" test

## 17. Queen console

- [ ] 17.1 Add proposal commands: list, explain an item's prerequisites, accept with runner, budget and base overrides, decline, snooze and un-hide, and verify CLI tests against a daemon with fakes for each queen-console proposal scenario
- [ ] 17.2 Add ant commands: list, follow the active run's activity, stop, abandon, retry, continue, clean up, and verify the "follow an ant" test and that detaching does not affect the run
- [ ] 17.3 Add commands to list and answer questions, list and decide extension requests, and accept or decline follow-up proposals, and verify the "answer from the console" test
- [ ] 17.4 Add `usage` and a full `status` (providers, rate limits, active runs, queued ants, held claims with lease expiry, active rounds), and verify the "daily usage" and "status overview" tests
- [ ] 17.5 Write `docs/cli.md` with every command and an example session, and verify each documented command exists by a test against the `cobra` command tree

## 18. Observability

- [ ] 18.1 Set up the OpenTelemetry SDK, off until a local OTLP endpoint is configured, with headers from the credential store, and verify with an in-process OTLP receiver that nothing is sent without an endpoint and that headers are sent with one
- [ ] 18.2 Add spans for polls, provider calls, eligibility, claims, runs and tool calls, and shepherd rounds, with the item, PR, ant id, instance and runner attributes, and verify the "ant run trace" test with an in-memory exporter
- [ ] 18.3 Add the metrics from observability and export application logs and captured agent activity linked to the trace, with redaction and no content, and verify the "usage metrics" and "captured agent log line with code" tests
- [ ] 18.4 Switch on agent telemetry with Anthill resource attributes and trace context, with content capture off, and verify the "agent telemetry carries Anthill attributes" and "agent prompt logging" tests against the fake agent's environment
- [ ] 18.5 Write `docs/observability.md` with the setup, attributes and metrics, and verify the documented metric names match the code by a test

## 19. Integration checks

- [ ] 19.1 Run the full ant lifecycle on a fake provider with the fake agent: accept, first run, question and answer, PR, shepherd round, follow-up run, merge, `done`, and verify the test passes on all three OSes in CI
- [ ] 19.2 Run two instances against one fake provider racing for the same item and the same PR, and verify exactly one ant and one round result
- [ ] 19.3 Restart the daemon during a run and with a pending change, and verify the change is handled once and the ant is `failed` or resumed according to `auto-resume`
- [ ] 19.4 Do a manual smoke test on a sandbox GitHub repository and a sandbox Azure DevOps project with Claude Code, following and updating `docs/smoke-test.md`, and verify every step in it passes

## Workflow follow-up

- Archive the change with `openspec archive establish-anthill-core` once all tasks are done, which moves the specs into `openspec/specs/`.
- Verify the archived specs with `openspec validate --strict`.
