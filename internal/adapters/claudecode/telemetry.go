package claudecode

import (
	"net/url"
	"runtime"
	"slices"
	"strings"

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
		"OTEL_EXPORTER_OTLP_ENDPOINT="+t.Endpoint,
	)
	if t.Protocol != "" {
		env = append(env, "OTEL_EXPORTER_OTLP_PROTOCOL="+t.Protocol)
	}
	if len(t.Headers) > 0 {
		env = append(env, "OTEL_EXPORTER_OTLP_HEADERS="+keyValues(t.Headers))
	}
	if len(t.ResourceAttributes) > 0 {
		env = append(env, "OTEL_RESOURCE_ATTRIBUTES="+keyValues(t.ResourceAttributes))
	}
	if t.TraceParent != "" {
		env = append(env, "TRACEPARENT="+t.TraceParent)
	}
	return env
}

// keyValues formats the OpenTelemetry key=value,… list, with the values
// percent-encoded, in key order.
func keyValues(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + url.PathEscape(m[k])
	}
	return strings.Join(parts, ",")
}

// envKey is how the system compares variable names: without case on
// Windows.
func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
