# Agent runners

Anthill starts the agent of each run through a runner: Claude Code, Codex or OpenCode (spec ant-execution, design D6). This page says how each runner is started and why, what it supports, and how to check it by hand against the real CLI. The adapters' tests use recorded output only; the manual smoke runs below are what proves the recordings still match the real CLIs.

## What every runner shares

- **Process tree.** The agent runs in a process tree of its own: a process group on Unix, a Job Object on Windows (`internal/adapters/internal/agentproc`). Stopping a run asks the tree to end (SIGTERM, or CTRL_BREAK on Windows), waits for the grace period, then kills the whole tree. A run that ends on its own takes what it started with it.
- **Windows console.** When Anthill has no console window of its own, as when it runs in the background, the agent starts with `CREATE_NO_WINDOW`, so no console window opens on the developer's desktop. CTRL_BREAK then cannot reach the agent, so on Windows the grace period just passes before the kill. The agent's session is saved as it goes, so the kill does not lose it.
- **Daemon crash.** On Windows, the Job Object also kills the agents when the daemon itself ends, even by a crash. On Unix, the process group outlives a crashed daemon (see the open questions of the design).
- **A guard, not a sandbox** (design D4). A process the agent starts in a group or session of its own, with `setsid` for example, is out of reach of a stop on Unix. The agent runs as the developer's OS user.
- **No provider credentials.** The agent's environment has provider tokens removed and the git credential helper disabled (`domain.AgentEnv`), so a stray `git push` fails.
- **Telemetry credential.** When telemetry export is on, the agent gets the OTLP endpoint and its headers in `OTEL_EXPORTER_OTLP_HEADERS`, so it can export its own telemetry to the same endpoint. Those headers often hold an authentication token. The agent can read it: it is a credential for telemetry only, never a provider or git credential (design D18).
- **Prompt logging off.** Prompt and tool detail logging are always off (spec observability, "Content is never exported").

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
  --permission-mode acceptEdits --allowedTools Bash,Edit,Write,Read,Glob,Grep,mcp__anthill \
  [--resume <session id>]
```

- **The brief.** The brief, then each live input, is written to standard input as one JSON line: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]},"parent_tool_use_id":null}`. Nothing of the brief goes on the command line. On Windows, cmd.exe would mangle it there.
- **When the run ends on its own.** Each answered message ends with a `result` event. Once every message written has its result, Anthill closes standard input, and Claude Code exits. A `Send` after that fails, and the message goes to the next means of delivery (design D5).
- **The run directory.** `<run dir>` is a temporary directory outside the worktree, so the agent cannot commit it. It holds the two files below and is removed when the run ends. The settings file holds the run's tool token, which therefore never appears in the brief.
  - `mcp.json` names one stdio server, `anthill`, that runs `anthill mcp --ant <token>`.
  - `settings.json` installs the hooks:
    - `PreToolUse` on `Bash` runs `"<anthill>" hook pre-tool-use`. It refuses `git push`, `gh` and `az` with exit code 2 and a message naming the ant tool to use, such as `open_pull_request` for `gh pr create`.
    - `PostToolUse` on every tool runs `"<anthill>" hook post-tool-use --ant "<token>"`. It takes the run's queued notices from the daemon (`ant.notices`) and returns them as `additionalContext`. With no notice, or when the daemon cannot be reached, it prints nothing and exits 0, so it never blocks the agent.
