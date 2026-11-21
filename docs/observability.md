# Observability

Anthill can export its own traces, metrics and logs, and those of the agents it starts, to an OpenTelemetry collector over OTLP (spec observability, design D18). Export is off by default: until you set an endpoint, no telemetry leaves your machine.

## Setup

1. Run an OpenTelemetry collector, or any backend that accepts OTLP over HTTP. Anthill sends protobuf (`http/protobuf`) to `<endpoint>/v1/traces`, `<endpoint>/v1/metrics` and `<endpoint>/v1/logs`. gRPC is not supported.
2. Set the endpoint in your own configuration file, at the top level (see [configuration](configuration.md)). It is accepted only there, never in `.anthill.yml` or a repository entry:

   ```yaml
   otlp-endpoint: http://localhost:4318
   ```

3. If the endpoint needs headers, such as an API key, store them in the OS credential store, under the service `anthill` and the account `otlp-headers`, in the `OTEL_EXPORTER_OTLP_HEADERS` format: `key1=value1,key2=value2`, with values percent-encoded (a space is `%20`). For example, on Linux:

   ```bash
   secret-tool store --label "anthill otlp-headers" service anthill username otlp-headers
   # then type: Authorization=Bearer%20<your key>
   ```

   Anthill sends the headers with every export and never writes them to a configuration file or a log. Configuration files that hold OTLP headers are rejected.

4. Restart the daemon.

The standard `OTEL_*` environment variables do not turn export on: without `otlp-endpoint`, Anthill builds no exporter at all.

Every export carries the resource attributes `service.name=anthill`, `service.version` and `anthill.instance`.

## What is never exported

Telemetry holds names, ids, counts and timings only. It never holds prompts, task briefs, code, diffs, ticket or comment bodies, agent messages or credentials (spec observability, "Content is never exported"):

- application log messages and their attributes go through the same secret redaction as the local log;
- a captured line of an agent's activity is exported as its event type, tool name and time only (see [Logs](#logs));
- agents are started with their own prompt and content logging off, whether export is on or not.

## Traces

Each run is its own trace: the run span is a root span, linked to the span that started it. Its tool calls are its children, and the provider calls a tool makes are children of the tool call.

| Span | When | Attributes |
|------|------|------------|
| `anthill.poll` | one poll cycle of a provider connection | `anthill.provider` |
| `anthill.provider_call` | one call to a provider, inside a poll or a tool call | `anthill.provider`, `anthill.call`, `anthill.item` or `anthill.pr` |
| `anthill.eligibility` | the evaluation of an item's prerequisites | `anthill.item` |
| `anthill.claim` | one claim attempt | `anthill.item` or `anthill.pr`, `anthill.claim_kind`, `anthill.result`, `anthill.instance` |
| `anthill.run` | one ant run, from its start to the record of its end | `anthill.item`, `anthill.ant_id`, `anthill.run_id`, `anthill.run_kind`, `anthill.runner`, `anthill.repository`, `anthill.instance`, `anthill.outcome` |
| `anthill.tool_call` | one ant tool call, a child of its run | `anthill.tool`, and the run's attributes |
| `anthill.round` | one shepherd round | `anthill.pr`, `anthill.round_id`, `anthill.round_kind`, `anthill.runner`, `anthill.repository`, `anthill.instance` |

A span whose operation failed has the error status. `anthill.result` is `won` or `lost` on a claim, and `error` on a failed span.

## Metrics

