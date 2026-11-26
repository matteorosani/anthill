package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// AgentRun is what one execution of an AgentExecutor does.
type AgentRun struct {
	// Script is what the fake agent does. When its MCPCommand is empty,
	// the executor's MCP function makes one from the execution's tool
	// token.
	Script AgentScript
	// Prepare, when set, is called before the process starts, with the
	// execution's spec and the script, which it may change. A test makes
	// the agent's commits in the fake source control there, which the
	// fake agent's own git cannot reach, and adds steps that need what
	// only exists by then, such as the thread a reply goes to.
	Prepare func(spec ports.ExecSpec, script *AgentScript)
	// Hold, when set, starts no process: the execution stays active until
	// Anthill stops it or the test ends it, as an agent still at work.
	Hold bool
}

// AgentExecutorOptions configure an AgentExecutor.
type AgentExecutorOptions struct {
	// Dir is the working directory of the fake agent processes.
	Dir string
	// MCP returns the command line of the ant tools server for a tool
	// token, such as the test binary running anthill mcp --ant <token>.
	MCP func(token string) []string
	// Reply answers an execution without a tool token, such as a comment
	// classifier call, with its last message. When nil, or when it returns
	// false, such an execution stays active until stopped or ended.
	Reply func(spec ports.ExecSpec) (string, bool)
}

// AgentExecutor is a ports.Executor that starts the fake agent as a real
// process for each execution with a tool token: a run, a review agent or
// a fix agent. Each takes the next AgentRun queued with Queue, so its
// tool calls go through the real ant tools server. An execution with no
// queued AgentRun stays active until stopped or ended. It records every
// execution as Executor does, which it embeds: a test reads them, and
// ends held ones, through it. It reports the session id of each
// execution: the resumed one, or session-<id> for a new session. Stop
// kills the process. Close kills what is left and waits for every
// process.
type AgentExecutor struct {
	*Executor
	opts AgentExecutorOptions

	mu     sync.Mutex
	queue  []AgentRun
	procs  map[ports.ExecID]*agentProc
	closed bool
	wg     sync.WaitGroup
}

// agentProc is one fake agent process.
type agentProc struct {
	cmd    *exec.Cmd
	out    *SyncBuffer
	exited chan struct{}
}

var _ ports.Executor = (*AgentExecutor)(nil)

// NewAgentExecutor returns an executor over inner, which records the
// executions and gives the runners' capabilities.
func NewAgentExecutor(inner *Executor, opts AgentExecutorOptions) *AgentExecutor {
	return &AgentExecutor{Executor: inner, opts: opts, procs: map[ports.ExecID]*agentProc{}}
}

// Queue adds what the next executions with a tool token do, in order.
func (a *AgentExecutor) Queue(runs ...AgentRun) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queue = append(a.queue, runs...)
}

// Queued returns how many queued AgentRuns no execution has taken yet.
func (a *AgentExecutor) Queued() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.queue)
}

// Running returns how many fake agent processes have not exited yet.
func (a *AgentExecutor) Running() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, p := range a.procs {
		select {
		case <-p.exited:
		default:
			n++
		}
	}
	return n
}

// Output returns what the fake agent of the execution with id printed so
// far, or "" when it started no process.
func (a *AgentExecutor) Output(id ports.ExecID) string {
	a.mu.Lock()
	p := a.procs[id]
	a.mu.Unlock()
	if p == nil {
		return ""
	}
	return p.out.String()
}

// Start records spec as Executor does, then answers it, starts its fake
// agent, or leaves it active.
func (a *AgentExecutor) Start(ctx context.Context, spec ports.ExecSpec) (ports.Execution, error) {
	x, err := a.Executor.Start(ctx, spec)
	if err != nil {
		return x, err
	}
	f := a.Execution(x.ID)
	if spec.Run.ToolToken == "" {
		if a.opts.Reply != nil {
			if reply, ok := a.opts.Reply(spec); ok {
				f.EmitLastMessage(reply)
				f.End(ports.ExecExited, ports.Exit{})
			}
		}
		return x, nil
	}
	session := spec.Session
	if session == "" {
		session = "session-" + string(x.ID)
	}
	f.Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: session})

	a.mu.Lock()
	if len(a.queue) == 0 || a.closed {
		a.mu.Unlock()
		return x, nil
	}
	run := a.queue[0]
	a.queue = a.queue[1:]
	a.mu.Unlock()
	if run.Hold {
		return x, nil
	}
	if run.Prepare != nil {
		run.Prepare(spec, &run.Script)
	}
	if err := a.spawn(f, run.Script); err != nil {
		f.EmitLastMessage(err.Error())
		f.End(ports.ExecExited, ports.Exit{Code: 2})
	}
	return x, nil
}

// spawn starts the fake agent of f and ends f when it exits.
func (a *AgentExecutor) spawn(f *FakeExecution, script AgentScript) error {
	if len(script.MCPCommand) == 0 && a.opts.MCP != nil {
		script.MCPCommand = a.opts.MCP(f.Spec.Run.ToolToken)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p := &agentProc{out: &SyncBuffer{}, exited: make(chan struct{})}
	p.cmd = exec.Command(exe, FakeAgentArgs(script)...)
	p.cmd.Dir = a.opts.Dir
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("starting the fake agent: %w", err)
	}
	a.mu.Lock()
	a.procs[f.ID] = p
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		err := p.cmd.Wait()
		close(p.exited)
		a.mu.Lock()
		closed := a.closed
		a.mu.Unlock()
		if closed {
			// Close abandoned it: nobody watches it any more.
			return
		}
		exit := ports.Exit{}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit.Code = exitErr.ExitCode()
			exit.Crashed = exit.Code < 0
		} else if err != nil {
			exit = ports.Exit{Code: -1, Crashed: true}
		}
		f.End(ports.ExecExited, exit)
	}()
	return nil
}

// Stop kills the fake agent of the execution, if any, and ends it as
// Executor.Stop does.
func (a *AgentExecutor) Stop(id ports.ExecID, grace time.Duration) error {
	err := a.Executor.Stop(id, grace)
	a.kill(id)
	return err
}

func (a *AgentExecutor) kill(id ports.ExecID) {
	a.mu.Lock()
	p := a.procs[id]
	a.mu.Unlock()
	if p == nil {
		return
	}
	select {
	case <-p.exited:
	default:
		_ = p.cmd.Process.Kill()
	}
}

// Close kills every fake agent still running and waits until every
// process has exited, as when the daemon's own process ends. It ends no
// execution: what was active stays active, with nobody to report its
// end, so a daemon started again over the same store finds the runs the
// restart interrupted. Later executions start no process.
func (a *AgentExecutor) Close() {
	a.mu.Lock()
	a.closed = true
	ids := make([]ports.ExecID, 0, len(a.procs))
	for id := range a.procs {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.kill(id)
	}
	a.wg.Wait()
}
