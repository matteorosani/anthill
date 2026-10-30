package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/claudecode"
	"github.com/matteorosani/anthill/internal/adapters/codex"
	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/adapters/lockfile"
	"github.com/matteorosani/anthill/internal/adapters/opencode"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// smokeStopGrace is the grace period of a smoke run that is stopped.
const smokeStopGrace = 10 * time.Second

// smokeTool is the one ant tool of a smoke run.
const smokeTool = "smoke_ping"

// agentRunners builds the runner adapters. exe is the anthill program,
// which the hooks run, log receives the agents' activity, and commands
// replace a runner's CLI, for tests.
func agentRunners(exe string, log io.Writer, commands map[domain.Runner]string) (map[domain.Runner]ports.AgentRunner, error) {
	claude, err := claudecode.New(claudecode.Options{Anthill: exe, Command: commands[domain.RunnerClaudeCode], Log: log})
	if err != nil {
		return nil, err
	}
	return map[domain.Runner]ports.AgentRunner{
		domain.RunnerClaudeCode: claude,
		domain.RunnerCodex:      codex.New(codex.Options{Command: commands[domain.RunnerCodex], Log: log}),
		domain.RunnerOpenCode:   opencode.New(opencode.Options{Command: commands[domain.RunnerOpenCode], Log: log}),
	}, nil
}

