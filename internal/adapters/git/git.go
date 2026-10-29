// Package git implements ports.SourceControl with the git command-line
// tool, run through os/exec. It never changes the developer's working
// copy: branches are created with git branch, ant work happens in linked
// worktrees under the worktree root, and the only line it writes to the
// clone's .git/config is extensions.worktreeConfig = true, so that each
// worktree gets its own hooks and settings (spec branching-policy).
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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

	versionMu  sync.Mutex
	versionOK  bool
	versionErr error
}

var _ ports.SourceControl = (*Git)(nil)

// New returns a source control that runs git from the PATH. The redactor
// scrubs git's output in errors and push results; nil means the built-in
// patterns only.
func New(redactor *domain.Redactor) *Git {
	if redactor == nil {
		redactor, _ = domain.NewRedactor(nil)
	}
	return &Git{bin: "git", redactor: redactor}
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

// run checks the git version once, then runs git in dir.
func (g *Git) run(ctx context.Context, dir string, args ...string) (result, error) {
	if err := g.checkVersion(ctx); err != nil {
		return result{}, err
	}
	return g.exec(ctx, dir, args...)
}

// output runs git and returns its standard output without the final
// newline.
func (g *Git) output(ctx context.Context, dir string, args ...string) (string, error) {
	r, err := g.run(ctx, dir, args...)
	return strings.TrimRight(r.stdout, "\r\n"), err
}

func (g *Git) exec(ctx context.Context, dir string, args ...string) (result, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Dir = dir
	// Anthill's own git calls never prompt, since nobody would answer,
	// and print messages in English, since some are parsed.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "LC_ALL=C", "LANGUAGE=C")
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
		return r, &Error{Args: args, ExitCode: code, Stderr: r.stderr, Err: err}
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
	r, err := g.exec(ctx, "", "version")
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
