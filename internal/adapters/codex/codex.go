// Package codex is the AgentRunner of Codex (design D6). It runs codex
// exec headless with JSON events on standard output: it captures the
// session id, resumes sessions and reports tokens. Codex has no live
// input and no hooks, so messages reach the agent as notices on its next
// ant-tool result (design D5). docs/runners.md lists the flags and why.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/agentenv"
	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/ports"
)

// DefaultCommand is the Codex CLI.
const DefaultCommand = "codex"

// MCPServerName is the name of the ant tools server in the run's
// configuration.
const MCPServerName = "anthill"

// Sandbox is the most restrictive headless sandbox that still lets the
// agent edit files and run commands in its worktree (design, Risks).
const Sandbox = "workspace-write"

// Options configure the runner.
type Options struct {
	// Command is the Codex CLI; empty means DefaultCommand on the PATH.
	Command string
	// Git is the git program, used to find the worktree's git directory;
	// empty means git on the PATH.
	Git string
	// Log, when set, receives the agent's output lines and its standard
	// error, as its activity log. It must be safe for concurrent use.
	Log io.Writer
}

// Runner starts Codex runs.
type Runner struct {
	opts Options
}

var _ ports.AgentRunner = (*Runner)(nil)

// New returns a runner.
func New(opts Options) *Runner {
	if opts.Command == "" {
		opts.Command = DefaultCommand
	}
	if opts.Git == "" {
		opts.Git = "git"
	}
	return &Runner{opts: opts}
}

// Capabilities: Codex resumes sessions and reports tokens; it has no
// live input and no hooks.
func (r *Runner) Capabilities() ports.Capabilities {
	return ports.Capabilities{Resume: true, Usage: true}
}

// Available finds the CLI on the PATH.
func (r *Runner) Available(context.Context) error {
	if _, err := exec.LookPath(r.opts.Command); err != nil {
		return fmt.Errorf("codex: %w", err)
	}
	return nil
}

// Start starts a run with a new session.
func (r *Runner) Start(ctx context.Context, spec ports.RunSpec) (ports.Run, error) {
	return r.start(ctx, "", spec)
}

// Resume starts a run that continues sessionID.
func (r *Runner) Resume(ctx context.Context, sessionID string, spec ports.RunSpec) (ports.Run, error) {
	if sessionID == "" {
		return nil, errors.New("codex: resume needs a session id")
	}
	return r.start(ctx, sessionID, spec)
}

func (r *Runner) start(ctx context.Context, resume string, spec ports.RunSpec) (ports.Run, error) {
	if len(spec.MCPCommand) == 0 {
		return nil, errors.New("codex: the run has no MCP command")
	}
	gitDir, err := r.gitCommonDir(ctx, spec.Worktree)
	if err != nil {
		return nil, err
	}
	args := append([]string{"exec"}, options(gitDir, spec.MCPCommand, spec.Telemetry)...)
	if resume != "" {
		args = append(args, "resume", resume)
	}
	// "-" reads the brief from standard input, so it is never on the
	// command line.
	args = append(args, "-")

	rn := &run{session: resume, log: r.opts.Log}
	env := spec.Env
	if spec.Telemetry != nil {
		env = agentenv.With(env, agentenv.OTLP(spec.Telemetry))
	}
	proc, err := agentproc.Start(agentproc.Spec{
		Path:   r.opts.Command,
		Args:   args,
		Dir:    spec.Worktree,
		Env:    env,
		Stdin:  true,
		Line:   rn.line,
		Stderr: r.opts.Log,
	})
	if err != nil {
		return nil, fmt.Errorf("starting codex: %w", err)
	}
	rn.proc = proc
	err = proc.Write([]byte(spec.Brief))
	if err == nil {
		err = proc.CloseInput()
	}
	if err != nil {
		_ = proc.Stop(0)
		proc.Wait()
		return nil, fmt.Errorf("giving codex its brief: %w", err)
	}
	return rn, nil
}

