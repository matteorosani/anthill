# Agent runners

Anthill starts the agent of each run through a runner: Claude Code, Codex or OpenCode (spec ant-execution, design D6). This page says how each runner is started and why, what it supports, and how to check it by hand against the real CLI. The adapters' tests use recorded output only; the manual smoke runs below are what proves the recordings still match the real CLIs.

## What every runner shares

- **Process tree.** The agent runs in a process tree of its own: a process group on Unix, a Job Object on Windows (`internal/adapters/internal/agentproc`). Stopping a run asks the tree to end (SIGTERM, or CTRL_BREAK on Windows), waits for the grace period, then kills the whole tree. A run that ends on its own takes what it started with it.
- **Windows console.** When Anthill has no console window of its own, as when it runs in the background, the agent starts with `CREATE_NO_WINDOW`, so no console window opens on the developer's desktop. CTRL_BREAK then cannot reach the agent, so on Windows the grace period just passes before the kill. The agent's session is saved as it goes, so the kill does not lose it.
- **Daemon crash.** On Windows, the Job Object also kills the agents when the daemon itself ends, even by a crash. On Unix, the process group outlives a crashed daemon (see the open questions of the design).
- **A guard, not a sandbox** (design D4). A process the agent starts in a group or session of its own, with `setsid` for example, is out of reach of a stop on Unix. The agent runs as the developer's OS user.
- **No provider credentials.** The agent's environment has provider tokens removed and the git credential helper disabled (`domain.AgentEnv`), so a stray `git push` fails.
- **Telemetry credential.** When telemetry export is on, the agent gets the OTLP endpoint and its headers in `OTEL_EXPORTER_OTLP_HEADERS`, so it can export its own telemetry to the same endpoint. Those headers often hold an authentication token. The agent can read it: it is a credential for telemetry only, never a provider or git credential (design D18).
- **Prompt logging off.** Where a runner exports telemetry, its prompt and tool detail logging are always off (spec observability, "Content is never exported").

## Claude Code

| Capability | Supported | How |
|---|---|---|
| Resume | yes | `--resume <session id>` |
| Live input | yes | streaming JSON on standard input |
| Hooks | yes | `PreToolUse` refuses commands, `PostToolUse` adds notices |
| Usage | yes | the `result` events |
| Telemetry | yes | `CLAUDE_CODE_ENABLE_TELEMETRY` and the `OTEL_*` variables |

### How a run is started

In the worktree, Anthill runs:

```
claude -p --input-format stream-json --output-format stream-json --verbose \
  --mcp-config <run dir>/mcp.json --strict-mcp-config \
  --settings <run dir>/settings.json \
  --permission-mode acceptEdits --allowedTools Bash,PowerShell,Edit,Write,Read,Glob,Grep,mcp__anthill \
  [--resume <session id>]
```

