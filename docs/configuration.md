# Configuration

Anthill reads its settings from several layers (spec anthill-configuration). For each setting, the highest layer that sets it wins:

1. **label**: a per-item setting label on the item: `agent:<runner>`, `budget:<minutes>` or `base:<branch>`.
2. **.anthill.yml**: the repository file at the root of the target repository. It holds team-wide settings.
3. **local repository entry**: your entry for the repository in your own configuration file.
4. **global configuration**: the top level of your own configuration file.
5. **default**: the built-in value.

Anthill records where each effective value comes from, so you can see why a setting has the value it has.

Two settings follow their own rules:

- `reviewing: false` in the global configuration turns reviewing off for the whole machine, whatever the repository entries say.
- `review-runner`, when no layer sets it, is the repository's `runner`. An `agent:` label changes the runner of one item's ant, but not the review runner.

## Files

### Your configuration file

The global configuration and the local repository entries live in one YAML file, `anthill/config.yml` in your OS configuration directory:

| OS | Path |
|----|------|
| Linux | `~/.config/anthill/config.yml` (or `$XDG_CONFIG_HOME/anthill/config.yml`) |
| macOS | `~/Library/Application Support/anthill/config.yml` |
| Windows | `%AppData%\anthill\config.yml` |

If the file is missing, Anthill starts with no repositories. Global settings sit at the top level. Each repository this machine works on has one entry under `repositories:`:

```yaml
capacity: 3
otlp-endpoint: http://localhost:4318
secret-patterns:
  - 'ACME-[0-9]{8}'
azure-devops-projects:
  - project: contoso/shop
    default-repository: web
provider-poll-intervals:
  azure-devops/contoso: 5m
notifications:
  claim-lost: false

repositories:
  - provider: github
    remote: matteorosani/anthill
    path: /home/me/src/anthill
    runner: codex
  - provider: azure-devops
    remote: contoso/shop/web
    path: /home/me/src/web
    reviewing: false
```

A repository is identified by `provider` (`github` or `azure-devops`) and `remote` (`owner/name` on GitHub, `organization/project/repository` on Azure DevOps). Each repository may appear only once. `path` is the absolute path of your local clone. An entry may also set any shared setting. Those values apply when `.anthill.yml` does not set them.

### The repository file

`.anthill.yml` sits at the root of the repository and holds only the shared settings marked `.anthill.yml` in the table below. Anthill reads it from the working folder of your local clone, whatever branch is checked out, so an uncommitted edit takes effect at the next reload:

```yaml
budget: 45
warning-threshold: 75
extension-allowance:
  minutes: 15
  times: 1
protected-branches: [main, "release/*"]
verification-command: go test ./...
auto-follow-up: false
```

It must never hold credentials or machine paths. Anthill rejects the whole file if it contains:

- a credential key, such as `token`, `github-token`, `password` or `api-key`, at any depth: `forbidden key "token": credentials belong in the OS credential store`;
- a telemetry key, such as `otlp-endpoint`: `forbidden key "otlp-endpoint": telemetry settings belong in the local configuration`;
- `secret-patterns`: `forbidden key "secret-patterns": secret patterns belong in the local configuration`;
- a machine path, such as `path` or `worktree-root`: `forbidden key "path": machine paths belong in the local configuration`;
- a machine setting, such as `runner` or `capacity`: `forbidden key "runner": machine settings belong in the local configuration`;
- a value shaped like a secret (a built-in secret pattern matches it): `key "verification-command" holds a value that looks like a secret`;
- any other key not listed below: `unknown key "budgte"`.

Your own configuration file never accepts credential keys either. OTLP headers are rejected in every file with `OTLP headers belong in the OS credential store, not in a configuration file`.

### Value formats

