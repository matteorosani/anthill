## Purpose

Defines how Anthill exports its own traces, metrics and logs, and those of the agents it runs, through OpenTelemetry. It also covers how the two are linked to each other, and what content is never exported.

## ADDED Requirements

### Requirement: Export is off until configured
Telemetry export SHALL be off by default and SHALL start only when an OTLP endpoint is configured in the user's local configuration. When export is off, no telemetry SHALL leave the machine.

#### Scenario: No endpoint configured
- **WHEN** Anthill runs without an OTLP endpoint
- **THEN** no traces, metrics or logs are sent anywhere

#### Scenario: Endpoint configured
- **WHEN** the local configuration sets an OTLP endpoint
- **THEN** Anthill exports traces, metrics and logs to it

### Requirement: Traces
When export is on, Anthill SHALL emit spans for: each poll cycle and provider call, eligibility evaluation, each claim attempt, each ant run and its tool calls, and each shepherd round. Spans SHALL carry the item, PR, ant id, instance id and runner as attributes where they apply.

#### Scenario: Ant run trace
- **WHEN** an ant run for `gh-12` calls `push` and `open_pull_request`
- **THEN** the run's span has child spans for both tool calls, with attributes `anthill.item=gh-12` and the ant id

### Requirement: Metrics
When export is on, Anthill SHALL emit at least:
- running and queued ants;
- run duration and outcome;
- tokens and cost per run where known;
- provider poll latency and errors, and remaining rate limit;
- claims won and lost;
- review rounds and findings by severity.

#### Scenario: Usage metrics
- **WHEN** an ant run ends with known tokens and cost
- **THEN** both are emitted as metrics tagged with the item, runner and repository

### Requirement: Logs
When export is on, Anthill SHALL export its structured application logs, and each agent's activity log as captured from the runner. Log records SHALL be linked to the active trace and span.

#### Scenario: Agent activity linked to its run
- **WHEN** an agent log line is captured during a run
- **THEN** the exported log record carries the run's trace id and the ant id

### Requirement: Agent telemetry is switched on and linked
When export is on and a runner supports OpenTelemetry, Anthill SHALL launch the agent with telemetry export to the same endpoint. It SHALL set resource attributes `anthill.item`, `anthill.ant_id` and `anthill.instance`, and pass the run's trace context where the runner accepts it.

#### Scenario: Agent telemetry carries Anthill attributes
- **WHEN** an ant runs on a runner with OpenTelemetry support while export is on
- **THEN** the agent's own telemetry arrives at the endpoint with `anthill.item` and `anthill.ant_id`

#### Scenario: Runner without telemetry support
- **WHEN** a runner has no OpenTelemetry support
- **THEN** only Anthill's telemetry and the captured agent log are exported for that run

### Requirement: Content is never exported
Exported telemetry SHALL NOT contain prompts, task briefs, code, diffs, ticket or comment bodies, agent messages, or credentials, whether from Anthill or from agents. Anthill SHALL launch agents with their own content capture turned off, and SHALL apply secret redaction to log messages.

#### Scenario: Agent prompt logging
- **WHEN** Anthill switches on an agent's telemetry
- **THEN** the agent's prompt and content logging options are set to off

#### Scenario: Captured agent log line with code
- **WHEN** a captured agent activity line contains file content
- **THEN** the exported record contains only the event type, tool name and timing, not the content