- **The brief.** The brief, then each live input, is written to standard input as one JSON line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]},"parent_tool_use_id":null}`. Nothing of the brief goes on the command line. On Windows, cmd.exe would mangle it there.
- **When the run ends on its own.** A turn ends with a `result` event. A message sent while Claude Code works can be folded into the running turn, which then answers it with the brief in one `result` (smoke check C3), or get a turn and a `result` of its own (C6). So Anthill does not count results: once a `result` reports `queued_turn_count` 0, Anthill closes standard input. Claude Code still answers every message it has already read, then exits. In the smoke runs, `queued_turn_count` was 0 even when a message waited for a turn of its own; the input then closes after the first `result`, and live input reaches the agent only until then. A `Send` after that fails, and the message goes to the next means of delivery (design D5). A Claude Code that does not report `queued_turn_count` is handled as before: the input closes once every message written has its own `result`.
- **Background tasks.** Claude Code does not exit while a command it started in the background is still going, even after its last `result` and with standard input closed. Such a run ends only when that command ends or Anthill stops the run (smoke check C7 on Windows).
- **The run directory.** `<run dir>` is a temporary directory outside the worktree, so the agent cannot commit it. It holds the two files below and is removed when the run ends. The settings file holds the run's tool token, which therefore never appears in the brief.
  - `mcp.json` names one stdio server, `anthill`, that runs `anthill mcp --ant <token>`.
  - `settings.json` installs the hooks:
    - `PreToolUse` on `Bash` and `PowerShell` (matcher `Bash|PowerShell`) runs `"<anthill>" hook pre-tool-use`. It refuses `git push`, `gh` and `az` with exit code 2 and a message naming the ant tool to use, such as `open_pull_request` for `gh pr create`. It reads a `PowerShell` command with PowerShell's quoting, where a backslash is an ordinary character and the backtick escapes, and also follows `Invoke-Expression` and `Start-Process`.
    - `PostToolUse` on every tool runs `"<anthill>" hook post-tool-use --ant "<token>"`. It takes the run's queued notices from the daemon (`ant.notices`) and returns them as `additionalContext`. With no notice, or when the daemon cannot be reached, it prints nothing and exits 0, so it never blocks the agent.
- **`--strict-mcp-config`.** This leaves out the developer's own MCP servers. A GitHub MCP server holding a token would otherwise give the agent provider access, which design D4 rules out.
- **`--permission-mode acceptEdits` with `--allowedTools`.** This is the most restrictive headless setting that still lets the agent edit files and run the tests in its worktree (design, Risks). `acceptEdits` alone would still ask before each Bash command, and nobody answers a headless run. `--allowedTools` lets only the listed tools run without a prompt. On Windows, Claude Code has a `PowerShell` tool besides `Bash`, and the agent often chooses it. Without `PowerShell` in the list, Claude Code asks before every PowerShell command that is not read-only, so the agent could not build, test or commit with it (smoke check C2). The `PreToolUse` hook still refuses `git push`, `gh` and `az`. `bypassPermissions` was not used: it would also let through every tool not listed.
- **The model.** Anthill sets no model. Claude Code uses the developer's own default.
- **Session id.** The session id is read from the `system` `init` event, and also from every later event that carries one.
- **Usage.** Tokens are every token of each `result` event: input, output, cache creation and cache reads. They are added up over the run's results, so cached tokens count, as on every runner. The cost is the `total_cost_usd` of the last result, on the assumption that it is cumulative for the process (smoke check C6). A run stopped before its first `result` has unknown usage, although it used tokens.
- **Telemetry variables.**
  - Always: `OTEL_LOG_USER_PROMPTS=0` and `OTEL_LOG_TOOL_DETAILS=0`.
  - With export on, also: `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_METRICS_EXPORTER=otlp`, `OTEL_LOGS_EXPORTER=otlp`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_RESOURCE_ATTRIBUTES` (`anthill.item`, `anthill.ant_id`, `anthill.instance`) and `TRACEPARENT`.
- **Hook commands.** The anthill path in the hook commands is in double quotes, which a POSIX shell and cmd.exe both accept. Smoke check C4 confirms this on Windows.

### What is not certain yet

Most tests run against recorded output written from Claude Code's documentation. The test of a message folded into the running turn replays output captured on Windows (issue #146). The smoke runs check these points; record the answers in the table at the end.