- Budgets are whole minutes, like the `budget:` label: `budget: 45`.
- Percentages are whole numbers without `%`: `warning-threshold: 80`.
- Intervals are durations with a unit: `poll-interval: 2m`, `question-staleness: 72h`.
- `extension-allowance` has `minutes` and `times`. When `minutes` is set and `times` is omitted, the allowance applies once.
- `protected-branches` is a list of glob patterns such as `release/*`. The base branch and the default branch are always protected.
- `azure-devops-projects` is a list of entries, each with `project` (`organization/project`, in any case) and `default-repository` (the name of a Git repository of that project). An Azure DevOps work item goes to the repository its `repo:<name>` label names, else to its project's default repository, else to the project's only Git repository. Otherwise it is unmapped (spec work-item-sources, "Repository mapping").
- `provider-poll-intervals` maps a provider connection to its own poll interval. Anthill polls each connection on its own: `github` for GitHub, and `azure-devops/<organization>` for each Azure DevOps organization, written in any case. A connection without an entry uses `poll-interval`. Each interval must be at least `30s`.
- `notifications` turns desktop notifications on (`true`) or off (`false`) per kind. A kind without an entry stays on. The kinds are `proposal`, `question`, `stale-question`, `extension-request`, `warning`, `succeeded`, `failed`, `timed-out`, `no-change`, `follow-up-started`, `follow-up-proposed`, `follow-up-limit`, `change-stored`, `claim-lost` and `poll-failed` (spec queen-console, "Notifications"). An unknown kind or a value that is not `true` or `false` makes the global configuration invalid.
- `secret-patterns` is a list of Go regular expressions (RE2 syntax). A pattern can name a group `secret` to redact only that part. These patterns add to the built-in ones, which cannot be removed.

## Validation

Anthill validates the configuration when the daemon starts and when it reloads.

- An invalid global configuration stops the daemon from starting, with a precise error such as `config.yml: warning-threshold: warning threshold must be between 1 and 99 percent`. On reload, Anthill keeps the previous configuration and reports the error.
- An invalid repository configuration disables only that repository. The repository's `.anthill.yml` or its local entry may be invalid. The other repositories keep working. A disabled repository starts no new proposals, ants, runs or shepherd rounds until it is fixed. Runs that are already active finish normally.

## Credentials

Provider credentials and OTLP headers never live in a configuration file. Anthill looks for a credential in this order:

1. The OS credential store (macOS Keychain, Windows Credential Manager, or the Secret Service on Linux), under the service `anthill`, with these accounts:
   - `github:<host>`, such as `github:github.com`, for a GitHub token;
   - `azure-devops:<organization>` for an Azure DevOps token;
   - `otlp-headers` for the OTLP headers, written as `key1=value1,key2=value2`.
2. The provider's own CLI session, when the store has no entry: `gh auth token --hostname <host>` for GitHub, and `az account get-access-token` for Azure DevOps. OTLP headers come only from the store.

An Azure DevOps value shaped like a JWT, such as the token `az` prints, is a Microsoft Entra ID access token. Anthill sends it as a Bearer token and asks for a new one two minutes before it expires. Any other value is a personal access token (PAT), sent with Basic authentication. A PAT needs these scopes:

- Work Items (Read & write), for work items, their tags, comments and claims;
- Code (Read & write), for repositories and pull requests;
- Identity (Read) and Security (Manage), to check who has write access. Without them, Anthill treats every user as without write access and logs one warning.

Anthill works with Azure DevOps Services (`dev.azure.com`) only. Azure DevOps Server (on-premises) is not supported.

Credential values never appear in logs, errors, comments, task briefs or telemetry.

## Settings

