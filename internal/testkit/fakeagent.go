package testkit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The first argument that makes a test binary run as the fake agent, or
// as a process the fake agent started. RunFakeAgentIfRequested looks for
// them.
const (
	FakeAgentFlag  = "-anthill-fake-agent"
	fakeChildFlag  = "-anthill-fake-agent-child"
	fakeChildQuiet = "ignore-interrupt"
)

// The operations of an AgentStep.
const (
	// StepTool calls Tool with Args on the ant tools server, started from
	// AgentScript.MCPCommand on the first call.
	StepTool = "tool"
	// StepCommit writes Text to File, or to a new file, and commits it
	// with Message.
	StepCommit = "commit"
	// StepUsage prints Tokens and CostUSD.
	StepUsage = "usage"
	// StepPrint prints Text as it is.
	StepPrint = "print"
	// StepReadInput reads one line from standard input and prints it.
	StepReadInput = "read_input"
	// StepSpawnChild starts a process that stays idle for an hour,
	// sharing the agent's output, and prints its pid.
	StepSpawnChild = "spawn_child"
	// StepIgnoreInterrupt makes the agent, and the children it starts
	// after this step, ignore SIGTERM and CTRL_BREAK.
	StepIgnoreInterrupt = "ignore_interrupt"
	// StepExitOnInterrupt makes the agent print an "interrupted" event
	// and exit with Code when it is asked to end, as an agent saving its
	// session does.
	StepExitOnInterrupt = "exit_on_interrupt"
	// StepIdle does nothing for Duration.
	StepIdle = "idle"
	// StepExit exits with Code.
	StepExit = "exit"
	// StepCrash ends the agent abnormally: SIGKILL on Unix, an access
	// violation status on Windows.
	StepCrash = "crash"
)

// AgentScript is what the fake agent does, in order. After the last
// step it exits with code 0.
type AgentScript struct {
	// MCPCommand is the command line of the ant tools server, such as
	// RunSpec.MCPCommand.
	MCPCommand []string    `json:"mcp_command,omitempty"`
	Steps      []AgentStep `json:"steps"`
}

// AgentStep is one step of an AgentScript. Op says which fields it
// uses.
type AgentStep struct {
	Op       string          `json:"op"`
	Tool     string          `json:"tool,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Message  string          `json:"message,omitempty"`
	File     string          `json:"file,omitempty"`
	Text     string          `json:"text,omitempty"`
	Tokens   int64           `json:"tokens,omitempty"`
	CostUSD  float64         `json:"cost_usd,omitempty"`
	Duration string          `json:"duration,omitempty"`
	Code     int             `json:"code,omitempty"`
}

// AgentEvent is one line the fake agent prints on standard output.
type AgentEvent struct {
	// Event is start, tool, commit, usage, input, child, interrupted or
	// error.
	Event   string  `json:"event"`
	Pid     int     `json:"pid,omitempty"`
	Tool    string  `json:"tool,omitempty"`
	Text    string  `json:"text,omitempty"`
	IsError bool    `json:"is_error,omitempty"`
	Tokens  int64   `json:"tokens,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// FakeAgentArgs returns the arguments that run the current test binary
// as the fake agent following script. The test's TestMain must call
// RunFakeAgentIfRequested. The program is os.Executable().
func FakeAgentArgs(script AgentScript) []string {
	b, err := json.Marshal(script)
	if err != nil {
		panic(err)
	}
	return []string{FakeAgentFlag, string(b)}
}

// RunFakeAgentIfRequested runs the fake agent, or a process it started,
// and exits, when the program's first argument asks for it. Otherwise it
// returns. Call it first in TestMain.
func RunFakeAgentIfRequested() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case FakeAgentFlag:
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "fake agent: want one script argument")
			os.Exit(2)
		}
		os.Exit(runFakeAgent(os.Args[2]))
	case fakeChildFlag:
		if len(os.Args) > 2 && os.Args[2] == fakeChildQuiet {
			signal.Ignore(os.Interrupt, syscall.SIGTERM)
		}
		time.Sleep(time.Hour)
		os.Exit(0)
	}
}

// fakeAgent runs one script. It never reads a terminal: its only input
// is the script and, for read_input, its standard input.
type fakeAgent struct {
	script  AgentScript
	out     *json.Encoder
	in      *bufio.Reader
	tools   *mcp.ClientSession
	ignored bool
}

