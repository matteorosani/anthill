package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// memLogs is an in-memory log exporter.
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

func (m *memLogs) all() []sdklog.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sdklog.Record(nil), m.records...)
}

// telemetryEnv is a Telemetry over in-memory exporters, with no
// goroutine of its own, so it works in a synctest bubble.
type telemetryEnv struct {
	tel    *app.Telemetry
	spans  *tracetest.InMemoryExporter
	reader *sdkmetric.ManualReader
	logs   *memLogs
}

func newTelemetryEnv(t *testing.T, export *ports.AgentTelemetry) *telemetryEnv {
	t.Helper()
	e := &telemetryEnv{spans: tracetest.NewInMemoryExporter(), reader: sdkmetric.NewManualReader(), logs: &memLogs{}}
	tel, err := app.NewTelemetry(app.TelemetryDeps{
		Tracer:      sdktrace.NewTracerProvider(sdktrace.WithSyncer(e.spans)),
		Meter:       sdkmetric.NewMeterProvider(sdkmetric.WithReader(e.reader)),
		Logger:      sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(e.logs))),
		AgentExport: export,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.tel = tel
	return e
}

func (e *telemetryEnv) spansNamed(name string) tracetest.SpanStubs {
	var out tracetest.SpanStubs
	for _, s := range e.spans.GetSpans() {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func spanAttr(s tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// metric returns the data points of the named metric.
func (e *telemetryEnv) metric(t *testing.T, name string) metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := e.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m.Data
			}
		}
	}
	t.Fatalf("no metric %s", name)
	return nil
}

func hasAttrs(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.String() != v {
			return false
		}
	}
	return true
}

// withTelemetry wires tel into the supervisor as the composition root
// does: the run span, the end of runs through the usage recorder, and
// the activity.
func withTelemetry(tel *app.Telemetry) func(*app.AntSupervisorDeps) {
	return func(d *app.AntSupervisorDeps) {
		d.Telemetry = tel
		d.Runs = app.NewUsageRecorder(tel.RunLog(d.Store.(ports.RunLog)))
		d.Activity = tel.Activity(d.Activity)
	}
}