- **`--strict-mcp-config`.** This leaves out the developer's own MCP servers. A GitHub MCP server holding a token would otherwise give the agent provider access, which design D4 rules out.
- **`--permission-mode acceptEdits` with `--allowedTools`.** This is the most restrictive headless setting that still lets the agent edit files and run the tests in its worktree (design, Risks). `acceptEdits` alone would still ask before each Bash command, and nobody answers a headless run. `--allowedTools` lets only the listed tools run without a prompt. The `PreToolUse` hook still refuses `git push`, `gh` and `az`. `bypassPermissions` was not used: it would also let through every tool not listed.
- **The model.** Anthill sets no model. Claude Code uses the developer's own default.
- **Session id.** The session id is read from the `system` `init` event, and also from every later event that carries one.
- **Usage.** Tokens are every token of each `result` event: input, output, cache creation and cache reads. They are added up over the run's results, so cached tokens count, as on every runner. The cost is the `total_cost_usd` of the last result, on the assumption that it is cumulative for the process (smoke check C6).
- **Telemetry variables.**
  - Always: `OTEL_LOG_USER_PROMPTS=0` and `OTEL_LOG_TOOL_DETAILS=0`.
  - With export on, also: `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_METRICS_EXPORTER=otlp`, `OTEL_LOGS_EXPORTER=otlp`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_RESOURCE_ATTRIBUTES` (`anthill.item`, `anthill.ant_id`, `anthill.instance`) and `TRACEPARENT`.
- **Hook commands.** The anthill path in the hook commands is in double quotes, which a POSIX shell and cmd.exe both accept. Smoke check C4 confirms this on Windows.

### What is not certain yet

The tests run against recorded output written from Claude Code's documentation, not captured from a real run. The smoke run checks these points; record the answers in the table at the end.

1. A user message with content blocks and `parent_tool_use_id: null`, without a `session_id`, is accepted on standard input (C1).
2. A message sent while Claude Code is working gets its own `result`, and Claude Code exits once standard input is closed after the last one (C3). If several queued messages are answered by a single `result`, the run would not end on its own: it would only end when Anthill stops it.
3. The `PostToolUse` matcher `*` runs the hook after every tool, and `additionalContext` reaches the agent (C4).
4. `--resume` keeps the session id, or reports a new one in its `init` event; either way the run reports what Claude Code says (C5).
5. `total_cost_usd` is cumulative over the process (C6).
6. On Windows, the hooks run with the quoted path (C4).

## Codex and OpenCode

Task group 13 adds them next (tasks 13.4 and 13.5).

## Manual smoke run: Claude Code

The hidden command `anthill runner-smoke` starts one run of the real agent in the current directory, through the same adapter the daemon uses. It stands in for the daemon:

- It takes the daemon lock, so stop the daemon first.
- It serves one ant tool, `smoke_ping`, which answers `pong`.
- It queues the notices given with `--notice` for the `PostToolUse` hook.
- It prints its own report on standard output, as lines starting with `smoke:`, and the agent's streaming JSON on standard error.
- Ctrl+C stops the run with a 10 second grace period.

### Before you start

1. Install Claude Code and log in. `claude --version` prints a version, and `claude -p "say hi"` answers.
2. Build Anthill from the branch under test:
   - macOS or Linux: `make build`; the binary is `bin/anthill`.
   - Windows (PowerShell): `go build -o bin\anthill.exe .\cmd\anthill`.
3. Stop the Anthill daemon if it runs.
4. Make a throwaway repository and go into it:

   ```bash
   mkdir /tmp/smoke && cd /tmp/smoke && git init && echo "# smoke" > README.md && git add . && git commit -m init
   ```

   On Windows, use `mkdir $env:TEMP\smoke; cd $env:TEMP\smoke` and the same git commands.

Below, `anthill` means the path of the binary you built, for example `~/src/anthill/bin/anthill` or `C:\src\anthill\bin\anthill.exe`.

### C1: a run starts, calls a tool, and ends on its own

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

### C2: the redirect hooks refuse `gh` and `git push`

```bash
anthill runner-smoke --runner claude-code "Run the shell command 'gh pr create --fill' and then 'git push origin HEAD'. Report each command's output exactly."
```

Expected:

- On standard error, the tool results of both Bash calls are errors.
- The `gh` one says `anthill: gh is disabled for agents; call the open_pull_request tool instead`.
- The `git push` one says `anthill: git push is disabled for agents; call the push tool instead`.
- The agent's last message reports both refusals.
- `gh` did not run, so no pull request was created. `git log origin/HEAD` fails, because the repository has no remote.

### C3: live input

```bash
anthill runner-smoke --runner claude-code --send "Also tell me what 6 times 7 is." --send-after 5s \
  "Run 'sleep 20' in the shell, then say done."
```

On Windows, use `Start-Sleep -Seconds 20` in the brief.

Expected:

- `smoke: live input sent` about 5 seconds after the start.
- On standard error, two `result` events: one for the brief, and one that answers 42. Note whether there were two results or one.
- The process then ends on its own with exit code 0.

If it does not end within a minute of the last result, press Ctrl+C. Then record "one result for both messages" for open point 2.

### C4: notices reach the agent through the hook

```bash
anthill runner-smoke --runner claude-code --notice "The secret word is anthill-42." \
  "Run 'echo hello' in the shell. Then tell me any Anthill notice you received, word for word."
```

Expected:

- `smoke: the hook took 1 notice(s)` after the `echo` call.
- The agent's last message quotes `The secret word is anthill-42.`

On Windows, this also checks that the hook commands run.

### C5: resume

```bash
anthill runner-smoke --runner claude-code --resume <session id from C1> "What word did the tool return earlier? Answer with that word only."
```

Expected:

- The agent answers `pong` without calling the tool again.
- `smoke: session id:` shows the session id. Note whether it is the same as in C1.

### C6: usage and cost

From the C3 run, compare the `total_cost_usd` of the two `result` events. The second is not lower than the first if the cost is cumulative. The cost printed by `smoke: usage:` is the last one.

### C7: stop leaves no process behind

```bash
anthill runner-smoke --runner claude-code --stop-after 15s \
  "Run 'sleep 300' in the shell and wait for it."
```

Expected:

- `smoke: stopping the run (--stop-after), grace 10s`.
- Within about 10 seconds, `smoke: run ended: exit code -1, crashed true`.
- Afterwards, no agent process is left:
  - macOS or Linux: `ps -ef | grep -E 'claude|sleep 300' | grep -v grep` prints nothing.
  - Windows: `Get-Process | Where-Object { $_.ProcessName -match 'claude|node' }` shows no process started by the smoke run. Compare with the list before the run if other Claude Code windows are open.

Also run C7 with Ctrl+C instead of `--stop-after`: the output and the result are the same, with `(interrupted)`.

### Results

Copy this table into the pull request or issue that records the smoke run:

| Check | macOS/Linux | Windows | Notes |
|---|---|---|---|
| C1 run, tool, own end |  |  |  |
| C2 hooks refuse gh, git push |  |  |  |
| C3 live input (results: one or two?) |  |  |  |
| C4 notice through the hook |  |  |  |
| C5 resume (same session id?) |  |  |  |
| C6 cost cumulative? |  |  |  |
| C7 no process after stop |  |  |  |
