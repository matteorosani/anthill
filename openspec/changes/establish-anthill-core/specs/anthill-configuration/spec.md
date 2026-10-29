## Purpose

Defines how Anthill is configured: which layers of configuration exist and how they combine, which settings are per repository, the default values the other capabilities rely on, and how provider credentials are handled.

## ADDED Requirements

### Requirement: Configuration layers
Configuration SHALL be resolved from these layers, highest precedence first: per-item labels or tags where a capability defines them, then the optional repository file `.anthill.yml` in the target repository, then the user's local repository entry, then the user's global configuration, then built-in defaults. The effective value and its source SHALL be inspectable.

#### Scenario: Repository file overrides global default
- **WHEN** the global budget is 60 minutes and the repository's `.anthill.yml` sets 45
- **THEN** ants for that repository get 45 minutes, and inspecting the setting shows `.anthill.yml` as its source

### Requirement: Repository file location
Anthill SHALL read `.anthill.yml` from the root of the local clone's working folder, whatever branch is checked out there, including uncommitted edits. It SHALL NOT read the file from a commit or from the provider.

#### Scenario: Uncommitted edit is used
- **WHEN** the developer edits `.anthill.yml` in the local clone to set the budget to 30 and has not committed the change
- **THEN** after the next reload, ants for that repository get 30 minutes, and inspecting the setting shows `.anthill.yml` as its source

### Requirement: Shared repository settings
`.anthill.yml` SHALL be able to set team-wide behaviour only: opt-in, hold and claim labels, budgets, maximum base budget and warning threshold, extension allowance and ceiling, branch prefix and base branch, protected branch patterns, shepherd scope, review runner, round limit, auto-follow-up, follow-up limit, auto-resume, verification command, acceptance-criteria requirement, and agent instructions file. It SHALL NOT contain credentials or machine paths.

#### Scenario: Credentials in repository file
- **WHEN** `.anthill.yml` contains a `token` key
- **THEN** the file is rejected with an error naming the forbidden key, and the repository is treated as misconfigured

### Requirement: Local repository entries
The user's local configuration SHALL list the repositories this machine works on. Each entry holds the provider and remote identity, the path of the local clone, and optional local overrides such as the runner, capacity, and turning shepherd reviewing off for that repository. The global configuration SHALL also allow turning reviewing off for the whole machine.

#### Scenario: Repository listed without a clone
- **WHEN** a configured local path does not contain a git clone of the configured remote
- **THEN** the daemon reports the repository as unavailable, and its items are not eligible on this machine

### Requirement: Telemetry settings are local
The OTLP endpoint SHALL be accepted only in local configuration, never in `.anthill.yml`. OTLP headers SHALL be read from the OS credential store, never from a configuration file.

#### Scenario: Telemetry endpoint in repository file
- **WHEN** `.anthill.yml` contains an OTLP endpoint
- **THEN** the file is rejected with an error naming the forbidden key

#### Scenario: OTLP headers in a configuration file
- **WHEN** the local configuration contains OTLP headers
- **THEN** the configuration is rejected with an error saying that OTLP headers belong in the OS credential store

### Requirement: Built-in defaults for labels and timing
Anthill SHALL provide these defaults:
- opt-in label `anthill`; hold label `anthill:hold`; claim label `anthill:claimed`
- poll interval 2 min; claim lease 15 min
- budget 60 min; maximum base budget 180 min; warning at 80%; ceiling 3× budget; grace period 3 min; follow-up budget 50%; extension auto-approve allowance 0 min (always ask the queen)

#### Scenario: Default budget and lease
- **WHEN** no budget or lease is configured anywhere
- **THEN** ants get 60 minutes and claims are leased for 15 minutes

#### Scenario: No auto-approve allowance by default
- **WHEN** no extension allowance is configured and an ant requests 5 more minutes
- **THEN** the request is not approved automatically and waits for the queen's decision

### Requirement: Built-in defaults for limits and automation
Anthill SHALL provide these defaults:
- telemetry export off; extension wait 5 min
- capacity 2 active runs; review rounds 3; follow-up limit 5; question staleness 3 days
- runner `claude-code`; shepherd reviewing on; review runner same as ant runner; shepherd scope `ant-authored`; auto-fix only on human PRs that opt in per PR; auto-follow-up on; auto-resume off; branch prefix `ant`

#### Scenario: Empty configuration
- **WHEN** Anthill runs with only one repository entry and no other settings
- **THEN** all behaviour follows the listed defaults

### Requirement: Secret patterns
Anthill SHALL redact secrets with built-in patterns covering common credential formats: provider tokens, cloud access keys, private keys, bearer tokens and connection strings. The user's local configuration SHALL be able to add patterns. Built-in patterns SHALL NOT be removable, and `.anthill.yml` SHALL NOT set patterns.

#### Scenario: Built-in pattern
- **WHEN** a comment body contains a string shaped like a GitHub personal access token
- **THEN** the value is redacted without any pattern being configured

#### Scenario: Local pattern added
- **WHEN** the local configuration adds a pattern for the team's internal API keys
- **THEN** strings matching it are redacted along with the built-in patterns

### Requirement: Validation at startup and reload
Configuration SHALL be validated when the daemon starts and when it reloads. An invalid global configuration SHALL prevent startup with a precise error. An invalid repository configuration SHALL disable only that repository.

#### Scenario: Warning threshold out of range
- **WHEN** the global configuration sets the warning threshold to 120%
- **THEN** the daemon refuses to start with "warning threshold must be between 1 and 99 percent"

### Requirement: Credentials stay in secure storage
Provider credentials SHALL come from the OS credential store or from the provider's own authenticated CLI session (for example `gh` or `az`). OTLP headers SHALL come from the OS credential store. Neither SHALL be written to configuration files, logs, comments, task briefs or telemetry.

#### Scenario: Using an existing gh login
- **WHEN** the user is logged in with the GitHub CLI and has configured no token
- **THEN** Anthill authenticates to GitHub using that session

#### Scenario: Token never logged
- **WHEN** a provider request fails with an authentication error
- **THEN** the logged error contains no credential value
