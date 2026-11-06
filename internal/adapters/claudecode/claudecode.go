// Package claudecode is the AgentRunner of Claude Code (design D6). It
// runs claude headless with streaming JSON on both standard input and
// output, so it has every capability: it captures the session id,
// resumes sessions, gives live input, installs the redirect and notice
// hooks, and reports usage. docs/runners.md lists the flags and why.
package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/agentenv"
	"github.com/matteorosani/anthill/internal/adapters/internal/agentproc"
	"github.com/matteorosani/anthill/internal/ports"
)

// DefaultCommand is the Claude Code CLI.
const DefaultCommand = "claude"

// MCPServerName is the name of the ant tools server in the run's MCP
// configuration, so the agent sees the tools as mcp__anthill__<tool>.
const MCPServerName = "anthill"

// PermissionMode and AllowedTools are the most restrictive headless
// setting that still lets the agent edit files and run commands, such as
// the tests, in its worktree without asking (design, Risks): edits are
// accepted, and only the listed tools run without a prompt. The
// PreToolUse hook still refuses git push, gh and az.
const PermissionMode = "acceptEdits"

// SettingSources leaves the developer's user settings out of a run:
// their plugins, skills, hooks, commands, subagents and CLAUDE.md
// (design D4; spec ant-execution, "Personal agent settings are left
// out"). The repository's own settings, and the developer's local
// settings for that repository, still apply. --strict-mcp-config does
// the same for MCP servers, and the run's own --settings are not a
// setting source, so its hooks still run.
const SettingSources = "project,local"

// AutoMemoryOff turns off Claude Code's auto memory for a run. The
// memory is personal: all worktrees of a repository share one memory
// folder in the developer's home, which --setting-sources does not
// cover (design D4; spec ant-execution, "Personal agent settings are
// left out").
const AutoMemoryOff = "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1"

// AllowedTools are the tools the agent uses without a prompt. On
// Windows, Claude Code also runs commands with its PowerShell tool, which
// would otherwise ask before every command that is not read-only
// (issue #146).
var AllowedTools = []string{ToolBash, ToolPowerShell, "Edit", "Write", "Read", "Glob", "Grep", "mcp__" + MCPServerName}

// Options configure the runner.
type Options struct {
	// Anthill is the path of the anthill program, which the hooks run.
	Anthill string
	// Command is the Claude Code CLI; empty means DefaultCommand on the
	// PATH.
	Command string
	// Log, when set, receives the agent's output lines and its standard
	// error, as its activity log. It must be safe for concurrent use.
	Log io.Writer
}

// Runner starts Claude Code runs.
type Runner struct {
	opts Options
}

var _ ports.AgentRunner = (*Runner)(nil)

// New returns a runner. Options.Anthill is required.
func New(opts Options) (*Runner, error) {
	if opts.Anthill == "" {
		return nil, errors.New("claude code runner: the anthill program is required for the hooks")
	}
	if opts.Command == "" {
		opts.Command = DefaultCommand
	}
	return &Runner{opts: opts}, nil
}

// Capabilities are all supported.
func (r *Runner) Capabilities() ports.Capabilities {
	return ports.Capabilities{Resume: true, LiveInput: true, Hooks: true, Usage: true}
}

// Available finds the CLI on the PATH.
func (r *Runner) Available(context.Context) error {
	if _, err := exec.LookPath(r.opts.Command); err != nil {
		return fmt.Errorf("claude code: %w", err)
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
		return nil, errors.New("claude code: resume needs a session id")
	}
	return r.start(ctx, sessionID, spec)
}

func (r *Runner) start(ctx context.Context, resume string, spec ports.RunSpec) (ports.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(spec.MCPCommand) == 0 {
		return nil, errors.New("claude code: the run has no MCP command")
	}
	// The run's configuration files live outside the worktree, so the
	// agent does not commit them, and are removed when the run ends.
	dir, err := os.MkdirTemp("", "anthill-claude-")
	if err != nil {
		return nil, err
	}
	mcpPath, settingsPath := filepath.Join(dir, "mcp.json"), filepath.Join(dir, "settings.json")
	if err := writeJSON(mcpPath, mcpConfig(spec.MCPCommand)); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := writeJSON(settingsPath, hookSettings(r.opts.Anthill, spec.ToolToken)); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--mcp-config", mcpPath,
		"--strict-mcp-config",
		"--setting-sources", SettingSources,
		"--settings", settingsPath,
		"--permission-mode", PermissionMode,
		"--allowedTools", strings.Join(AllowedTools, ","),
	}
	if resume != "" {
		args = append(args, "--resume", resume)
	}

	rn := &run{dir: dir, session: resume, pending: 1, log: r.opts.Log}
	proc, err := agentproc.Start(agentproc.Spec{
		Path:   r.opts.Command,
		Args:   args,
		Dir:    spec.Worktree,
		Env:    agentenv.With(spec.Env, append([]string{AutoMemoryOff}, telemetryEnv(spec.Telemetry)...)),
		Stdin:  true,
		Line:   rn.line,
		Stderr: r.opts.Log,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("starting claude code: %w", err)
	}
	rn.proc = proc
	if err := proc.Write(userMessage(spec.Brief)); err != nil {
		_ = proc.Stop(0)
		rn.Wait()
		return nil, err
	}
	return rn, nil
}

