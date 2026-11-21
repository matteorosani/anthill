# Anthill command line

The `anthill` command is the queen console (spec queen-console): the CLI you, the queen, use to run the daemon, review proposals, accept or decline work, answer ants and control them. Every console command is a thin client: it asks the running daemon over the local control interface and prints the answer (design D10). The control interface takes connections from this machine only.

This page lists every command, then shows an example session. `anthill <command> --help` prints the same flags.

## Conventions

### Items

Commands that act on one item take its reference as the first argument. The daemon resolves it against the repositories configured on this machine:

| You type | Means |
|----------|-------|
| `#12` or `12` | item 12 of the only configured repository |
| `gh-12` | item 12 of the only configured GitHub repository |
| `AB#12` or `ado-12` | work item 12 of the only configured Azure DevOps project |
| `acme/shop#12` | item 12 of that repository, or of that Azure DevOps project (`contoso/shop#12`, or `contoso/shop/web#12` for one of its code repositories) |

A short form that more than one repository could hold is refused, and the message names the repositories: write `<repository>#<number>` then.

Commands on an ant act on the item's current ant on this machine.

### Exit codes and errors

A command exits with 0 on success, 1 when it failed, and 2 when the command line itself is wrong. Errors go to standard error, after `anthill:`. One error is common to every console command:

- `anthill: daemon not running`: start it with `anthill daemon`.

### Durations and times

Ages, budgets and durations print as `2d3h`, `1h30m`, `45m`, or `30s` under a minute. Times print in your local time zone.

## Daemon and health

### `anthill daemon`

Runs the daemon in the foreground until Ctrl+C or SIGTERM. The daemon polls, evaluates eligibility, renews claims, supervises runs and runs the shepherd, whether or not a console is attached. Only one daemon runs per user and machine: a second one exits with `daemon already running (pid 4242)`.

At start, the daemon rebuilds missing ant records from the providers and git, wraps up the runs a restart interrupted, starts queued ants, handles the changes left from before, and evaluates the proposal list. Then each provider connection is polled on its interval. After each poll the daemon handles the changes it found, checks the PRs of its ants for merge or close, asks the shepherd about each PR that changed, and evaluates the proposals of that connection's repositories again. A proposal that was not in the list before raises the "new proposal" notification; after a start, the whole list raises one notification.

The daemon keeps its local state in `~/.anthill/anthill.db`. It starts without credentials or network: a provider it cannot reach shows as a failed poll in `anthill status` and raises the polling-failure notification.

### `anthill status`

Shows the daemon's health: its pid, version and uptime, then for each provider connection its last successful poll, its last error and whether polling is paused for the rate limit, the ants with an active run and their budget, the queued ants, each held claim with its lease expiry (or `parked`), and the shepherd's active rounds (spec queen-console, "Health and status"). Without a daemon it prints `daemon not running` and exits with 1.

### `anthill version`

Prints the Anthill version. `anthill --version` does the same.

## Proposals

### `anthill proposals`

