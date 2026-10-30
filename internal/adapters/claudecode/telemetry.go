package claudecode

import (
	"github.com/matteorosani/anthill/internal/adapters/internal/agentenv"
	"github.com/matteorosani/anthill/internal/ports"
)

// telemetryEnv returns the variables of Claude Code's OpenTelemetry
// export (design D18). Prompt and tool detail logging are always off
// (spec observability, "Content is never exported"); export itself is on
// only with t.
func telemetryEnv(t *ports.AgentTelemetry) []string {
	env := []string{"OTEL_LOG_USER_PROMPTS=0", "OTEL_LOG_TOOL_DETAILS=0"}
	if t == nil {
		return env
	}
	env = append(env,
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_LOGS_EXPORTER=otlp",
	)
	return append(env, agentenv.OTLP(t)...)
}
