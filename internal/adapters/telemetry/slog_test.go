package telemetry_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/matteorosani/anthill/internal/adapters/telemetry"
	"github.com/matteorosani/anthill/internal/domain"
)

type memLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (m *memLogs) Export(_ context.Context, rs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rs {
		m.records = append(m.records, r.Clone())
	}
	return nil
}
func (m *memLogs) Shutdown(context.Context) error   { return nil }
func (m *memLogs) ForceFlush(context.Context) error { return nil }

// Task 18.3: application logs are exported linked to the active span,
// with secrets redacted from the message and the attributes.
func TestLogHandlerLinksAndRedacts(t *testing.T) {
	logs := &memLogs{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(logs)))
	redactor, err := domain.NewRedactor([]string{`ACME-[0-9]{4}`})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(telemetry.LogHandler(lp, func() *domain.Redactor { return redactor })).
		With("component", "claims").WithGroup("claim")

	ctx, span := sdktrace.NewTracerProvider().Tracer("t").Start(context.Background(), "claim")
	logger.WarnContext(ctx, "lost claim ACME-1234", "item", "gh-12", "error", errors.New("token ACME-9999 refused"), "attempt", 2)
	span.End()

	if len(logs.records) != 1 {
		t.Fatalf("%d records, want 1", len(logs.records))
	}
	r := logs.records[0]
	if r.TraceID() != span.SpanContext().TraceID() || r.SpanID() != span.SpanContext().SpanID() {
		t.Error("the record is not linked to the active span")
	}
	if got := r.Body().String(); got != "lost claim "+domain.Redacted {
		t.Errorf("body = %q", got)
	}
	if r.SeverityText() != "WARN" {
		t.Errorf("severity = %q", r.SeverityText())
	}
	got := map[string]attribute.Value{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		got[string(kv.Key)] = kv.Value
		return true
	})
	if got["component"].String() != "claims" || got["claim.item"].String() != "gh-12" || got["claim.attempt"].AsInt64() != 2 {
		t.Errorf("attributes = %v", got)
	}
	if e := got["claim.error"].String(); e != "token "+domain.Redacted+" refused" {
		t.Errorf("error = %q, want it redacted", e)
	}
}
