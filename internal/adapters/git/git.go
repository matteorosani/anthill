// Package git implements ports.SourceControl with the git command-line
// tool, run through os/exec. It never changes the developer's working
// copy: branches are created with git branch, ant work happens in linked
// worktrees under the worktree root, and the only line it writes to the
// clone's .git/config is extensions.worktreeConfig = true, so that each
// worktree gets its own hooks and settings (spec branching-policy).
//
// An agent can write the clone's configuration and hooks, so no git
// command that reads them holds credentials (design D4). Commands in the
// clone or a worktree run with the agent's environment and with hooks
// and core.fsmonitor off (local). Only fetch, push and ls-remote hold
// credentials, and they run in a transfer repository (transfer.go) that
// reads none of the clone's configuration.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// MinVersion is the oldest git Anthill supports: it needs
// extensions.worktreeConfig (2.20) and trailer formats in git log.
var MinVersion = Version{2, 25, 0}

// Git runs the git command for Anthill.
type Git struct {
	bin      string
	redactor *domain.Redactor
	// transferRoot holds the transfer repositories and noHooks, the empty
	// hooks directory of every command.
	transferRoot string

	noHooksMu sync.Mutex
	noHooksOK bool

	versionMu  sync.Mutex
	versionOK  bool
	versionErr error
}

var _ ports.SourceControl = (*Git)(nil)

// TransferDirName is the directory, in <anthill-home>, that holds the
// transfer repositories.
const TransferDirName = "transfer"

// New returns a source control that runs git from the PATH. The redactor
// scrubs git's output in errors and push results; nil means the built-in
// patterns only. home is Anthill's data folder, <anthill-home>: the
// transfer repositories live in its TransferDirName directory.
func New(redactor *domain.Redactor, home string) *Git {
	if redactor == nil {
		redactor, _ = domain.NewRedactor(nil)
	}
	return &Git{bin: "git", redactor: redactor, transferRoot: filepath.Join(home, TransferDirName)}
}

// Error is a git command that failed. Stderr is redacted.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// exitCode returns the exit code of a failed git command, or -1.
func exitCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.ExitCode
	}
	return -1
}

// result is the output of one git command.
type result struct {
	stdout, stderr string
}

// env selects the environment of a git command.
type env int

const (
	// agentEnv is the agent's environment (domain.AgentEnv): no provider
	// token and no credential helper. Every command that reads the
	// clone's configuration or runs in a worktree uses it.
	agentEnv env = iota
	// anthillEnv is Anthill's own environment, with the developer's
	// credentials. Only commands in a transfer repository use it.
	anthillEnv
)

// run checks the git version once, then runs git in dir with the agent's
// environment and hooks and core.fsmonitor off.
func (g *Git) run(ctx context.Context, dir string, args ...string) (result, error) {
	return g.runIn(ctx, agentEnv, dir, nil, args...)
}

// runIn checks the git version once, then runs git in dir with the
// environment e, hooks and core.fsmonitor off, and stdin as input.
func (g *Git) runIn(ctx context.Context, e env, dir string, stdin io.Reader, args ...string) (result, error) {
	return g.runWith(ctx, e, dir, stdin, nil, args...)
}

// runWith is runIn with extra git options before args, which errors do
// not show.
func (g *Git) runWith(ctx context.Context, e env, dir string, stdin io.Reader, extra []string, args ...string) (result, error) {
	if err := g.checkVersion(ctx); err != nil {
		return result{}, err
	}
	noHooks, err := g.noHooksDir()
	if err != nil {
		return result{}, err
	}
	// An empty core.fsmonitor turns it off in every git version; "false"
	// would be a hook path before git 2.36.
	opts := append([]string{"-c", "core.hooksPath=" + filepath.ToSlash(noHooks), "-c", "core.fsmonitor="}, extra...)
	return g.exec(ctx, e, dir, stdin, opts, args...)
}

// output runs git and returns its standard output without the final
// newline.
func (g *Git) output(ctx context.Context, dir string, args ...string) (string, error) {
	r, err := g.run(ctx, dir, args...)
	return strings.TrimRight(r.stdout, "\r\n"), err
}

// noHooksDir creates, once, the empty directory every command takes as
// core.hooksPath, and returns it.
func (g *Git) noHooksDir() (string, error) {
	dir := filepath.Join(g.transferRoot, "no-hooks")
	g.noHooksMu.Lock()
	defer g.noHooksMu.Unlock()
	if !g.noHooksOK {
		// A relative core.hooksPath would be relative to the worktree,
		// which the agent writes.
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("anthill's data folder %q is not an absolute path", filepath.Dir(g.transferRoot))
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("creating the empty hooks directory: %w", err)
		}
		g.noHooksOK = true
	}
	return dir, nil
}

// exec runs git with opts, then args, in dir. Errors show args only,
// redacted.
func (g *Git) exec(ctx context.Context, e env, dir string, stdin io.Reader, opts []string, args ...string) (result, error) {
	cmd := exec.CommandContext(ctx, g.bin, append(slices.Clip(opts), args...)...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	environ := os.Environ()
	if e == agentEnv {
		environ = domain.AgentEnv(environ)
	}
	// Anthill's own git calls never prompt, since nobody would answer,
	// and print messages in English, since some are parsed.
	cmd.Env = append(environ, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "LC_ALL=C", "LANGUAGE=C")
	// When the context ends, git is killed; WaitDelay bounds the wait for
	// a child such as ssh that still holds the output pipes.
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: g.redactor.Redact(stderr.String())}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		shown := make([]string, len(args))
		for i, a := range args {
			shown[i] = g.redactor.Redact(a)
		}
		return r, &Error{Args: shown, ExitCode: code, Stderr: r.stderr, Err: err}
	}
	return r, nil
}

// Version is a git version.
type Version [3]int

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// Less reports whether v is older than w.
func (v Version) Less(w Version) bool {
	for i := range v {
		if v[i] != w[i] {
			return v[i] < w[i]
		}
	}
	return false
}

var versionPattern = regexp.MustCompile(`^git version (\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion reads the output of git version, such as "git version
// 2.45.1.windows.1" or "git version 2.39.3 (Apple Git-145)".
func ParseVersion(s string) (Version, error) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("unrecognised git version %q", strings.TrimSpace(s))
	}
	var v Version
	for i, part := range m[1:] {
		if part != "" {
			v[i], _ = strconv.Atoi(part)
		}
	}
	return v, nil
}

// checkVersion runs git version on the first call and fails every call
// when git is missing or older than MinVersion. A check cut short by its
// context is tried again on the next call.
func (g *Git) checkVersion(ctx context.Context) error {
	g.versionMu.Lock()
	defer g.versionMu.Unlock()
	if g.versionOK || g.versionErr != nil {
		return g.versionErr
	}
	r, err := g.exec(ctx, agentEnv, "", nil, nil, "version")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		g.versionErr = fmt.Errorf("running git: %w", err)
		return g.versionErr
	}
	v, err := ParseVersion(r.stdout)
	if err == nil {
		err = CheckMinimum(v)
	}
	g.versionErr, g.versionOK = err, err == nil
	return err
}

// CheckMinimum fails when v is older than MinVersion.
func CheckMinimum(v Version) error {
	if v.Less(MinVersion) {
		return fmt.Errorf("git %s or later is required, found %s", MinVersion, v)
	}
	return nil
}
