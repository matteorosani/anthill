# Anthill

## Developing Anthill

### Prerequisites

- [Go](https://go.dev/dl/) 1.27.3 or later. With an older Go 1.21+ installed, the `go` command downloads the right version itself, because `go.mod` asks for it.
- [golangci-lint](https://golangci-lint.run/welcome/install/) v2.15 or later, for `make lint`.
- GNU make, for the `make` targets. It ships with macOS (Xcode command line tools) and most Linux distributions. On Windows, install it with `winget install GnuWin32.Make`, `choco install make` or `scoop install make`, or run the commands listed under each target in the `Makefile` directly.
- On Windows, a C compiler such as MinGW-w64 `gcc` on `PATH`, because the race detector used by `make test` needs cgo there.

### Build

```bash
make build
```

This compiles every package and writes the `anthill` binary (`anthill.exe` on Windows) to `bin/`. Check it with:

```bash
bin/anthill version     # bin\anthill version in Windows cmd or PowerShell
```

### Test and lint

```bash
make test   # go test -race ./...
make lint   # golangci-lint run
make vet    # go vet ./...
make        # build, lint, vet and test
```

Before pushing, run the checks CI runs. They are listed in `CLAUDE.md` under "Before pushing".
