# Anthill: conventions for contributors and agents

## Read first

- `openspec/glossary.md`: use every term exactly as defined there, and follow its writing rules.
- `openspec/changes/establish-anthill-core/`: `proposal.md`, `design.md` (D13 fixes the Go layout and libraries), `specs/`, and `tasks.md`.

## Workflow

- One task group from `tasks.md` per branch and per PR to `main`. Never push to `main`.
- Tick the task checkboxes (`- [x]`) in `tasks.md` in the same PR that completes them.
- Each task has a GitHub issue. List them in the PR as `Closes #<n>`.
- Fill in every section of `.github/pull_request_template.md`. The "Go concepts" section explains, for a reader new to Go, the Go ideas the PR introduces, with links to the code that uses them.
- The maintainer reviews and merges. Do not merge your own PR.

## Code

- Go module `github.com/matteorosani/anthill`. Layout: `cmd/anthill`, `internal/domain`, `internal/app`, `internal/ports`, `internal/adapters/<name>`, `internal/testkit`.
- The domain has no I/O. Application services depend on ports, never on adapters.
- Every task group adds tests. Tests use fakes, never the network or a real agent.
- Code must build and pass tests on Linux, macOS and Windows.

## Before pushing

Run what CI runs:

```bash
gofmt -l .
go mod tidy
go vet ./...
go test -race ./...
golangci-lint run
OPENSPEC_TELEMETRY=0 openspec validate --all --strict --no-interactive
```

## Doubts

If the specs, design or tasks are unclear, contradict each other, or leave a choice open, stop and ask. Do not guess.

## Commits and PRs

Never put a model name or model identifier in commits, PR titles, PR descriptions or code.