| Metric | Type | Unit | Attributes | Meaning |
|--------|------|------|------------|---------|
| `anthill.runs.active` | gauge | `{run}` | | runs with a live process on this instance |
| `anthill.ants.queued` | gauge | `{ant}` | | ants `queued` for capacity on this instance |
| `anthill.runs` | counter | `{run}` | `anthill.item`, `anthill.runner`, `anthill.repository`, `anthill.outcome` | runs ended, by outcome |
| `anthill.run.duration` | histogram | `s` | `anthill.item`, `anthill.runner`, `anthill.repository`, `anthill.outcome` | duration of each run |
| `anthill.run.tokens` | counter | `{token}` | `anthill.item`, `anthill.runner`, `anthill.repository` | tokens of each run, when the runner reports them |
| `anthill.run.cost` | counter | `USD` | `anthill.item`, `anthill.runner`, `anthill.repository` | cost of each run, when the runner reports it; the run's own share of a session total |
| `anthill.poll.duration` | histogram | `s` | `anthill.provider`, `anthill.result` | duration of each poll cycle |
| `anthill.provider.latency` | histogram | `s` | `anthill.provider`, `anthill.call`, `anthill.result` | latency of each provider call |
| `anthill.provider.errors` | counter | `{error}` | `anthill.provider`, `anthill.call` | provider calls that failed |
| `anthill.provider.rate_limit.remaining` | gauge | `{request}` | `anthill.provider` | requests left in the provider's rate limit window, as its latest response said: GitHub's `X-RateLimit-Remaining`, the lowest of the resources used; Azure DevOps sends it only near its limit, so the gauge has no value before |
| `anthill.provider.rate_limit.reset` | gauge | `s` | `anthill.provider` | time until the provider's exhausted rate limit resets, as `anthill status` shows it; 0 while polling is not paused for it |
| `anthill.claims` | counter | `{claim}` | `anthill.claim_kind`, `anthill.result` | claim attempts won and lost |
| `anthill.review.rounds` | counter | `{round}` | `anthill.round_kind`, `anthill.repository`, `anthill.result` | shepherd rounds |
| `anthill.review.findings` | counter | `{finding}` | `anthill.severity`, `anthill.repository` | findings of shepherd rounds, by severity |

Metrics are exported every 30 seconds, and once more when the daemon stops.

## Logs

Anthill exports two kinds of log records:

- **application logs**: the daemon's structured log, with secrets redacted. A record logged during an operation carries that operation's trace and span ids.
- **agent activity**: one record per line of an agent's output during a run, with the event name `anthill.agent.activity`. It carries the run's trace and span ids and the run's attributes (`anthill.item`, `anthill.ant_id`, `anthill.run_id`, `anthill.runner`, …), plus `anthill.event`, the line's event type such as `assistant` or `item.started:mcp_tool_call`, and `anthill.tool`, the tool it calls, if any. The record's body is the event type. A line that is not a JSON event, which is a line of the agent's standard error, has the event `stderr` and no text. Nothing else from the line is exported.

## Agent telemetry

When export is on, each run starts its agent with telemetry export to the same endpoint, for runners that support it (Claude Code and Codex). The agent gets:

- `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` and `OTEL_EXPORTER_OTLP_HEADERS`: the headers are a telemetry credential the agent can read, never a provider or git credential;
- `OTEL_RESOURCE_ATTRIBUTES` with `anthill.item`, `anthill.ant_id` and `anthill.instance`, so the agent's own spans, metrics and logs can be joined to the Anthill run;
- `TRACEPARENT` with the run span's W3C trace context, for runners that honour it;
- the runner's own switches: for Claude Code `CLAUDE_CODE_ENABLE_TELEMETRY=1` with OTLP metrics and logs, for Codex its `otel.exporter` setting.

Review agents and fix agents get the same, but their `OTEL_RESOURCE_ATTRIBUTES` hold `anthill.pr` and `anthill.instance` and no `anthill.ant_id`, since they belong to no ant, and a review agent's `TRACEPARENT` is its round's trace context.

Prompt and content capture stays off: `OTEL_LOG_USER_PROMPTS=0` and `OTEL_LOG_TOOL_DETAILS=0` for Claude Code, `otel.log_user_prompt=false` for Codex. For a runner without OpenTelemetry support, such as OpenCode, only Anthill's own telemetry and the captured activity are exported for the run.