<!-- settings:begin -->
| Key | Layers | Default | Description |
|-----|--------|---------|-------------|
| `opt-in-label` | .anthill.yml, local repository entry, global configuration | anthill | Label an item needs before Anthill proposes it. |
| `hold-label` | .anthill.yml, local repository entry, global configuration | anthill:hold | Label that stops Anthill from starting any work on the item. |
| `claim-label` | .anthill.yml, local repository entry, global configuration | anthill:claimed | Label that marks a claimed item or PR. |
| `budget` | label, .anthill.yml, local repository entry, global configuration | 60 min | Base budget of an ant, in minutes. |
| `follow-up-budget` | .anthill.yml, local repository entry, global configuration | 50% | Budget of a follow-up run, as a share of the base budget. |
| `max-base-budget` | .anthill.yml, local repository entry, global configuration | 180 min | Largest base budget a `budget:` label can set. |
| `warning-threshold` | .anthill.yml, local repository entry, global configuration | 80% | Share of the budget at which the ant is warned (1 to 99). |
| `extension-allowance` | .anthill.yml, local repository entry, global configuration | 0 min | Extensions approved without the queen: up to `minutes`, `times` times per budget. |
| `ceiling` | .anthill.yml, local repository entry, global configuration | 3× | Most one budget can grow to with extensions, as a multiple of its initial length. |
| `branch-prefix` | .anthill.yml, local repository entry, global configuration | ant | First part of every ant branch name. |
| `base-branch` | label, .anthill.yml, local repository entry, global configuration | none | Branch ant branches start from; none means the remote's default branch. |
| `protected-branches` | .anthill.yml, local repository entry, global configuration | none | Extra branch patterns Anthill never pushes to. |
| `shepherd-scope` | .anthill.yml, local repository entry, global configuration | ant-authored | PRs the shepherd reviews on its own: `ant-authored`, `labelled` or `all`. |
| `review-runner` | .anthill.yml, local repository entry, global configuration | same as runner | Runner used for shepherd rounds and fix passes. |
| `round-limit` | .anthill.yml, local repository entry, global configuration | 3 | Automatic shepherd rounds per PR. |
| `auto-follow-up` | .anthill.yml, local repository entry, global configuration | true | Start follow-up runs on their own; `false` proposes them to the queen. |
| `follow-up-limit` | .anthill.yml, local repository entry, global configuration | 5 | Follow-up runs per PR. |
| `auto-resume` | .anthill.yml, local repository entry, global configuration | false | Resume runs interrupted by a restart on their own. |
| `verification-command` | .anthill.yml, local repository entry, global configuration | none | Command that must pass before Anthill pushes a shepherd auto-fix; none means suggested changes only. |
| `require-acceptance-criteria` | .anthill.yml, local repository entry, global configuration | false | Require an acceptance-criteria section before an item is eligible. |
| `agent-instructions` | .anthill.yml, local repository entry, global configuration | none | Path, inside the repository, of the instructions file given to agents. |
| `runner` | label, local repository entry, global configuration | claude-code | Runner for ants: `claude-code`, `codex` or `opencode`. |
| `capacity` | local repository entry, global configuration | 2 | Most active runs at a time; in an entry, a limit for that repository within the machine's capacity. |
| `reviewing` | local repository entry, global configuration | true | Take part in shepherd reviews; `false` globally turns it off for the machine. |
| `poll-interval` | global configuration | 2m | Time between two polls of a provider. |
| `lease` | global configuration | 15m | How long a claim stays valid without renewal. |
| `grace-period` | global configuration | 3m | Time an ant gets to commit after its budget runs out. |
| `extension-wait` | global configuration | 5m | How long `request_extension` waits for the queen. |
| `question-staleness` | global configuration | 72h | Time after which an unanswered question is reported as stale. |
| `otlp-endpoint` | global configuration | none | OTLP endpoint for telemetry export; none means export is off. |
| `secret-patterns` | global configuration | none | Extra secret patterns, added to the built-in ones. |
| `worktree-root` | global configuration | ~/.anthill/worktrees | Absolute path of the directory that holds the ant worktrees, laid out as `<provider>-<org>-<name>/<branch>`. |
| `azure-devops-projects` | global configuration | none | Default target repository of each Azure DevOps project, for work items without a `repo:` label: a list of `project` (`organization/project`) and `default-repository`. |
| `provider-poll-intervals` | global configuration | none | Poll interval per provider connection, overriding `poll-interval`: a map from `github` or `azure-devops/<organization>` to an interval of at least 30s. |
| `notifications` | global configuration | every kind on | Desktop notifications per kind: a map from a notification kind to `true` or `false`. |
| `provider` | local repository entry | required | Provider of the repository: `github` or `azure-devops`. |
| `remote` | local repository entry | required | Repository on the provider: `owner/name` or `organization/project/repository`. |
| `path` | local repository entry | required | Absolute path of the local clone. |
<!-- settings:end -->

## Behaviour that is not configurable

- Shepherd auto-fix runs only on human PRs that opt in, one PR at a time, with the `anthill:autofix` label or `/anthill fix`.
- Telemetry export stays off until you set `otlp-endpoint`.
- The `repo:<name>` label picks an item's target repository. It is not a setting.
- Anthill never changes your clone's working copy, checked-out branch or uncommitted changes. The only thing it writes to the clone's `.git/config` is `extensions.worktreeConfig = true`, so that each ant worktree can have its own hooks and its own git settings.
- In an ant worktree, Anthill's hooks run first and then call the repository's own hook of the same name, if there is one, so checks such as `pre-commit` and `commit-msg` still run. `pre-push` is the exception: it always refuses, because ants push only through the `push` tool.