func runFakeAgent(raw string) int {
	a := &fakeAgent{out: json.NewEncoder(os.Stdout), in: bufio.NewReader(os.Stdin)}
	if err := json.Unmarshal([]byte(raw), &a.script); err != nil {
		a.emit(AgentEvent{Event: "error", Text: "reading the script: " + err.Error()})
		return 2
	}
	defer a.close()
	a.emit(AgentEvent{Event: "start", Pid: os.Getpid()})
	for _, s := range a.script.Steps {
		code, done, err := a.step(s)
		if err != nil {
			a.emit(AgentEvent{Event: "error", Text: fmt.Sprintf("%s: %v", s.Op, err)})
			return 2
		}
		if done {
			return code
		}
	}
	return 0
}

func (a *fakeAgent) emit(e AgentEvent) {
	_ = a.out.Encode(e)
}

func (a *fakeAgent) close() {
	if a.tools != nil {
		_ = a.tools.Close()
	}
}

// step runs one step. done is set when the agent must exit with code.
func (a *fakeAgent) step(s AgentStep) (code int, done bool, err error) {
	switch s.Op {
	case StepTool:
		return 0, false, a.callTool(s)
	case StepCommit:
		return 0, false, a.commit(s)
	case StepUsage:
		a.emit(AgentEvent{Event: "usage", Tokens: s.Tokens, CostUSD: s.CostUSD})
	case StepPrint:
		fmt.Println(s.Text)
	case StepReadInput:
		line, err := a.in.ReadString('\n')
		if err != nil && line == "" {
			return 0, false, err
		}
		a.emit(AgentEvent{Event: "input", Text: strings.TrimRight(line, "\r\n")})
	case StepSpawnChild:
		args := []string{fakeChildFlag}
		if a.ignored {
			args = append(args, fakeChildQuiet)
		}
		exe, err := os.Executable()
		if err != nil {
			return 0, false, err
		}
		cmd := exec.Command(exe, args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return 0, false, err
		}
		a.emit(AgentEvent{Event: "child", Pid: cmd.Process.Pid})
	case StepIgnoreInterrupt:
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		a.ignored = true
	case StepExitOnInterrupt:
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-ch
			a.emit(AgentEvent{Event: "interrupted"})
			os.Exit(s.Code)
		}()
	case StepIdle:
		d, err := time.ParseDuration(s.Duration)
		if err != nil {
			return 0, false, err
		}
		time.Sleep(d)
	case StepExit:
		return s.Code, true, nil
	case StepCrash:
		a.close()
		crash()
	default:
		return 0, false, fmt.Errorf("unknown step %q", s.Op)
	}
	return 0, false, nil
}

// callTool calls a tool on the ant tools server, which it starts on the
// first call, and prints the result.
func (a *fakeAgent) callTool(s AgentStep) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if a.tools == nil {
		if len(a.script.MCPCommand) == 0 {
			return errors.New("the script has no MCP command")
		}
		cmd := exec.Command(a.script.MCPCommand[0], a.script.MCPCommand[1:]...)
		cmd.Stderr = os.Stderr
		client := mcp.NewClient(&mcp.Implementation{Name: "fake-agent", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			return fmt.Errorf("connecting to the ant tools: %w", err)
		}
		a.tools = session
	}
	var args any
	if len(s.Args) > 0 {
		args = s.Args
	}
	res, err := a.tools.CallTool(ctx, &mcp.CallToolParams{Name: s.Tool, Arguments: args})
	if err != nil {
		return err
	}
	var texts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, t.Text)
		}
	}
	a.emit(AgentEvent{Event: "tool", Tool: s.Tool, Text: strings.Join(texts, "\n"), IsError: res.IsError})
	return nil
}

// commit writes a file in the working directory and commits it with
// git, as an agent does.
func (a *fakeAgent) commit(s AgentStep) error {
	file := s.File
	if file == "" {
		file = fmt.Sprintf("fake-%d.txt", time.Now().UnixNano())
	}
	if err := os.WriteFile(file, []byte(s.Text+"\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "--", file}, {"commit", "--quiet", "-m", s.Message}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	a.emit(AgentEvent{Event: "commit", Text: s.Message})
	return nil
}
