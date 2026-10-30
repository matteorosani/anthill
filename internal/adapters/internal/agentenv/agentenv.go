// Package agentenv builds the environment of an agent process for the
// runner adapters: it sets variables over the agent's environment, and
// writes the standard OpenTelemetry variables that link an agent's own
// telemetry to its Anthill run (design D18).
package agentenv

import (
	"net/url"
	"runtime"
	"slices"
	"strings"

	"github.com/matteorosani/anthill/internal/ports"
)

// With returns env with the variables of set, which replace any earlier
// value of the same name.
func With(env, set []string) []string {
	names := map[string]bool{}
	for _, kv := range set {
		name, _, _ := strings.Cut(kv, "=")
		names[key(name)] = true
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !names[key(name)] {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}

// OTLP returns the standard OpenTelemetry variables of t: the endpoint,
// its protocol and headers, the resource attributes and the trace
// context. The headers are a telemetry credential the agent can read,
// never a provider or git one (design D18).
func OTLP(t *ports.AgentTelemetry) []string {
	env := []string{"OTEL_EXPORTER_OTLP_ENDPOINT=" + t.Endpoint}
	if t.Protocol != "" {
		env = append(env, "OTEL_EXPORTER_OTLP_PROTOCOL="+t.Protocol)
	}
	if len(t.Headers) > 0 {
		env = append(env, "OTEL_EXPORTER_OTLP_HEADERS="+KeyValues(t.Headers))
	}
	if len(t.ResourceAttributes) > 0 {
		env = append(env, "OTEL_RESOURCE_ATTRIBUTES="+KeyValues(t.ResourceAttributes))
	}
	if t.TraceParent != "" {
		env = append(env, "TRACEPARENT="+t.TraceParent)
	}
	return env
}

// KeyValues formats the OpenTelemetry key=value,… list, with the values
// percent-encoded, in key order.
func KeyValues(m map[string]string) string {
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

// key is how the system compares variable names: without case on
// Windows.
func key(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
