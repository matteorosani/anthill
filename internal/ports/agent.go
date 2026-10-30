package ports

import (
	"context"
	"time"
)

// Capabilities are what a runner supports (design D6). Anthill picks the
// means of delivering messages from them (design D5).
type Capabilities struct {
	// Resume: a later run can continue a saved session.
	Resume bool
	// LiveInput: Run.Send works while the run is active.
	LiveInput bool
	// Hooks: the runner can refuse commands and inject context.
	Hooks bool
	// Usage: the runner reports tokens or cost.
	Usage bool
}

// RunSpec is what a run starts with.
type RunSpec struct {
	// Worktree is the run's working directory (design D17).
	Worktree string
	// Brief is the first input: the task brief, or the input of a resumed
	// session such as an answer or stored changes.
	Brief string
	// Budget is the time left; the runner may pass it on, but Anthill
	// enforces it (spec ant-timebox).
	Budget time.Duration
	// MCPCommand is the command line of the ant tools server, such as
	// anthill mcp --ant <token> (design D5).
	MCPCommand []string
	// ToolToken is the run's tool token, which MCPCommand also carries.
	// A runner with hooks gives it to the hook that takes the run's
	// notices (design D5). It never goes in the brief.
	ToolToken string
	// Env is the agent's environment, without provider tokens (design D4).
	Env []string
	// Telemetry switches on the agent's own telemetry export, for a
	// runner that supports it (spec observability, "Agent telemetry is
	// switched on and linked"). It is nil when export is off.
	Telemetry *AgentTelemetry
}

// AgentTelemetry is where an agent exports its telemetry, and what links
// it to the Anthill run (design D18).
type AgentTelemetry struct {
	// Endpoint is the OTLP endpoint, and Protocol its protocol, such as
	// grpc or http/protobuf.
	Endpoint string
	Protocol string
	// Headers are the OTLP headers from the OS credential store, such as
	// an authentication token for the endpoint. The agent can read them:
	// they are a telemetry credential, never a provider or git one
	// (design D18).
	Headers map[string]string
	// ResourceAttributes are anthill.item, anthill.ant_id and
	// anthill.instance.
	ResourceAttributes map[string]string
	// TraceParent is the run's W3C trace context, or empty.
	TraceParent string
}

// Usage is what a runner reported for a run or round. A nil field is
// unknown, which is different from zero (spec ant-execution, "Usage is
// recorded").
type Usage struct {
	Tokens  *int64
	CostUSD *float64
}

// Exit is how a run's process ended.
type Exit struct {
	Code int
	// Crashed is set when the process was killed by a signal or ended
	// abnormally.
	Crashed bool
}

// AgentRunner starts agent processes for one runner, such as Claude Code
// (design D6).
type AgentRunner interface {
	Capabilities() Capabilities
	// Available returns an error when the runner's CLI cannot be started
	// on this machine, such as when it is not installed (spec
	// ant-execution, "Runner not installed").
	Available(ctx context.Context) error
	// Start starts a run with a new session.
	Start(ctx context.Context, spec RunSpec) (Run, error)
	// Resume starts a run that continues sessionID with spec.Brief as its
	// input (spec ant-execution, "Sessions are persisted for
	// resumption"). It returns ErrUnsupported when the runner cannot
	// resume.
	Resume(ctx context.Context, sessionID string, spec RunSpec) (Run, error)
}

// Run is one active agent process.
type Run interface {
	// SessionID is the runner's session id, captured from its output when
	// the run starts (design D17). It is empty until the runner reports it.
	SessionID() string
	// Send gives input to the active run, such as the budget warning. It
	// returns ErrUnsupported without live input (design D5).
	Send(ctx context.Context, input string) error
	// Stop ends the run's whole process tree, killing it after grace
	// (design D6).
	Stop(grace time.Duration) error
	// Wait blocks until the process has ended.
	Wait() Exit
	// Usage is what the runner reported so far.
	Usage() Usage
}