// Task 18.2, spec observability, "Ant run trace": the run's span has
// child spans for push and open_pull_request, with anthill.item=gh-12
// and the ant id. Task 18.3: "Usage metrics" and "Captured agent log
// line with code".
func TestAntRunTrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		te := newTelemetryEnv(t, nil)
		e := newSupEnv(t, 2, withTelemetry(te.tel))
		id := e.mustAccept(12)
		rec := e.wantState(id, domain.AntRunning)
		x := e.execOf(ghRef(12))
		if x.Spec.Run.Telemetry != nil {
			t.Errorf("agent telemetry %+v with export off", x.Spec.Run.Telemetry)
		}
		token := x.Spec.Run.ToolToken

		e.git.Commit(rec.Worktree, "Fix the timeout")
		for _, c := range []struct{ tool, args string }{
			{domain.ToolPush, `{}`},
			{domain.ToolOpenPullRequest, `{"title":"Fix login timeout","body":"Raises the timeout."}`},
		} {
			if res := e.tools.Call(e.ctx, token, c.tool, json.RawMessage(c.args)); res.IsError {
				t.Fatalf("%s: %s", c.tool, res.Text)
			}
		}

		// An activity line with file content.
		x.Emit(ports.ExecEvent{Kind: ports.ExecActivity, Line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"main.go","content":"package main // SECRET-CODE"}}]}}`})
		tokens, cost := int64(1200), 0.42
		x.Emit(ports.ExecEvent{Kind: ports.ExecUsage, Usage: ports.Usage{Tokens: &tokens, CostUSD: &cost}})
		e.settle()
		e.end(ghRef(12), ports.ExecExited)

		runs := te.spansNamed(domain.SpanRun)
		if len(runs) != 1 {
			t.Fatalf("%d run spans, want 1", len(runs))
		}
		run := runs[0]
		antID := strconv.FormatInt(int64(id), 10)
		for key, want := range map[string]string{domain.AttrItem: "gh-12", domain.AttrAntID: antID, domain.AttrRunner: "claude-code", domain.AttrInstance: instanceA} {
			if v, _ := spanAttr(run, key); v.String() != want {
				t.Errorf("run span %s = %q, want %q", key, v.String(), want)
			}
		}
		tools := map[string]bool{}
		for _, s := range te.spansNamed(domain.SpanToolCall) {
			tool, _ := spanAttr(s, domain.AttrTool)
			tools[tool.String()] = true
			if s.Parent.SpanID() != run.SpanContext.SpanID() || s.SpanContext.TraceID() != run.SpanContext.TraceID() {
				t.Errorf("%s span is not a child of the run span", tool.String())
			}
			if v, _ := spanAttr(s, domain.AttrItem); v.String() != "gh-12" {
				t.Errorf("%s span item = %q", tool.String(), v.String())
			}
			if v, _ := spanAttr(s, domain.AttrAntID); v.String() != antID {
				t.Errorf("%s span ant id = %q", tool.String(), v.String())
			}
		}
		if !tools[domain.ToolPush] || !tools[domain.ToolOpenPullRequest] {
			t.Errorf("tool call spans = %v, want push and open_pull_request", tools)
		}

		// Usage metrics: tokens and cost tagged with the item, runner and
		// repository.
		tags := map[string]string{domain.AttrItem: "gh-12", domain.AttrRunner: "claude-code", domain.AttrRepo: "acme/shop"}
		tok := te.metric(t, domain.MetricRunTokens).(metricdata.Sum[int64])
		if len(tok.DataPoints) != 1 || tok.DataPoints[0].Value != 1200 || !hasAttrs(tok.DataPoints[0].Attributes, tags) {
			t.Errorf("tokens = %+v", tok.DataPoints)
		}
		cst := te.metric(t, domain.MetricRunCost).(metricdata.Sum[float64])
		if len(cst.DataPoints) != 1 || cst.DataPoints[0].Value != 0.42 || !hasAttrs(cst.DataPoints[0].Attributes, tags) {
			t.Errorf("cost = %+v", cst.DataPoints)
		}
		ended := te.metric(t, domain.MetricRuns).(metricdata.Sum[int64])
		if len(ended.DataPoints) != 1 || !hasAttrs(ended.DataPoints[0].Attributes, map[string]string{domain.AttrOutcome: string(ports.OutcomeFailed)}) {
			t.Errorf("runs = %+v", ended.DataPoints)
		}
		if d := te.metric(t, domain.MetricRunDuration).(metricdata.Histogram[float64]); len(d.DataPoints) != 1 {
			t.Errorf("run duration = %+v", d.DataPoints)
		}
		if g := te.metric(t, domain.MetricActiveRuns).(metricdata.Gauge[int64]); len(g.DataPoints) != 1 || g.DataPoints[0].Value != 0 {
			t.Errorf("active runs after the end = %+v", g.DataPoints)
		}

		// The captured line: event type, tool name and time only, with
		// the run's trace id and the ant id.
		logs := te.logs.all()
		if len(logs) != 1 {
			t.Fatalf("%d activity records, want 1", len(logs))
		}
		r := logs[0]
		if r.TraceID() != run.SpanContext.TraceID() {
			t.Errorf("record trace id %s, want the run's %s", r.TraceID(), run.SpanContext.TraceID())
		}
		got := map[string]string{"body": r.Body().String()}
		r.WalkAttributes(func(kv attribute.KeyValue) bool {
			got[string(kv.Key)] = kv.Value.String()
			return true
		})
		if got[domain.AttrAntID] != antID || got[domain.AttrTool] != "Write" || got[domain.AttrEvent] != "assistant" {
			t.Errorf("record = %v", got)
		}
		for k, v := range got {
			if strings.Contains(v, "SECRET-CODE") || strings.Contains(v, "main.go") {
				t.Errorf("record %s holds content: %q", k, v)
			}
		}
	})
}

// Task 18.4, spec observability, "Agent telemetry is switched on and
// linked": with export on, the run starts with the endpoint, the
// headers, the Anthill resource attributes and the run's trace context.
func TestRunStartsWithAgentTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		export := &ports.AgentTelemetry{Endpoint: "http://localhost:4318", Protocol: "http/protobuf", Headers: map[string]string{"Authorization": "Bearer k"}}
		te := newTelemetryEnv(t, export)
		e := newSupEnv(t, 2, withTelemetry(te.tel))
		id := e.mustAccept(12)
		at := e.execOf(ghRef(12)).Spec.Run.Telemetry
		if at == nil {
			t.Fatal("no agent telemetry with export on")
		}
		if at.Endpoint != export.Endpoint || at.Protocol != export.Protocol || at.Headers["Authorization"] != "Bearer k" {
			t.Errorf("agent telemetry = %+v", at)
		}
		want := map[string]string{domain.AttrItem: "gh-12", domain.AttrAntID: strconv.FormatInt(int64(id), 10), domain.AttrInstance: instanceA}
		for k, v := range want {
			if at.ResourceAttributes[k] != v {
				t.Errorf("resource attribute %s = %q, want %q", k, at.ResourceAttributes[k], v)
			}
		}
		e.end(ghRef(12), ports.ExecExited)
		run := te.spansNamed(domain.SpanRun)[0]
		if wantTP := "00-" + run.SpanContext.TraceID().String() + "-" + run.SpanContext.SpanID().String() + "-01"; at.TraceParent != wantTP {
			t.Errorf("traceparent = %q, want %q", at.TraceParent, wantTP)
		}
	})
}

// A nil *Telemetry does nothing and its decorators return what they
// wrap.
func TestNilTelemetry(t *testing.T) {
	var tel *app.Telemetry
	ctx := context.Background()
	if got, end := tel.StartPoll(ctx, "github"); got != ctx {
		t.Error("StartPoll changed the context")
	} else {
		end(app.PollReport{}, nil)
	}
	if tel.StartRun(ctx, app.RunTelemetry{}) != nil {
		t.Error("StartRun returned agent telemetry")
	}
	if tel.RunLog(nil) != nil || tel.Tracker("x", nil) != nil || tel.Activity(nil) != nil {
		t.Error("a decorator of a nil Telemetry wrapped its input")
	}
	_, endTool := tel.StartToolCall(ctx, 1, domain.ToolPush)
	endTool(false)
	if trace.SpanFromContext(ctx).SpanContext().IsValid() {
		t.Error("a span was started")
	}
}

// Task 18.2 and 18.3: a poll cycle's span is the parent of its provider
// calls, which record their latency and errors per connection.
func TestPollAndProviderTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		te := newTelemetryEnv(t, nil)
		e := newPollEnv(t, domain.SettingValues{})
		for i, c := range e.conns {
			e.conns[i].Tracker = te.tel.Tracker(c.Name, c.Tracker)
			e.conns[i].CodeHost = te.tel.CodeHost(c.Name, c.CodeHost)
		}
		e.gh.PutItem(optedIn(ghRef(12), "Fix login timeout"))
		e.ado.Fail(testkit.OpItems, errors.New("503 Service Unavailable"))
		ctx, cancel := context.WithCancel(context.Background())
		s := app.NewScheduler(e.ingestion, e.config, e.clock, &testkit.Notifier{}, e.conns...)
		s.SetTelemetry(te.tel)
		done := make(chan struct{})
		go func() {
			s.Run(ctx)
			close(done)
		}()
		synctest.Wait()
		cancel()
		<-done

		polls := te.spansNamed(domain.SpanPoll)
		if len(polls) != 2 {
			t.Fatalf("%d poll spans, want one per connection", len(polls))
		}
		byConn := map[string]tracetest.SpanStub{}
		for _, p := range polls {
			v, _ := spanAttr(p, domain.AttrProvider)
			byConn[v.String()] = p
		}
		if byConn["azure-devops/contoso"].Status.Code != codes.Error || byConn["github"].Status.Code == codes.Error {
			t.Errorf("poll statuses: ado %v, github %v", byConn["azure-devops/contoso"].Status, byConn["github"].Status)
		}
		calls := te.spansNamed(domain.SpanProviderCall)
		if len(calls) == 0 {
			t.Fatal("no provider call spans")
		}
		for _, c := range calls {
			conn, _ := spanAttr(c, domain.AttrProvider)
			if c.Parent.SpanID() != byConn[conn.String()].SpanContext.SpanID() {
				t.Errorf("provider call %v is not a child of its poll", c.Attributes)
			}
		}
		errs := te.metric(t, domain.MetricProviderErrors).(metricdata.Sum[int64])
		if len(errs.DataPoints) != 1 || !hasAttrs(errs.DataPoints[0].Attributes, map[string]string{domain.AttrProvider: "azure-devops/contoso", domain.AttrCall: "items"}) {
			t.Errorf("provider errors = %+v", errs.DataPoints)
		}
		if lat := te.metric(t, domain.MetricProviderLatency).(metricdata.Histogram[float64]); len(lat.DataPoints) == 0 {
			t.Error("no provider latency")
		}
		if d := te.metric(t, domain.MetricPollDuration).(metricdata.Histogram[float64]); len(d.DataPoints) != 2 {
			t.Errorf("poll durations = %+v", d.DataPoints)
		}
	})
}

// Task 18.2 and 18.3: the acceptance's eligibility re-check and claim
// get spans, and claims won and lost are counted.
func TestEligibilityAndClaimTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		te := newTelemetryEnv(t, nil)
		var eligibility *app.Eligibility
		e := newSupEnv(t, 2, withTelemetry(te.tel), func(d *app.AntSupervisorDeps) {
			eligibility = d.Eligibility.(*app.Eligibility)
		})
		eligibility.SetTelemetry(te.tel)
		e.claiming.SetTelemetry(te.tel)
		e.mustAccept(12)

		el := te.spansNamed(domain.SpanEligibility)
		if len(el) == 0 {
			t.Fatal("no eligibility span")
		}
		if v, _ := spanAttr(el[0], domain.AttrItem); v.String() != "gh-12" {
			t.Errorf("eligibility item = %q", v.String())
		}
		if v, _ := spanAttr(el[0], "anthill.eligible"); !v.AsBool() {
			t.Error("eligibility span does not say eligible")
		}
		cl := te.spansNamed(domain.SpanClaim)
		if len(cl) != 1 {
			t.Fatalf("%d claim spans, want 1", len(cl))
		}
		for key, want := range map[string]string{domain.AttrItem: "gh-12", domain.AttrResult: domain.ClaimResultWon, domain.AttrInstance: instanceA, domain.AttrClaim: string(domain.ClaimTask)} {
			if v, _ := spanAttr(cl[0], key); v.String() != want {
				t.Errorf("claim span %s = %q, want %q", key, v.String(), want)
			}
		}

		// A lost race counts as lost.
		_, end := te.tel.StartClaim(context.Background(), ghRef(13), domain.ClaimTask)
		end(ports.HeldClaim{}, &app.ClaimTakenError{Ref: ghRef(13), Holder: "lbianchi@DESKTOP-7"})
		claims := te.metric(t, domain.MetricClaims).(metricdata.Sum[int64])
		got := map[string]int64{}
		for _, dp := range claims.DataPoints {
			v, _ := dp.Attributes.Value(domain.AttrResult)
			got[v.String()] += dp.Value
		}
		if got[domain.ClaimResultWon] != 1 || got[domain.ClaimResultLost] != 1 {
			t.Errorf("claims = %v, want one won and one lost", got)
		}
	})
}

type fakeRoundRunner struct{ findings []domain.Finding }

func (f fakeRoundRunner) Run(context.Context, app.RoundStart) (app.RoundResult, error) {
	return app.RoundResult{Findings: f.findings}, nil
}

// Task 18.2 and 18.3: a shepherd round gets a span with its PR, and
// rounds and findings by severity are counted.
func TestRoundTelemetry(t *testing.T) {
	te := newTelemetryEnv(t, nil)
	rounds := te.tel.Rounds(fakeRoundRunner{findings: []domain.Finding{
		{Severity: domain.SeverityBlocking}, {Severity: domain.SeverityNit}, {Severity: domain.SeverityNit},
	}})
	start := app.RoundStart{Round: ports.RoundRecord{ID: 3, PR: ghPR(34), Requested: true}, Runner: domain.RunnerClaudeCode, Instance: instanceA}
	if _, err := rounds.Run(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	spans := te.spansNamed(domain.SpanRound)
	if len(spans) != 1 {
		t.Fatalf("%d round spans", len(spans))
	}
	for key, want := range map[string]string{domain.AttrPR: "gh-pr-34", domain.AttrRoundKind: "requested", domain.AttrRunner: "claude-code", domain.AttrInstance: instanceA} {
		if v, _ := spanAttr(spans[0], key); v.String() != want {
			t.Errorf("round span %s = %q, want %q", key, v.String(), want)
		}
	}
	if r := te.metric(t, domain.MetricRounds).(metricdata.Sum[int64]); len(r.DataPoints) != 1 || r.DataPoints[0].Value != 1 {
		t.Errorf("rounds = %+v", r.DataPoints)
	}
	f := te.metric(t, domain.MetricFindings).(metricdata.Sum[int64])
	got := map[string]int64{}
	for _, dp := range f.DataPoints {
		v, _ := dp.Attributes.Value(domain.AttrSeverity)
		got[v.String()] = dp.Value
	}
	if got["blocking"] != 1 || got["nit"] != 2 {
		t.Errorf("findings = %v", got)
	}
}

// Task 18.4: a review agent starts with its own telemetry, with
// anthill.pr and anthill.instance, no ant id, and its round's trace
// context.
func TestReviewAgentTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		export := &ports.AgentTelemetry{Endpoint: "http://localhost:4318", Protocol: "http/protobuf"}
		te := newTelemetryEnv(t, export)
		e := newRoundEnv(t, "feature/login", func(d *app.ShepherdRoundsDeps) { d.Telemetry = te.tel })
		tp := sdktrace.NewTracerProvider()
		ctx, span := tp.Tracer("t").Start(e.ctx, domain.SpanRound)
		var at *ports.AgentTelemetry
		if _, err := e.round(ctx, func(x *testkit.FakeExecution) {
			at = x.Spec.Run.Telemetry
			e.submit(x, "Looks good.")
		}); err != nil {
			t.Fatal(err)
		}
		span.End()
		if at == nil {
			t.Fatal("no agent telemetry with export on")
		}
		want := map[string]string{domain.AttrPR: "gh-pr-34", domain.AttrInstance: instanceA}
		if len(at.ResourceAttributes) != len(want) {
			t.Errorf("resource attributes = %v, want %v", at.ResourceAttributes, want)
		}
		for k, v := range want {
			if at.ResourceAttributes[k] != v {
				t.Errorf("resource attribute %s = %q, want %q", k, at.ResourceAttributes[k], v)
			}
		}
		if !strings.Contains(at.TraceParent, span.SpanContext().TraceID().String()) {
			t.Errorf("traceparent %q is not the round's trace", at.TraceParent)
		}
	})
}

// Task 18.3: the rate limit gauge reads the poll state that anthill
// status shows.
func TestRateLimitGauge(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	reset := time.Now().Add(10 * time.Minute)
	tel, err := app.NewTelemetry(app.TelemetryDeps{
		Tracer: sdktrace.NewTracerProvider(), Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), Logger: sdklog.NewLoggerProvider(),
		PollStatuses: func(context.Context) ([]ports.PollStatus, error) {
			return []ports.PollStatus{{Connection: "github", RateLimitedUntil: reset}, {Connection: "azure-devops/contoso"}}, nil
		},
	})
	if err != nil || tel == nil {
		t.Fatal(err)
	}
	te := &telemetryEnv{reader: reader}
	g := te.metric(t, domain.MetricRateLimitReset).(metricdata.Gauge[float64])
	got := map[string]float64{}
	for _, dp := range g.DataPoints {
		v, _ := dp.Attributes.Value(domain.AttrProvider)
		got[v.String()] = dp.Value
	}
	if got["github"] < 590 || got["github"] > 600 || got["azure-devops/contoso"] != 0 || len(got) != 2 {
		t.Errorf("rate limit reset = %v", got)
	}
}

// reportingTracker is a tracker that reports its remaining rate limit.
type reportingTracker struct {
	ports.TrackerPort
	remaining int64
}

func (r reportingTracker) RateLimitRemaining() (int64, bool) { return r.remaining, true }

// Task 18.3: the remaining rate limit of each connection whose provider
// port reports one is a gauge.
func TestRateLimitRemainingGauge(t *testing.T) {
	te := newTelemetryEnv(t, nil)
	gh := testkit.NewProvider(testkit.NewClock(t0))
	te.tel.Tracker("github", reportingTracker{TrackerPort: gh.Client("mrosani"), remaining: 4321})
	te.tel.Tracker("azure-devops/contoso", gh.Client("mrosani"))
	g := te.metric(t, domain.MetricRateLimitRemaining).(metricdata.Gauge[int64])
	if len(g.DataPoints) != 1 || g.DataPoints[0].Value != 4321 ||
		!hasAttrs(g.DataPoints[0].Attributes, map[string]string{domain.AttrProvider: "github"}) {
		t.Errorf("remaining = %+v, want 4321 for github only", g.DataPoints)
	}
}

// The provider ports found through the routes, such as by the ant
// tools, get provider call spans named by their connection.
func TestRouteTelemetry(t *testing.T) {
	te := newTelemetryEnv(t, nil)
	gh := testkit.NewProvider(testkit.NewClock(t0))
	client := gh.Client("mrosani")
	tracker := te.tel.TrackerRoute(func(context.Context, domain.Ref) (ports.TrackerPort, error) { return client, nil })
	codeHost := te.tel.CodeHostRoute(func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return client, nil })
	ctx := context.Background()
	tr, err := tracker(ctx, ghRef(12))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = tr.Item(ctx, ghRef(12))
	ch, err := codeHost(ctx, shop)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = ch.PullRequest(ctx, ghPR(34))
	calls := te.spansNamed(domain.SpanProviderCall)
	if len(calls) != 2 {
		t.Fatalf("%d provider call spans, want 2", len(calls))
	}
	for _, c := range calls {
		if v, _ := spanAttr(c, domain.AttrProvider); v.String() != domain.ConnectionName(shop) {
			t.Errorf("provider = %q, want %q", v.String(), domain.ConnectionName(shop))
		}
	}
}