// run is one Claude Code process.
type run struct {
	proc *agentproc.Process
	dir  string
	log  io.Writer

	mu      sync.Mutex
	session string
	// pending counts the input messages whose result has not arrived,
	// for a Claude Code that does not report queued_turn_count. At zero,
	// the input is closed and Claude Code exits.
	pending     int
	inputClosed bool
	// resultTokens are the tokens of the results so far, and
	// messageTokens those of the main-loop messages since the last
	// result, by message id, so that a run stopped before its result
	// keeps them (spec ant-execution, "Usage of a stopped run").
	resultTokens  *int64
	messageTokens map[string]int64
	cost          *float64

	cleanup sync.Once
}

// SessionID is the session Claude Code reported, or the resumed one
// until it reports one.
func (r *run) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

// Send gives input to the agent as a new user message. Claude Code may
// answer it in the turn it is working on, or in a turn of its own. Send
// fails once the agent has answered every message and its input is
// closed.
func (r *run) Send(ctx context.Context, input string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.inputClosed {
		r.mu.Unlock()
		return errors.New("send: the run's input has ended")
	}
	r.pending++
	r.mu.Unlock()
	if err := r.proc.Write(userMessage(input)); err != nil {
		r.mu.Lock()
		r.pending--
		r.mu.Unlock()
		return fmt.Errorf("send: %w", err)
	}
	return nil
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

// Usage is the tokens of the results so far, plus those of the agent's
// messages since the last result, each message once. The cost is the
// last result's, which is a total for the session, and unknown before
// any result.
func (r *run) Usage() ports.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	var tokens *int64
	if r.resultTokens != nil || len(r.messageTokens) > 0 {
		var n int64
		if r.resultTokens != nil {
			n = *r.resultTokens
		}
		for _, t := range r.messageTokens {
			n += t
		}
		tokens = &n
	}
	// total_cost_usd is the running total of the session: a resumed
	// session starts from the spend of its earlier runs.
	return ports.Usage{Tokens: tokens, CostUSD: r.cost, CostIsSessionTotal: true}
}

// line handles one line of Claude Code's output.
func (r *run) line(b []byte) {
	if r.log != nil {
		_, _ = r.log.Write(append(append([]byte(nil), b...), '\n'))
	}
	ev, ok := parseEvent(b)
	if !ok {
		return
	}
	r.mu.Lock()
	if ev.SessionID != "" {
		r.session = ev.SessionID
	}
	if id, n, ok := ev.messageTokens(); ok {
		if r.messageTokens == nil {
			r.messageTokens = map[string]int64{}
		}
		if _, seen := r.messageTokens[id]; !seen {
			r.messageTokens[id] = n
		}
	}
	closeInput := false
	if ev.Type == eventResult {
		// The result's usage covers the turn's messages so far.
		if n, ok := ev.tokens(); ok {
			if r.resultTokens != nil {
				n += *r.resultTokens
			}
			r.resultTokens = &n
			clear(r.messageTokens)
		}
		if ev.TotalCostUSD != nil {
			cost := *ev.TotalCostUSD
			r.cost = &cost
		}
		r.pending--
		// Every message is answered when no message waits for a turn of
		// its own. A message written just before this result, and not
		// yet read, stays in the pipe ahead of the end of the input; that
		// Claude Code still answers it is an open point of
		// docs/runners.md.
		answered := r.pending <= 0
		if ev.QueuedTurnCount != nil {
			answered = *ev.QueuedTurnCount == 0
		}
		if answered && !r.inputClosed {
			r.inputClosed, closeInput = true, true
		}
	}
	r.mu.Unlock()
	if closeInput {
		_ = r.proc.CloseInput()
	}
}

// userMessage is one input line of the streaming JSON input.
func userMessage(text string) []byte {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": text}},
		},
		"parent_tool_use_id": nil,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// mcpConfig is the --mcp-config file: the ant tools server only.
func mcpConfig(command []string) any {
	return map[string]any{
		"mcpServers": map[string]any{
			MCPServerName: map[string]any{
				"type":    "stdio",
				"command": command[0],
				"args":    append([]string{}, command[1:]...),
			},
		},
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