Lists the proposals: the eligible items that are not hidden on this machine, in priority order, with their reference, title, target repository, type, priority, age and the runner an acceptance would use (spec queen-console, "Viewing proposals"). Eligibility is evaluated on fresh provider data. A tracker that cannot be read is reported as a warning on standard error, and its items are left out. The list is ordered by provider priority, then priority label, then age, oldest first (spec task-eligibility, "Proposal list"). Priority shows `P<n>` for a provider priority (Azure DevOps' Priority field; GitHub has none), otherwise the item's highest-ranked label from `priority-labels`, otherwise `-` (see [configuration](configuration.md)).

### `anthill explain <item>`

Shows every prerequisite of the item, met and unmet, with the reason of each unmet one, such as `dependencies-satisfied: unmet (dependency #10 is open)`. It also says when you declined or snoozed the item.

### `anthill accept <item>`

Accepts a proposal (spec queen-console, "Accepting and declining"). Anthill checks eligibility again on fresh data, claims the item, creates the ant branch and worktree, and starts the first run. When capacity is full, the ant is created `queued` and its run starts when a slot frees. An item that is no longer eligible is refused, with the unmet prerequisites, such as `no longer eligible: open`.

- `--runner <name>`: the runner to use (`claude-code`, `codex` or `opencode`) instead of the configured one.
- `--budget <minutes>`: the base budget instead of the configured one.
- `--base <branch>`: the base branch instead of the configured one.

### `anthill decline <item>`

Hides the proposal on this machine only. Colleagues' machines still propose it, and nothing is written to the provider. The item is proposed again when its title, description or acceptance criteria change, or when you un-hide it.

### `anthill snooze <item> <duration>`

Hides the proposal on this machine for a while: a duration such as `90m` or `2h`, or a number of days such as `3d`. It is proposed again when the snooze ends.

### `anthill unhide <item>`

Shows a declined or snoozed proposal again.

## Ants

### `anthill ants`

Lists the ants that are not final, oldest first, with their item, state, runner, elapsed and remaining budget, and branch (spec queen-console, "Monitoring ants"). The remaining budget is marked `(near limit)` once the ant was warned at its warning threshold, and the state is marked `(follow-up proposed)` while a follow-up proposal is open.

- `--all`: also list `done` and `abandoned` ants.

### `anthill ant <item>`

Shows the item's ant in full: its state, runner, branch, budget and PR, every transition with its time and reason, and every run with its kind, start, duration and outcome.

### `anthill follow <item>`

Streams the activity of the active run of the item's ant until the run ends, then prints how it ended, such as `run ended: stopped (stopped by the queen)`. Ctrl+C detaches: the ant is not affected. Lines are skipped, with a note, when the console cannot keep up, so following never slows the run down. An ant without an active run is refused.

### `anthill stop <item>`

Ends the ant's active run within 10 seconds, with its session saved, and keeps the claim. The ant becomes `stopped`.

### `anthill retry <item>`

Starts a new run for a `failed` or `stopped` ant. It resumes the ant's session in the same worktree, with the remaining budget. When capacity is full, the ant is queued.

### `anthill continue <item>`

Starts a new run for a `timed-out` ant with a new budget. Its commits go to the existing draft PR.

- `--budget <minutes>`: the new budget (default: the ant's base budget).

### `anthill abandon <item>`

Ends any active run, releases the claim and makes the ant `abandoned`. The worktree is kept until you clean it up.

### `anthill cleanup <item>`

Removes the worktree and the local branch of a `done` or `abandoned` ant (spec branching-policy, "Worktree cleanup"). Unpushed commits or uncommitted changes are not deleted without confirmation: the command fails and says what would be lost. The remote branch is deleted too when the ant's PR was merged and the provider kept the branch; after a closed PR, or for an ant that needed no change, it is kept.

- `--confirm`: also delete unpushed commits and uncommitted changes.

## Questions, extensions and follow-ups

### `anthill questions`

Lists the pending questions of the ants, with the item, where the question was asked, its age, marked `(stale)` after the question timeout, and its first line.

### `anthill answer <item> <text>...`

Answers the pending question of the item's ant (spec queen-console, "Answer from the console"). The answer is posted as your own comment where the question was asked: as a reply in the question's thread on a threaded provider, and otherwise as a comment that quotes the question. Then a new run resumes the ant's session with the answer. The words after the item are the answer; `-` reads it from standard input instead. An answer that holds an `/anthill` command is refused.

### `anthill extensions`

Lists the open extension requests, with the minutes asked, the ant's remaining budget and the agent's reason.

### `anthill extension approve <item>`

Grants the minutes the ant asked for.

### `anthill extension reduce <item> <minutes>`

Grants fewer minutes than the ant asked for.

### `anthill extension deny <item>`

Grants no extension. The ant wraps up within its remaining time.

### `anthill follow-ups`

Lists the follow-up proposals for you to decide (when `auto-follow-up` is off), with the ant's PR and the stored changes the follow-up run would get.

### `anthill follow-up accept <item>`

Starts the follow-up run with the stored changes, or queues it while capacity is full.

### `anthill follow-up decline <item>`

Declines the follow-up. The changes are kept, and the next actionable change brings a new proposal for all of them.

## Usage

### `anthill usage`

Shows the duration, tokens and cost of each ant run and shepherd round started today, with totals (spec queen-console, "Usage reporting"). A value the runner did not report is shown as `unknown` and left out of the total, and the total says how many runs or rounds it misses. Anthill enforces no usage limit.

- `--date <YYYY-MM-DD>`: report that day instead of today.
- `--item <item>`: report every run of the item's ants and every round on their PRs instead.

## Started by Anthill

These commands are not for you to run: Anthill or an agent's runner starts them.

### `anthill mcp --ant <token>`

Serves one run's ant tools to its agent over stdio, relaying every call to the daemon (see [ant-tools.md](ant-tools.md)). Anthill adds it to the agent's configuration when it starts a run.

- `--ant <token>`: the run's tool token.

Two more are hidden from `anthill --help`:

- `anthill hook post-tool-use`: the runner's hook that hands queued notices to the agent.
- `anthill runner-smoke`: starts one agent run for the manual checks of [runners.md](runners.md).

## Example session

A morning with two opted-in items. Your typing follows `$`.

```console
$ anthill daemon &
$ anthill status
daemon: running (pid 4242, anthill 0.4.0)
uptime: 2m0s

providers:
  github: last successful poll 2026-11-02 09:01 CET

active runs:
  none

queued ants:
  none

held claims:
  none

active rounds:
  none

$ anthill proposals
ITEM  TITLE                        REPOSITORY  TYPE  PRIORITY  AGE   RUNNER
#14   Add CSV export               acme/shop   task  urgent    1d4h  claude-code
#12   Fix login timeout on Safari  acme/shop   bug   -         3d2h  claude-code

$ anthill explain #13
#13 Rename the settings page
opted-in: met
open: met
dependencies-satisfied: unmet (dependency #10 is open)
is-leaf: met
not-claimed: met
repository-available: met
no-active-work: met
no-pending-question: met
has-description: met
not-excluded: met

$ anthill decline #14
declined #14 on this machine

$ anthill accept #12 --budget 90
accepted #12: the ant is running, with 1h30m of budget and runner claude-code, on branch ant/gh-12-fix-login-timeout-on-safari

$ anthill follow #12
reading src/auth/session.ts
running npm test -- session
^C
detached; the ant goes on

$ anthill questions
ITEM  ASKED ON  AGE  QUESTION
#12   #12       5m   Does "timeout" mean the idle timeout or the absolute session length?

$ anthill answer #12 The idle timeout. The absolute length stays 12 hours.
answered the question of the ant for #12: https://github.com/acme/shop/issues/12#issuecomment-2001

$ anthill extensions
ITEM  MINUTES  REMAINING  REASON
#12   30       8m         The Safari tests need a second pass after the fix.

$ anthill extension reduce #12 20
reduced the extension of the ant for #12: 20 minutes granted

$ anthill ants
ITEM  STATE      RUNNER       ELAPSED  REMAINING  BRANCH
#12   succeeded  claude-code  1h14m    36m        ant/gh-12-fix-login-timeout-on-safari

$ anthill usage
usage on 2026-11-02:
STARTED               ITEM  KIND      DURATION  TOKENS  COST
2026-11-02 09:05 CET  #12   task run  52m       184000  $3.10
2026-11-02 10:02 CET  #12   task run  22m       61000   $1.05
2026-11-02 10:30 CET  #57   round     6m        unknown  unknown
total: 1h20m, 245000 tokens (1 run or round with unknown tokens, missing from the total), $4.15 (1 run or round with unknown cost, missing from the total)
```
