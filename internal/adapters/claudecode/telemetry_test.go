package claudecode_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Task 18.4, spec observability, "Agent telemetry carries Anthill
// attributes" and "Agent prompt logging": the agent telemetry that
// Anthill starts a run with reaches the fake agent's environment with
// anthill.item, anthill.ant_id and anthill.instance, the run's trace
// context and the endpoint's headers, and prompt and content logging
// off.
func TestAgentTelemetryFromTheRun(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	tel, err := app.NewTelemetry(app.TelemetryDeps{
		Tracer: tp, Meter: metricnoop.NewMeterProvider(), Logger: lognoop.NewLoggerProvider(),
		AgentExport: &ports.AgentTelemetry{
			Endpoint: "http://localhost:4318", Protocol: "http/protobuf",
			Headers: map[string]string{"Authorization": "Bearer s3cret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := domain.Ref{Repo: domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}, Kind: domain.KindItem, Number: 12}
	at := tel.StartRun(context.Background(), app.RunTelemetry{
		Item: item, Ant: 7, Run: 3, Runner: domain.RunnerClaudeCode, Instance: "mrosani@LAPTOP-42",
	})

	f := newFake(t, "resume")
	spec := f.spec(t)
	spec.Telemetry = at
	run, err := f.runner.Start(context.Background(), spec, ports.RunIO{})
	if err != nil {
		t.Fatal(err)
	}
	run.Wait()
	env := f.env(t)

	var traceparent string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "TRACEPARENT="); ok {
			traceparent = v
		}
	}
	if !strings.HasPrefix(traceparent, "00-") || len(traceparent) != 55 {
		t.Errorf("TRACEPARENT = %q, want the run's W3C trace context", traceparent)
	}
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20s3cret",
		"OTEL_RESOURCE_ATTRIBUTES=anthill.ant_id=7,anthill.instance=mrosani@LAPTOP-42,anthill.item=gh-12",
		"OTEL_LOG_USER_PROMPTS=0",
		"OTEL_LOG_TOOL_DETAILS=0",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
	if slices.Contains(env, "OTEL_LOG_USER_PROMPTS=1") {
		t.Error("prompt logging is on")
	}
}