// options are the options of codex exec, before resume and the prompt:
//   - JSON events on standard output;
//   - the workspace-write sandbox, with the worktree's git directory,
//     which lies outside the worktree, writable so the agent can commit,
//     and network access so builds and tests can fetch dependencies;
//   - no approval prompt;
//   - the developer's own MCP servers removed, then only the ant tools
//     server, because a server holding a token would give the agent
//     provider access (design D4);
//   - prompt logging off, and the OTLP exporter when t is set.
//
// Values are TOML, which Codex parses for -c; a JSON string or array is
// valid TOML.
func options(gitDir string, mcp []string, t *ports.AgentTelemetry) []string {
	args := []string{
		"--json",
		"--sandbox", Sandbox,
		"--skip-git-repo-check",
		"--add-dir", gitDir,
		"-c", "sandbox_workspace_write.network_access=true",
		"-c", `approval_policy="never"`,
		"-c", "mcp_servers={}",
		"-c", "mcp_servers." + MCPServerName + ".command=" + tomlString(mcp[0]),
		"-c", "mcp_servers." + MCPServerName + ".args=" + tomlArray(mcp[1:]),
		"-c", "otel.log_user_prompt=false",
	}
	if t != nil {
		args = append(args, "-c", "otel.exporter="+otelExporter(t))
	}
	return args
}

// otelExporter is Codex's [otel] exporter for t. The headers are not in
// it, so they never appear on the command line: the exporter reads them
// from OTEL_EXPORTER_OTLP_HEADERS.
func otelExporter(t *ports.AgentTelemetry) string {
	switch t.Protocol {
	case "grpc":
		return `{ otlp-grpc = { endpoint = ` + tomlString(t.Endpoint) + ` } }`
	case "http/json":
		return `{ otlp-http = { endpoint = ` + tomlString(t.Endpoint) + `, protocol = "json" } }`
	}
	return `{ otlp-http = { endpoint = ` + tomlString(t.Endpoint) + `, protocol = "binary" } }`
}

func tomlString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func tomlArray(a []string) string {
	b, err := json.Marshal(append([]string{}, a...))
	if err != nil {
		panic(err)
	}
	return string(b)
}

// gitCommonDir is the absolute path of the worktree's shared git
// directory, where its commits go.
func (r *Runner) gitCommonDir(ctx context.Context, worktree string) (string, error) {
	cmd := exec.CommandContext(ctx, r.opts.Git, "rev-parse", "--git-common-dir")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("codex: finding the git directory of %s: %w", worktree, err)
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(worktree, dir)
	}
	return filepath.Clean(dir), nil
}

// run is one Codex process.
type run struct {
	proc *agentproc.Process
	log  io.Writer

	mu      sync.Mutex
	session string
	tokens  *int64
}

// SessionID is the thread Codex reported, or the resumed one until it
// reports one.
func (r *run) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

// Send is not supported: Codex reads its input only at the start.
func (r *run) Send(context.Context, string) error {
	return fmt.Errorf("send: %w", ports.ErrUnsupported)
}

// Stop ends the run's process tree.
func (r *run) Stop(grace time.Duration) error {
	return r.proc.Stop(grace)
}

// Wait waits for the process.
func (r *run) Wait() ports.Exit {
	return r.proc.Wait()
}

// Usage is the tokens of the turns so far. Codex reports no cost.
func (r *run) Usage() ports.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ports.Usage{Tokens: r.tokens}
}

func (r *run) line(b []byte) {
	if r.log != nil {
		_, _ = r.log.Write(append(append([]byte(nil), b...), '\n'))
	}
	ev, ok := parseEvent(b)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch ev.Type {
	case eventThreadStarted:
		if ev.ThreadID != "" {
			r.session = ev.ThreadID
		}
	case eventTurnCompleted:
		if n, ok := ev.tokens(); ok {
			if r.tokens != nil {
				n += *r.tokens
			}
			r.tokens = &n
		}
	}
}