Confirmed on Windows with Claude Code 2.1.284, 2.1.310 and 2.1.318 (issue #146):

- A user message with content blocks and `parent_tool_use_id: null`, without a `session_id`, is accepted on standard input (C1).
- The `PostToolUse` matcher `*` runs the hook after every tool, `additionalContext` reaches the agent, and the hooks run with the quoted path (C4).
- `--resume` keeps the session id (C5).
- A stop ends the whole tree, Claude Code's own commands included (C7).
- The `PreToolUse` hook refuses `gh pr create` and `git push` run with the `Bash` tool and with the `PowerShell` tool, with the messages naming `open_pull_request` and `push` (C2).
- A message sent during a turn that calls tools can be folded into it, with one `result` for both messages and `queued_turn_count` 0 (C3). Since task 13.7, the run then ends on its own.
- A message sent while Claude Code writes an answer without tools gets a turn and a `result` of its own. The first `result` still reports `queued_turn_count` 0; the input closes after it, and Claude Code answers the message anyway, then exits (C6).
- `total_cost_usd` is cumulative over the process: it grows from one `result` to the next in the same process (C6).

Still open:

1. Whether any version reports a waiting message in `queued_turn_count`. None did in the smoke runs. The adapter keeps the input open while it is above 0.
2. On macOS and Linux, all the checks: they have only been run on Windows.

## Codex

| Capability | Supported | How |
|---|---|---|
| Resume | yes | `codex exec … resume <session id> -` |
| Live input | no | messages come with the next ant-tool result |
| Hooks | no | a stray `git push` fails for lack of credentials |
| Usage | tokens only | the `turn.completed` events; the cost stays unknown |
| Telemetry | yes | Codex's `[otel]` settings, set with `-c` |

### How a run is started

In the worktree, Anthill runs:

```
codex exec --json --sandbox workspace-write --skip-git-repo-check --add-dir <git directory> \
  -c sandbox_workspace_write.network_access=true -c 'approval_policy="never"' \
  -c 'mcp_servers={}' -c 'mcp_servers.anthill.command="<anthill>"' \
  -c 'mcp_servers.anthill.args=["mcp","--ant","<token>"]' \
  -c otel.log_user_prompt=false [-c 'otel.exporter=…'] \
  [resume <session id>] -
```

- **The brief.** The final `-` makes Codex read the prompt from standard input. Anthill writes the brief there and closes it, so the brief is never on the command line.
- **`--sandbox workspace-write`.** This is the most restrictive headless sandbox that still lets the agent edit files and run commands in its worktree (design, Risks).
- **`--add-dir <git directory>`.** A worktree's commits go to the clone's shared git directory (`git rev-parse --git-common-dir`), which lies outside the worktree. Without this flag, every `git commit` fails in the sandbox.
- **`sandbox_workspace_write.network_access=true`.** Builds and tests need to fetch dependencies.
- **`approval_policy="never"`.** Nobody answers a headless run.
- **`mcp_servers={}`, then `mcp_servers.anthill`.** The first override drops the developer's own MCP servers. A server holding a token would give the agent provider access, which design D4 rules out. The next two overrides add only the ant tools server. Values are TOML, and Anthill writes them as JSON strings and arrays, which are valid TOML.
- **Session id.** It is the `thread_id` of the `thread.started` event.
- **Usage.** Tokens are `input_tokens + output_tokens` of each `turn.completed` event, added up; `input_tokens` already include the cached ones. Codex reports no cost, so the cost is recorded as unknown.
- **Telemetry.**
  - Always: `otel.log_user_prompt=false`.
  - With export on, also an exporter: `otel.exporter={ otlp-http = { endpoint = "…", protocol = "binary" } }` (`otlp-grpc`, or `protocol = "json"`, for those OTLP protocols), plus the standard variables `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_RESOURCE_ATTRIBUTES` and `TRACEPARENT`.
  - The headers go only in the environment, never on the command line, where other users of the machine could see them in the process list.
- **The model.** Anthill sets no model. Codex uses the developer's own default.

### What is not certain yet

The recorded output in the tests is written from Codex's documentation, not captured from a real run. The smoke checks confirm:

1. The global `exec` options go before `resume <id>`, and `-` reads the prompt from standard input for `resume` too (X1, X4).
2. `--add-dir` makes the git directory writable, so `git commit` works in a linked worktree (X2).
3. `-c 'mcp_servers={}'` clears the developer's MCP servers, and the later `mcp_servers.anthill.*` overrides still add the ant tools server (X1, X5).
4. The JSON event names and fields: `thread.started.thread_id` and `turn.completed.usage` (X1).
5. `-c sandbox_workspace_write.network_access=true` and `-c approval_policy="never"` are accepted with `exec` (X2).
6. The `[otel]` exporter format, and whether the exporter reads `OTEL_EXPORTER_OTLP_HEADERS` and `OTEL_RESOURCE_ATTRIBUTES`. This needs an OTLP endpoint and is not part of the smoke checks below; check it when the telemetry export of task group 18 can be switched on.
7. On Windows, whether the `workspace-write` sandbox is enforced at all (X2).

## OpenCode

| Capability | Supported | How |
|---|---|---|
| Resume | yes | `opencode run --session <session id>` |
| Live input | no | messages come with the next ant-tool result |
| Hooks | no | permission rules deny `git push`, `gh` and `az`, without naming the tool to use |
| Usage | yes | the `step_finish` events |
| Telemetry | no | the spec's "runner without telemetry support": only Anthill's own telemetry is exported |

### How a run is started

In the worktree, with `OPENCODE_CONFIG=<run dir>/opencode.json` in its environment, Anthill runs:

```
opencode run --format json [--session <session id>]
```

- **The brief.** Anthill writes the brief to standard input and closes it. There is no message argument, so the brief is never on the command line.
- **The run's configuration.** `<run dir>` is a temporary directory outside the worktree, removed when the run ends. OpenCode merges `opencode.json` over the developer's global configuration. A developer's own `OPENCODE_CONFIG` is replaced for the run. The file holds two things:
  - **`mcp.anthill`**: the ant tools server, as a local server running `anthill mcp --ant <token>`.
  - **`permission`**: `edit: allow`, and for `bash`, `*: allow` with `git push`, `git push *`, `gh`, `gh *`, `az` and `az *` set to `deny`. A headless run cannot answer a prompt, so everything else is allowed. The deny rules are a layer on top of the missing credentials (design D4). They match only commands that start that way, and their refusal does not name the ant tool to use, so they do not count as hooks.
- **Gap: the developer's MCP servers.** The developer's own MCP servers stay enabled: a merged configuration can only switch off servers by name, and Anthill does not know their names. A GitHub MCP server holding a token would give the agent provider access. Developers who use OpenCode with such a server should switch it off in their configuration.
- **Session id.** It is the `sessionID` that every event carries.
- **Usage.** Tokens are `input + output + reasoning + cache.read + cache.write` of each `step_finish` event, added up. The cost is the sum of their `cost`.
- **Possible later work.** An OpenCode plugin with a `tool.execute.before` hook could refuse commands with a message naming the ant tool, and add notices as context, which would give OpenCode hooks. It is not built.
- **The model.** Anthill sets no model. OpenCode uses the developer's own default.

### What is not certain yet

The recorded output in the tests is written from OpenCode's documentation, not captured from a real run. The smoke checks confirm:

1. `opencode run` without a message argument reads the prompt from standard input (O1).
2. `--format json` prints one JSON event per line. Each event carries `sessionID`, and `step_finish` events carry `part.tokens` and `part.cost` (O1).
3. `OPENCODE_CONFIG` is merged with the global configuration, and the `mcp` and `permission` keys work as written (O1, O2).
4. The bash permission patterns `git push *`, `gh *` and `az *` match as globs, and a denied command is refused without a prompt (O2).
5. `--session <id>` continues the session (O4).
6. The exit code of a run that fails, for example without credentials (O1 and the notes).

## Manual smoke runs

The hidden command `anthill runner-smoke` starts one run of the real agent in the current directory, through the same adapter the daemon uses. It stands in for the daemon:

- It takes the daemon lock, so stop the daemon first.
- It serves one ant tool, `smoke_ping`, which answers `pong`.
- It queues the notices given with `--notice`. As with the ant tools, the `PostToolUse` hook takes them where the runner has hooks; otherwise they come with the next tool result, as text starting with `Anthill notice:`.
- It prints its own report on standard output, as lines starting with `smoke:`, and the agent's JSON events on standard error.
- `--send` sends live input. Codex and OpenCode have none, so the report says `live input refused`.
- Ctrl+C stops the run with a 10 second grace period.

### Before you start

1. Install the CLI under test and log in:
   - Claude Code: `claude --version` prints a version, and `claude -p "say hi"` answers.
   - Codex: `codex --version` prints a version, and `codex exec "say hi"` answers.
   - OpenCode: `opencode --version` prints a version, and `opencode run "say hi"` answers.
2. Build Anthill from the branch under test:
   - macOS or Linux: `make build`; the binary is `bin/anthill`.
   - Windows (PowerShell): `go build -o bin\anthill.exe .\cmd\anthill`.
   - Windows, from Linux or the dev container: `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/anthill.exe ./cmd/anthill`.
3. Stop the Anthill daemon if it runs.
4. Make a throwaway repository and go into it:

   ```bash
   mkdir /tmp/smoke && cd /tmp/smoke && git init && echo "# smoke" > README.md && git add . && git commit -m init
   ```

   On Windows, use `mkdir $env:TEMP\smoke; cd $env:TEMP\smoke` and the same git commands.

Below, `anthill` means the path of the binary you built, for example `~/src/anthill/bin/anthill` or `C:\src\anthill\bin\anthill.exe`.

To keep a record, save the two outputs to files, for example `> c1.out.txt 2> c1.events.jsonl`. On Windows, do this in `cmd.exe` or PowerShell 7 (`pwsh`), not in Windows PowerShell 5.1: it saves the files as UTF-16 and wraps each line of standard error in an error record, which breaks the JSON lines.

### Claude Code

#### C1: a run starts, calls a tool, and ends on its own

```bash
anthill runner-smoke --runner claude-code "Call the smoke_ping tool once, then reply with the word it returned."
```

Expected:

- `smoke: run started with claude-code in /tmp/smoke`.
- `smoke: session id: <a UUID>` within a few seconds.
- `smoke: the agent called smoke_ping`.
- On standard error, a `system` `init` event whose `mcp_servers` lists `anthill` as `connected`, and whose `tools` include `mcp__anthill__smoke_ping`.
- The process ends without Ctrl+C, with `smoke: run ended: exit code 0, crashed false`.
- `smoke: usage: <n> tokens, <x> USD`, both known.

Write down the session id for C5.

#### C2: the redirect hooks refuse `gh` and `git push`

```bash
anthill runner-smoke --runner claude-code "Run the shell command 'gh pr create --fill' and then 'git push origin HEAD'. Report each command's output exactly."
```

Expected:

- On standard error, the tool results of both calls are errors. On Windows, the agent may use the `PowerShell` tool instead of `Bash`; the hook refuses both.
- The `gh` one says `anthill: gh is disabled for agents; call the open_pull_request tool instead`.
- The `git push` one says `anthill: git push is disabled for agents; call the push tool instead`.
- The agent's last message reports both refusals.
- `gh` did not run, so no pull request was created. `git log origin/HEAD` fails, because the repository has no remote.

#### C3: live input

```bash
anthill runner-smoke --runner claude-code --send "Also tell me what 6 times 7 is." --send-after 5s \
  "Run 'sleep 20' in the shell, then say done."
```

On Windows, use `Start-Sleep -Seconds 20` in the brief.

Expected:

- `smoke: live input sent` about 5 seconds after the start.
- On standard error, one or two `result` events. Claude Code may fold the message into the running turn and answer both in one `result`, or answer the message in a turn of its own. Note which, and the `queued_turn_count` of each `result`.
- The process then ends on its own with exit code 0.

If it does not end within a minute of the last result, press Ctrl+C, and record it as a failure.

#### C4: notices reach the agent through the hook

```bash
anthill runner-smoke --runner claude-code --notice "The secret word is anthill-42." \
  "Run 'echo hello' in the shell. Then tell me any Anthill notice you received, word for word."
```

Expected:

- `smoke: the hook took 1 notice(s)` after the `echo` call.
- The agent's last message quotes `The secret word is anthill-42.`

On Windows, this also checks that the hook commands run.

#### C5: resume

```bash
anthill runner-smoke --runner claude-code --resume <session id from C1> "What word did the tool return earlier? Answer with that word only."
```

Expected:

- The agent answers `pong` without calling the tool again.
- `smoke: session id:` shows the session id. Note whether it is the same as in C1.

#### C6: usage and cost

C3 often gives one `result`. For two, send the message while the agent writes an answer that needs no tool:

```bash
anthill runner-smoke --runner claude-code --send "Now say, in one sentence, what 6 times 7 is." --send-after 2s \
  "Without using any tools, write a story of about 500 words about an ant colony that learns to build bridges."
```

Expected: two `result` events, the second answering 42. Its `total_cost_usd` is not lower than the first's if the cost is cumulative. The cost printed by `smoke: usage:` is the last one. The run ends on its own.

#### C7: stop leaves no process behind

```bash
anthill runner-smoke --runner claude-code --stop-after 30s \
  "Run exactly this command with the Bash tool, in the foreground: ping -n 300 127.0.0.1 > /dev/null. Wait for it to finish."
```

On macOS and Linux, use `ping -c 300 127.0.0.1`. The brief does not use `sleep`: Claude Code may refuse a long `sleep` or `Start-Sleep`, or run it in the background, and the check then has no command to stop.

Expected:

- `smoke: stopping the run (--stop-after), grace 10s`.
- Within about 10 seconds, `smoke: run ended: exit code -1, crashed true`.
- Afterwards, no agent process is left:
  - macOS or Linux: `ps -ef | grep -E 'claude -p|ping' | grep -v grep` prints nothing.
  - Windows: in a second `cmd.exe` window, list the processes before, during and after the run, and compare:

    ```bat
    powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { $_.Name -match 'claude|bash|anthill|ping' } | Select-Object ProcessId,ParentProcessId,Name,CommandLine | Format-Table -AutoSize | Out-String -Width 400"
    ```

    During the run, the list shows `claude -p …` with `anthill mcp` and `bash.exe` under it, and `PING.EXE` under `bash.exe`. After it, none of them is left. A Claude Code window or desktop app of your own shows up in every list.

Also run C7 with Ctrl+C instead of `--stop-after`: the output and the result are the same, with `(interrupted)`.

#### Results

Copy this table into the pull request or issue that records the smoke run:

| Check | macOS/Linux | Windows | Notes |
|---|---|---|---|
| C1 run, tool, own end |  |  |  |
| C2 hooks refuse gh, git push |  |  |  |
| C3 live input (results: one or two? `queued_turn_count`?) |  |  |  |
| C4 notice through the hook |  |  |  |
| C5 resume (same session id?) |  |  |  |
| C6 cost cumulative? |  |  |  |
| C7 no process after stop |  |  |  |

The results so far are recorded in issue #146.

### Codex

Run these in a throwaway repository with a linked worktree, as Anthill uses them, so that X2 checks the commit into the shared git directory:

```bash
cd /tmp/smoke && git worktree add ../smoke-wt -b ant/smoke && cd ../smoke-wt
```

On Windows: `git worktree add ..\smoke-wt -b ant/smoke; cd ..\smoke-wt`.

#### X1: a run starts, calls a tool, and ends on its own

```bash
anthill runner-smoke --runner codex "Call the smoke_ping tool once, then reply with the word it returned."
```

Expected:

- `smoke: run started with codex`.
- `smoke: session id: <an id>`.
- `smoke: the agent called smoke_ping`.
- On standard error, JSON events: `thread.started` with a `thread_id`, items, and a `turn.completed` with `usage`.
- The run ends on its own with `exit code 0, crashed false`.
- `smoke: usage: <n> tokens, unknown USD`.

If Codex lists MCP servers when it starts, only `anthill` appears. Write down the session id for X4.

#### X2: the sandbox lets the agent commit and fetch, without prompts

```bash
anthill runner-smoke --runner codex "Create a file hello.txt containing hi, commit it with git, then run 'git ls-remote https://github.com/git/git HEAD' and report its output."
```

Expected:

- The run ends with exit code 0, and no approval prompt appears.
- `git log -1 --format=%s` in the worktree shows the agent's commit.
- The agent reports a commit hash from `git ls-remote`, so the sandbox has network access.

On Windows, also note whether Codex reports that the sandbox is not enforced.

#### X3: a stray git push fails for lack of credentials

First give the throwaway repository a remote that needs credentials, such as a private repository you own: `git remote add origin https://github.com/<you>/<private-repo>.git`. Then:

```bash
anthill runner-smoke --runner codex "Run 'git push origin HEAD' and report its output exactly."
```

Expected: the push fails, either because the sandbox blocks the network for git or because there are no credentials (`could not read Username` or `terminal prompts disabled`). Nothing reaches the remote. Codex has no hooks, so the message does not name the push tool.

#### X4: resume

```bash
anthill runner-smoke --runner codex --resume <session id from X1> "What word did the tool return earlier? Answer with that word only."
```

Expected: the agent answers `pong` without calling the tool again, and `smoke: session id:` shows the X1 session id.

#### X5: a notice comes with the next tool result

```bash
anthill runner-smoke --runner codex --notice "The secret word is anthill-42." \
  "Call the smoke_ping tool, then tell me any Anthill notice in its result, word for word."
```

Expected:

- `smoke: the tool result carries 1 notice(s)`.
- The agent quotes `The secret word is anthill-42.`

#### X6: stop leaves no process behind

```bash
anthill runner-smoke --runner codex --stop-after 15s "Run 'sleep 300' in the shell and wait for it."
```

Expected:

- `smoke: stopping the run (--stop-after), grace 10s`, then `exit code -1, crashed true` within about 10 seconds.
- No process is left:
  - macOS or Linux: `ps -ef | grep -E 'codex|sleep 300' | grep -v grep` prints nothing.
  - Windows: `Get-Process | Where-Object { $_.ProcessName -match 'codex|node' }` shows none from the run.

#### Results

| Check | macOS/Linux | Windows | Notes |
|---|---|---|---|
| X1 run, tool, own end; only the anthill MCP server |  |  |  |
| X2 commit and network in the sandbox, no prompt |  |  |  |
| X3 stray git push fails |  |  |  |
| X4 resume |  |  |  |
| X5 notice with the tool result |  |  |  |
| X6 no process after stop |  |  |  |

### OpenCode

Use a fresh throwaway repository, made as in "Before you start" (for example `/tmp/smoke-opencode`).

#### O1: a run starts, calls a tool, and ends on its own

```bash
anthill runner-smoke --runner opencode "Call the smoke_ping tool once, then reply with the word it returned."
```

Expected:

- `smoke: run started with opencode`.
- `smoke: session id: ses_…`.
- `smoke: the agent called smoke_ping`.
- On standard error, JSON events carrying `sessionID`, among them `step_finish` events with `tokens` and `cost`.
- The run ends on its own with `exit code 0, crashed false`, and `smoke: usage:` shows known tokens and cost.

If OpenCode reports that a message is required, or the agent answers without knowing the brief, OpenCode does not read the prompt from standard input: note it, because the adapter must then pass the brief as an argument. Write down the session id for O4.

Also run it once logged out of the model provider, and note the exit code.

#### O2: the permission rules deny gh and git push

```bash
anthill runner-smoke --runner opencode "Run the shell command 'gh pr create --fill' and then 'git push origin HEAD'. Report each command's output exactly."
```

Expected: both commands are refused by OpenCode's permission rules without a prompt, and the agent reports the refusals. The refusal does not name an ant tool; that is expected.

#### O3: a stray git push behind cd still fails

```bash
anthill runner-smoke --runner opencode "Run 'cd . && git push origin HEAD' and report its output exactly."
```

Expected: the deny rule does not match, so the command runs and fails for lack of credentials or a remote. Nothing reaches a remote.

#### O4: resume

```bash
anthill runner-smoke --runner opencode --resume <session id from O1> "What word did the tool return earlier? Answer with that word only."
```

Expected: the agent answers `pong` without calling the tool again, and the session id is the same.

#### O5: a notice comes with the next tool result

```bash
anthill runner-smoke --runner opencode --notice "The secret word is anthill-42." \
  "Call the smoke_ping tool, then tell me any Anthill notice in its result, word for word."
```

Expected: `smoke: the tool result carries 1 notice(s)`, and the agent quotes `The secret word is anthill-42.`

#### O6: stop leaves no process behind

```bash
anthill runner-smoke --runner opencode --stop-after 15s "Run 'sleep 300' in the shell and wait for it."
```

Expected:

- `exit code -1, crashed true` within about 10 seconds of the stop.
- No process is left:
  - macOS or Linux: `ps -ef | grep -E 'opencode|sleep 300' | grep -v grep` prints nothing.
  - Windows: `Get-Process | Where-Object { $_.ProcessName -match 'opencode|bun|node' }` shows none from the run.

#### Results

| Check | macOS/Linux | Windows | Notes |
|---|---|---|---|
| O1 run, tool, own end; brief from standard input |  |  |  |
| O2 permission rules deny gh, git push |  |  |  |
| O3 stray push behind cd fails |  |  |  |
| O4 resume |  |  |  |
| O5 notice with the tool result |  |  |  |
| O6 no process after stop |  |  |  |
