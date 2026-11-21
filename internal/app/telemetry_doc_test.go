package app_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// docNames returns the names in the first column of the table under the
// "## <section>" heading of docs/observability.md.
func docNames(t *testing.T, section string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "observability.md"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "## ") {
			in = line == "## "+section
			continue
		}
		if !in || !strings.HasPrefix(line, "| `") {
			continue
		}
		cell := strings.TrimSpace(strings.Split(line, "|")[1])
		names = append(names, strings.Trim(cell, "`"))
	}
	slices.Sort(names)
	return names
}

type recordingRunLog struct{ ports.RunLog }

func (recordingRunLog) EndRun(context.Context, ports.RunRecord) error { return nil }

// Task 18.5: the metrics and spans in docs/observability.md are the ones
// the code emits, found by using every instrument.
func TestObservabilityDocMatchesTheCode(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	tel, err := app.NewTelemetry(app.TelemetryDeps{
		Tracer:     sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans)),
		Meter:      sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		Logger:     sdklog.NewLoggerProvider(),
		QueuedAnts: func(context.Context) (int, error) { return 1, nil },
		PollStatuses: func(context.Context) ([]ports.PollStatus, error) {
			return []ports.PollStatus{{Connection: "github"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	pollCtx, endPoll := tel.StartPoll(ctx, "github")
	gh := testkit.NewProvider(testkit.NewClock(t0))
	client := gh.Client("mrosani")
	if _, _, err := tel.Tracker("github", reportingTracker{TrackerPort: client, remaining: 10}).Item(pollCtx, ghRef(99)); err == nil {
		t.Fatal("reading a missing item succeeded")
	}
	if _, _, err := tel.CodeHost("github", client).PullRequest(pollCtx, ghPR(99)); err == nil {
		t.Fatal("reading a missing PR succeeded")
	}
	endPoll(app.PollReport{Polled: 1}, nil)
	_, endEl := tel.StartEligibility(ctx, ghRef(12))
	endEl(domain.EligibilityResult{}, nil)
	_, endClaim := tel.StartClaim(ctx, ghRef(12), domain.ClaimTask)
	endClaim(ports.HeldClaim{Instance: instanceA}, nil)

	tel.StartRun(ctx, app.RunTelemetry{Item: ghRef(12), Ant: 1, Run: 1, Runner: domain.RunnerClaudeCode, Repo: shop})
	_, endTool := tel.StartToolCall(ctx, 1, domain.ToolPush)
	endTool(false)
	tokens, cost := int64(10), 0.01
	if err := tel.RunLog(recordingRunLog{}).EndRun(ctx, ports.RunRecord{
		ID: 1, Ant: 1, StartedAt: t0, EndedAt: t0.Add(time.Minute), Outcome: ports.OutcomeComplete,
		Usage: ports.Usage{Tokens: &tokens, CostUSD: &cost},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tel.Rounds(fakeRoundRunner{findings: []domain.Finding{{Severity: domain.SeverityNit}}}).Run(ctx, app.RoundStart{Round: ports.RoundRecord{PR: ghPR(34)}}); err != nil {
		t.Fatal(err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	var metrics []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			metrics = append(metrics, m.Name)
		}
	}
	slices.Sort(metrics)
	if doc := docNames(t, "Metrics"); !slices.Equal(doc, metrics) {
		t.Errorf("docs/observability.md metrics:\n%v\nthe code emits:\n%v", doc, metrics)
	}

	var names []string
	for _, s := range spans.GetSpans() {
		if !slices.Contains(names, s.Name) {
			names = append(names, s.Name)
		}
	}
	slices.Sort(names)
	if doc := docNames(t, "Traces"); !slices.Equal(doc, names) {
		t.Errorf("docs/observability.md spans:\n%v\nthe code emits:\n%v", doc, names)
	}
}