// newRunnerSmokeCommand returns anthill runner-smoke, which starts one
// run of a real agent in the current directory, outside any ant, for the
// manual checks of docs/runners.md. It stands in for the daemon: it
// takes the daemon lock and serves a control interface with one tool,
// smoke_ping, and the notices given with --notice.
func newRunnerSmokeCommand(env *environment) *cobra.Command {
	var (
		runner, resume, send string
		sendAfter, stopAfter time.Duration
		notices              []string
		command              string
	)
	cmd := &cobra.Command{
		Use:    "runner-smoke --runner <name> <brief>",
		Short:  "Start one agent run in the current directory, for the checks of docs/runners.md",
		Hidden: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &usageError{errors.New("runner-smoke needs the brief as its one argument")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := domain.Runner(runner)
			if !name.Valid() {
				return &usageError{fmt.Errorf("unknown runner %q", runner)}
			}
			if env.err != nil {
				return env.err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			s := &smokeRun{env: env, notices: notices}
			return s.run(ctx, smokeOptions{
				runner: name, command: command, brief: args[0], resume: resume,
				send: send, sendAfter: sendAfter, stopAfter: stopAfter,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&runner, "runner", "", "the runner: claude-code, codex or opencode")
	f.StringVar(&resume, "resume", "", "resume this session instead of starting a new one")
	f.StringVar(&send, "send", "", "live input to send to the agent after --send-after")
	f.DurationVar(&sendAfter, "send-after", 20*time.Second, "when to send --send")
	f.DurationVar(&stopAfter, "stop-after", 0, "stop the run after this long (0: let it end)")
	f.StringArrayVar(&notices, "notice", nil, "a notice for the agent, taken by the hook or the next tool result (repeatable)")
	f.StringVar(&command, "command", "", "the agent CLI to run instead of the runner's own (for tests)")
	_ = f.MarkHidden("command")
	return cmd
}

type smokeOptions struct {
	runner               domain.Runner
	command              string
	brief, resume, send  string
	sendAfter, stopAfter time.Duration
}

// smokeRun is the stand-in daemon of one smoke run.
type smokeRun struct {
	env *environment
	out sync.Mutex

	mu      sync.Mutex
	token   string
	notices []string
}

// say prints one line of the smoke run's own report.
func (s *smokeRun) say(format string, a ...any) {
	s.out.Lock()
	defer s.out.Unlock()
	_, _ = fmt.Fprintf(s.env.stdout, "smoke: "+format+"\n", a...)
}

func (s *smokeRun) run(ctx context.Context, o smokeOptions) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	lock, err := lockfile.Acquire(filepath.Join(s.env.home, lockFileName))
	if err != nil {
		var held *lockfile.HeldError
		if errors.As(err, &held) {
			return fmt.Errorf("stop the daemon before a smoke run: %w", err)
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	s.token = "ant_smoke_" + hex.EncodeToString(b)
	srv := control.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.register(srv)
	ln, err := control.Listen(s.env.address)
	if err != nil {
		return err
	}
	serveCtx, cancelServe := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx, ln) }()
	defer func() {
		cancelServe()
		if err := <-served; err != nil {
			s.say("control interface: %v", err)
		}
	}()

	runners, err := agentRunners(exe, &lockedWriter{mu: &s.out, w: s.env.stderr}, map[domain.Runner]string{o.runner: o.command})
	if err != nil {
		return err
	}
	r := runners[o.runner]
	if err := r.Available(ctx); err != nil {
		return fmt.Errorf("runner %s not available: %w", o.runner, err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	spec := ports.RunSpec{
		Worktree:   wd,
		Brief:      o.brief,
		Budget:     time.Hour,
		MCPCommand: []string{exe, "mcp", "--ant", s.token},
		ToolToken:  s.token,
		Env:        domain.AgentEnv(os.Environ()),
	}
	var run ports.Run
	if o.resume != "" {
		run, err = r.Resume(ctx, o.resume, spec)
	} else {
		run, err = r.Start(ctx, spec)
	}
	if err != nil {
		return err
	}
	s.say("run started with %s in %s", o.runner, wd)

	ended := make(chan ports.Exit, 1)
	go func() { ended <- run.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var sendC, stopC <-chan time.Time
	if o.send != "" {
		sendC = time.After(o.sendAfter)
	}
	if o.stopAfter > 0 {
		stopC = time.After(o.stopAfter)
	}
	session := ""
	stopRun := func(why string) {
		s.say("stopping the run (%s), grace %v", why, smokeStopGrace)
		go func() { _ = run.Stop(smokeStopGrace) }()
	}
	for {
		select {
		case <-ticker.C:
			if id := run.SessionID(); id != session {
				session = id
				s.say("session id: %s", id)
			}
		case <-sendC:
			if err := run.Send(ctx, o.send); err != nil {
				s.say("live input refused: %v", err)
			} else {
				s.say("live input sent")
			}
		case <-stopC:
			stopRun("--stop-after")
			stopC = nil
		case <-ctx.Done():
			stopRun("interrupted")
			ctx = context.Background()
		case exit := <-ended:
			s.report(run, exit)
			return nil
		}
	}
}

// report prints how the run ended and its usage.
func (s *smokeRun) report(run ports.Run, exit ports.Exit) {
	s.say("run ended: exit code %d, crashed %v", exit.Code, exit.Crashed)
	s.say("session id: %s", run.SessionID())
	u := run.Usage()
	tokens, cost := "unknown", "unknown"
	if u.Tokens != nil {
		tokens = fmt.Sprint(*u.Tokens)
	}
	if u.CostUSD != nil {
		cost = fmt.Sprintf("%.4f", *u.CostUSD)
	}
	s.say("usage: %s tokens, %s USD", tokens, cost)
}

// register serves ant.tools, ant.call and ant.notices for the smoke
// run's token.
func (s *smokeRun) register(srv *control.Server) {
	check := func(raw json.RawMessage) error {
		var p antToolsParams
		if err := decodeParams(raw, &p); err != nil {
			return err
		}
		if p.Token != s.token {
			return &control.Error{Code: codeUnknownToken, Message: "unknown or revoked tool token"}
		}
		return nil
	}
	srv.Handle(methodAntTools, func(_ context.Context, raw json.RawMessage, _ control.Stream) (any, error) {
		if err := check(raw); err != nil {
			return nil, err
		}
		return antToolsResult{Tools: []antTool{{
			Name:        smokeTool,
			Description: "Answers pong. Anthill's smoke test of the ant tools.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}}}, nil
	})
	srv.Handle(methodAntCall, func(_ context.Context, raw json.RawMessage, _ control.Stream) (any, error) {
		if err := check(raw); err != nil {
			return nil, err
		}
		var p antCallParams
		_ = json.Unmarshal(raw, &p)
		s.say("the agent called %s", p.Tool)
		if p.Tool != smokeTool {
			return antCallResult{Text: "unknown tool " + p.Tool, IsError: true}, nil
		}
		// Like the ant tools, a notice the hook has not taken goes with
		// the next tool result.
		n := s.takeNotices()
		if len(n) > 0 {
			s.say("the tool result carries %d notice(s)", len(n))
		}
		return antCallResult{Text: "pong", Notices: n}, nil
	})
	srv.Handle(methodAntNotices, func(_ context.Context, raw json.RawMessage, _ control.Stream) (any, error) {
		if err := check(raw); err != nil {
			return nil, err
		}
		n := s.takeNotices()
		if len(n) > 0 {
			s.say("the hook took %d notice(s)", len(n))
		}
		return antNoticesResult{Notices: append([]string{}, n...)}, nil
	})
}

// takeNotices takes the queued notices, once.
func (s *smokeRun) takeNotices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notices
	s.notices = nil
	return n
}

// lockedWriter writes to w under mu, so the agent's log lines and the
// smoke report do not mix within a line.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
