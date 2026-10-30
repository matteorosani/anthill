// Package opencode is the AgentRunner of OpenCode (design D6). It runs
// opencode run headless with JSON events on standard output: it captures
// the session id, resumes sessions and reports tokens and cost. OpenCode
// has no live input, and Anthill installs no hooks for it, so messages
// reach the agent as notices on its next ant-tool result (design D5).
// docs/runners.md lists the flags and why.
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/agentenv"
	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/ports"
)

// DefaultCommand is the OpenCode CLI.
const DefaultCommand = "opencode"

// MCPServerName is the name of the ant tools server in the run's
// configuration.
const MCPServerName = "anthill"

// Options configure the runner.
type Options struct {
	// Command is the OpenCode CLI; empty means DefaultCommand on the
	// PATH.
	Command string
	// Log, when set, receives the agent's output lines and its standard
	// error, as its activity log. It must be safe for concurrent use.
	Log io.Writer
}

// Runner starts OpenCode runs.
type Runner struct {
	opts Options
}

var _ ports.AgentRunner = (*Runner)(nil)

// New returns a runner.
func New(opts Options) *Runner {
	if opts.Command == "" {
		opts.Command = DefaultCommand
	}
	return &Runner{opts: opts}
}

// Capabilities: OpenCode resumes sessions and reports tokens and cost.
// Its permission rules deny git push, gh and az, but cannot say which
// ant tool to use instead, so they do not count as hooks (spec
// ant-tools, "Provider commands are redirected to tools").
func (r *Runner) Capabilities() ports.Capabilities {
	return ports.Capabilities{Resume: true, Usage: true}
}

// Available finds the CLI on the PATH.
func (r *Runner) Available(context.Context) error {
	if _, err := exec.LookPath(r.opts.Command); err != nil {
		return fmt.Errorf("opencode: %w", err)
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
		return nil, errors.New("opencode: resume needs a session id")
	}
	return r.start(ctx, sessionID, spec)
}

func (r *Runner) start(ctx context.Context, resume string, spec ports.RunSpec) (ports.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(spec.MCPCommand) == 0 {
		return nil, errors.New("opencode: the run has no MCP command")
	}
	// The run's configuration lives outside the worktree, so the agent
	// does not commit it, and is removed when the run ends.
	dir, err := os.MkdirTemp("", "anthill-opencode-")
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(dir, "opencode.json")
	b, err := json.MarshalIndent(config(spec.MCPCommand), "", "  ")
	if err == nil {
		err = os.WriteFile(configPath, b, 0o600)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	// The brief goes on standard input, never on the command line.
	args := []string{"run", "--format", "json"}
	if resume != "" {
		args = append(args, "--session", resume)
	}

	rn := &run{dir: dir, session: resume, log: r.opts.Log}
	proc, err := agentproc.Start(agentproc.Spec{
		Path:   r.opts.Command,
		Args:   args,
		Dir:    spec.Worktree,
		Env:    agentenv.With(spec.Env, []string{"OPENCODE_CONFIG=" + configPath}),
		Stdin:  true,
		Line:   rn.line,
		Stderr: r.opts.Log,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("starting opencode: %w", err)
	}
	rn.proc = proc
	err = proc.Write([]byte(spec.Brief))
	if err == nil {
		err = proc.CloseInput()
	}
	if err != nil {
		_ = proc.Stop(0)
		rn.Wait()
		return nil, fmt.Errorf("giving opencode its brief: %w", err)
	}
	return rn, nil
}

// config is the run's configuration, which OpenCode merges over the
// developer's own:
//   - the ant tools server as a local MCP server;
//   - edits and commands allowed without asking, which a headless run
//     needs, except git push, gh and az, which are denied as a layer on
//     top of the missing credentials (design D4).
func config(mcp []string) any {
	return map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{
			MCPServerName: map[string]any{
				"type":    "local",
				"command": append([]string{}, mcp...),
				"enabled": true,
			},
		},
		"permission": map[string]any{
			"edit": "allow",
			"bash": map[string]string{
				"*":          "allow",
				"git push":   "deny",
				"git push *": "deny",
				"gh":         "deny",
				"gh *":       "deny",
				"az":         "deny",
				"az *":       "deny",
			},
		},
	}
}

// run is one OpenCode process.
type run struct {
	proc *agentproc.Process
	dir  string
	log  io.Writer

	mu      sync.Mutex
	session string
	tokens  *int64
	cost    *float64

	cleanup sync.Once
}

// SessionID is the session OpenCode reported, or the resumed one until
// it reports one.
func (r *run) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

// Send is not supported: OpenCode reads its input only at the start.
func (r *run) Send(context.Context, string) error {
	return fmt.Errorf("send: %w", ports.ErrUnsupported)
}

// Stop ends the run's process tree.
func (r *run) Stop(grace time.Duration) error {
	return r.proc.Stop(grace)
}

// Wait waits for the process, then removes the run's configuration.
func (r *run) Wait() ports.Exit {
	exit := r.proc.Wait()
	r.cleanup.Do(func() { _ = os.RemoveAll(r.dir) })
	return exit
}

// Usage is the total of the steps so far.
func (r *run) Usage() ports.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ports.Usage{Tokens: r.tokens, CostUSD: r.cost}
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
	if ev.SessionID != "" {
		r.session = ev.SessionID
	}
	if ev.Type != eventStepFinish {
		return
	}
	if n, ok := ev.tokens(); ok {
		if r.tokens != nil {
			n += *r.tokens
		}
		r.tokens = &n
	}
	if ev.Part.Cost != nil {
		c := *ev.Part.Cost
		if r.cost != nil {
			c += *r.cost
		}
		r.cost = &c
	}
}
