package telemetry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/matteorosani/anthill/internal/adapters/telemetry"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// receiver is an in-process OTLP/HTTP receiver: it records every
// request it gets.
type receiver struct {
	*httptest.Server
	mu       sync.Mutex
	requests []request
}

type request struct {
	path   string
	header http.Header
	body   []byte
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, request{path: req.URL.Path, header: req.Header.Clone(), body: body})
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) got() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]request(nil), r.requests...)
}

// emit makes one span, one metric point and one log record.
func emit(ctx context.Context, p *telemetry.Providers) {
	ctx, span := p.Tracer.Tracer("test").Start(ctx, domain.SpanRun)
	counter, _ := p.Meter.Meter("test").Int64Counter(domain.MetricRuns)
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String(domain.AttrItem, "gh-12")))
	slog.New(telemetry.LogHandler(p.Logger, nil)).InfoContext(ctx, "run started", "ant", 7)
	span.End()
}

// Task 18.1, scenario "No endpoint configured": nothing is sent, even
// with the standard OTEL_* variables pointing at a receiver.
func TestNoEndpointSendsNothing(t *testing.T) {
	r := newReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", r.URL)
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	creds := &testkit.CredentialStore{}
	creds.Set(domain.OTLPHeadersCredential(), "Authorization=Bearer%20s3cret")

	ctx := context.Background()
	p, err := telemetry.Setup(ctx, telemetry.Config{}, creds)
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() || p.AgentExport != nil {
		t.Error("export is on without an endpoint")
	}
	emit(ctx, p)
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.got(); len(got) != 0 {
		t.Errorf("the receiver got %d requests without an endpoint, want none", len(got))
	}
	if len(creds.Lookups) != 0 {
		t.Errorf("the credential store was read without an endpoint: %v", creds.Lookups)
	}
}

// Task 18.1, scenarios "Endpoint configured" and "Endpoint needs an API
// key": traces, metrics and logs reach the endpoint, each with the header
// from the credential store.
func TestEndpointExportsWithHeaders(t *testing.T) {
	r := newReceiver(t)
	creds := &testkit.CredentialStore{}
	creds.Set(domain.OTLPHeadersCredential(), "Authorization=Bearer%20s3cret,X-Team=ants")

	ctx := context.Background()
	p, err := telemetry.Setup(ctx, telemetry.Config{Endpoint: r.URL + "/", Version: "1.2.3", Instance: "mrosani@LAPTOP-42"}, creds)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled() {
		t.Fatal("export is off with an endpoint")
	}
	emit(ctx, p)
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	paths := map[string]bool{}
	for _, req := range r.got() {
		paths[req.path] = true
		if got := req.header.Get("Authorization"); got != "Bearer s3cret" {
			t.Errorf("%s: Authorization = %q, want the stored key", req.path, got)
		}
		if got := req.header.Get("X-Team"); got != "ants" {
			t.Errorf("%s: X-Team = %q", req.path, got)
		}
	}
	for _, want := range []string{telemetry.PathTraces, telemetry.PathMetrics, telemetry.PathLogs} {
		if !paths[want] {
			t.Errorf("nothing was sent to %s; got %v", want, paths)
		}
	}

	// The span carries the resource attributes, and the log record is
	// linked to it.
	var traceID []byte
	for _, req := range r.got() {
		switch req.path {
		case telemetry.PathTraces:
			var m coltrace.ExportTraceServiceRequest
			if err := proto.Unmarshal(req.body, &m); err != nil {
				t.Fatal(err)
			}
			rs := m.GetResourceSpans()[0]
			attrs := map[string]string{}
			for _, kv := range rs.GetResource().GetAttributes() {
				attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
			}
			if attrs["service.name"] != "anthill" || attrs[domain.AttrInstance] != "mrosani@LAPTOP-42" || attrs["service.version"] != "1.2.3" {
				t.Errorf("resource attributes = %v", attrs)
			}
			traceID = rs.GetScopeSpans()[0].GetSpans()[0].GetTraceId()
		}
	}
	for _, req := range r.got() {
		if req.path != telemetry.PathLogs {
			continue
		}
		var m collogs.ExportLogsServiceRequest
		if err := proto.Unmarshal(req.body, &m); err != nil {
			t.Fatal(err)
		}
		rec := m.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
		if string(rec.GetTraceId()) != string(traceID) || len(traceID) == 0 {
			t.Errorf("the log record's trace id %x is not the span's %x", rec.GetTraceId(), traceID)
		}
	}

	// The agent gets the same endpoint, protocol and headers.
	ae := p.AgentExport
	if ae.Endpoint != r.URL || ae.Protocol != "http/protobuf" || ae.Headers["Authorization"] != "Bearer s3cret" {
		t.Errorf("AgentExport = %+v", ae)
	}
}

// With an endpoint and no stored headers, export works without them.
func TestEndpointWithoutHeaders(t *testing.T) {
	r := newReceiver(t)
	ctx := context.Background()
	p, err := telemetry.Setup(ctx, telemetry.Config{Endpoint: r.URL}, &testkit.CredentialStore{})
	if err != nil {
		t.Fatal(err)
	}
	emit(ctx, p)
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.got()) == 0 {
		t.Fatal("nothing was sent")
	}
	for _, req := range r.got() {
		if req.header.Get("Authorization") != "" {
			t.Errorf("%s has an Authorization header", req.path)
		}
	}
}

// A credential store failure stops the setup, and the error never holds
// the stored value.
func TestHeaderErrorsHideTheValue(t *testing.T) {
	ctx := context.Background()
	creds := &testkit.CredentialStore{Errs: map[string]error{domain.OTLPHeadersCredential().Account(): errors.New("locked")}}
	if _, err := telemetry.Setup(ctx, telemetry.Config{Endpoint: "http://localhost:4318"}, creds); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("err = %v, want the store's error", err)
	}

	creds = &testkit.CredentialStore{}
	creds.Set(domain.OTLPHeadersCredential(), "Authorization=Bearer s3cret%zz")
	_, err := telemetry.Setup(ctx, telemetry.Config{Endpoint: "http://localhost:4318"}, creds)
	if err == nil {
		t.Fatal("a malformed header was accepted")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("the error shows the value: %v", err)
	}
}

func TestParseHeaders(t *testing.T) {
	got, err := telemetry.ParseHeaders(" Authorization = Bearer%20k , x-a=b%2Cc,")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["Authorization"] != "Bearer k" || got["x-a"] != "b,c" {
		t.Errorf("got %v", got)
	}
	if _, err := telemetry.ParseHeaders("novalue"); err == nil {
		t.Error("a header without = was accepted")
	}
}

func TestBadEndpoint(t *testing.T) {
	if _, err := telemetry.Setup(context.Background(), telemetry.Config{Endpoint: "collector:4318"}, nil); err == nil {
		t.Error("an endpoint without a scheme was accepted")
	}
}
