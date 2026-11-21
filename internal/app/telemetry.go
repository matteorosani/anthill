package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// TelemetryScope is the instrumentation scope of Anthill's spans,
// metrics and agent activity records.
const TelemetryScope = "github.com/matteorosani/anthill"

// TelemetryDeps are the OpenTelemetry providers the telemetry adapter
// sets up, no-ops when export is off (spec observability, "Export is off
// until configured"), and the sources of the gauges.
type TelemetryDeps struct {
	Tracer trace.TracerProvider
	Meter  metric.MeterProvider
	Logger log.LoggerProvider
	// AgentExport is where agents export their own telemetry, or nil
	// when export is off (design D18).
	AgentExport *ports.AgentTelemetry
	// QueuedAnts counts the queued ants, for the queued ants gauge; nil
	// leaves the gauge out.
	QueuedAnts func(ctx context.Context) (int, error)
	// PollStatuses lists each connection's poll state, the source of the
	// rate limit that anthill status shows, for the rate limit gauge; nil
	// leaves the gauge out.
	PollStatuses func(ctx context.Context) ([]ports.PollStatus, error)
}

// Telemetry emits Anthill's traces, metrics and agent activity records
// (spec observability). The services call it at the points the spec
// names; the decorators in telemetry_*.go wrap ports and service
// dependencies so most services need no change. A nil *Telemetry does
// nothing, so services built without one are unaffected.
type Telemetry struct {
	tracer      trace.Tracer
	logger      log.Logger
	agentExport *ports.AgentTelemetry

	runDuration     metric.Float64Histogram
	runs            metric.Int64Counter
	runTokens       metric.Int64Counter
	runCost         metric.Float64Counter
	providerLatency metric.Float64Histogram
	providerErrors  metric.Int64Counter
	pollDuration    metric.Float64Histogram
	claims          metric.Int64Counter
	rounds          metric.Int64Counter
	findings        metric.Int64Counter

	mu sync.Mutex
	// active holds the runs whose span is open, by run ID: from the start
	// of the run to the record of its end.
	active map[ports.RunID]*runTrace
	// rateLimits are the provider ports that report their remaining rate
	// limit, by connection, found by the Tracker and CodeHost decorators.
	rateLimits map[string]ports.RateLimitReporter
}

// runTrace is an active run's span and what its metrics and records are
// tagged with.
type runTrace struct {
	ctx  context.Context
	span trace.Span
	info RunTelemetry
}

// RunTelemetry is what a run's span, metrics and agent telemetry carry.
type RunTelemetry struct {
	Item     domain.Ref
	Ant      ports.AntID
	Run      ports.RunID
	Kind     domain.AgentRole
	Runner   domain.Runner
	Repo     domain.Repository
	Instance string
}

// NewTelemetry creates the instruments. A nil provider is a no-op.
func NewTelemetry(deps TelemetryDeps) (*Telemetry, error) {
	if deps.Tracer == nil || deps.Meter == nil || deps.Logger == nil {
		return nil, errors.New("telemetry needs a tracer, meter and logger provider")
	}
	t := &Telemetry{
		tracer:      deps.Tracer.Tracer(TelemetryScope),
		logger:      deps.Logger.Logger(TelemetryScope),
		agentExport: deps.AgentExport,
		active:      map[ports.RunID]*runTrace{},
		rateLimits:  map[string]ports.RateLimitReporter{},
	}
	m := deps.Meter.Meter(TelemetryScope)
	var errs []error
	add := func(err error) { errs = append(errs, err) }
	var err error
	t.runDuration, err = m.Float64Histogram(domain.MetricRunDuration, metric.WithUnit("s"),
		metric.WithDescription("Duration of each ant run"))
	add(err)
	t.runs, err = m.Int64Counter(domain.MetricRuns, metric.WithUnit("{run}"),
		metric.WithDescription("Ant runs ended, by outcome"))
	add(err)
	t.runTokens, err = m.Int64Counter(domain.MetricRunTokens, metric.WithUnit("{token}"),
		metric.WithDescription("Tokens of each ant run, where the runner reports them"))
	add(err)
	t.runCost, err = m.Float64Counter(domain.MetricRunCost, metric.WithUnit("USD"),
		metric.WithDescription("Cost of each ant run, where the runner reports it"))
	add(err)
	t.providerLatency, err = m.Float64Histogram(domain.MetricProviderLatency, metric.WithUnit("s"),
		metric.WithDescription("Latency of each provider call"))
	add(err)
	t.providerErrors, err = m.Int64Counter(domain.MetricProviderErrors, metric.WithUnit("{error}"),
		metric.WithDescription("Provider calls that failed"))
	add(err)
	t.pollDuration, err = m.Float64Histogram(domain.MetricPollDuration, metric.WithUnit("s"),
		metric.WithDescription("Duration of each poll cycle of a provider connection"))
	add(err)
	t.claims, err = m.Int64Counter(domain.MetricClaims, metric.WithUnit("{claim}"),
		metric.WithDescription("Claim attempts won and lost"))
	add(err)
	t.rounds, err = m.Int64Counter(domain.MetricRounds, metric.WithUnit("{round}"),
		metric.WithDescription("Shepherd review rounds"))
	add(err)
	t.findings, err = m.Int64Counter(domain.MetricFindings, metric.WithUnit("{finding}"),
		metric.WithDescription("Findings of shepherd rounds, by severity"))
	add(err)
	_, err = m.Int64ObservableGauge(domain.MetricActiveRuns, metric.WithUnit("{run}"),
		metric.WithDescription("Active runs on this instance"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			t.mu.Lock()
			n := len(t.active)
			t.mu.Unlock()
			o.Observe(int64(n))
			return nil
		}))
	add(err)
	_, err = m.Int64ObservableGauge(domain.MetricRateLimitRemaining, metric.WithUnit("{request}"),
		metric.WithDescription("Requests left in a provider's rate limit window, as its latest response said"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			t.mu.Lock()
			reporters := maps.Clone(t.rateLimits)
			t.mu.Unlock()
			for conn, r := range reporters {
				if n, ok := r.RateLimitRemaining(); ok {
					o.Observe(n, metric.WithAttributes(attribute.String(domain.AttrProvider, conn)))
				}
			}
			return nil
		}))
	add(err)
	if deps.QueuedAnts != nil {
		_, err = m.Int64ObservableGauge(domain.MetricQueuedAnts, metric.WithUnit("{ant}"),
			metric.WithDescription("Ants queued for capacity on this instance"),
			metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
				n, err := deps.QueuedAnts(ctx)
				if err != nil {
					return err
				}
				o.Observe(int64(n))
				return nil
			}))
		add(err)
	}
	if deps.PollStatuses != nil {
		_, err = m.Float64ObservableGauge(domain.MetricRateLimitReset, metric.WithUnit("s"),
			metric.WithDescription("Time until a provider's exhausted rate limit resets, 0 when polling is not paused for it"),
			metric.WithFloat64Callback(func(ctx context.Context, o metric.Float64Observer) error {
				statuses, err := deps.PollStatuses(ctx)
				if err != nil {
					return err
				}
				now := time.Now()
				for _, s := range statuses {
					o.Observe(max(s.RateLimitedUntil.Sub(now).Seconds(), 0),
						metric.WithAttributes(attribute.String(domain.AttrProvider, s.Connection)))
				}
				return nil
			}))
		add(err)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("creating the telemetry instruments: %w", err)
	}
	return t, nil
}

// endSpan ends span with err as its status.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "")
		span.SetAttributes(attribute.String(domain.AttrResult, "error"))
	}
	span.End()
}

// StartPoll starts the span of one poll cycle of a connection. The
// returned function ends it with the poll's report.
func (t *Telemetry) StartPoll(ctx context.Context, connection string) (context.Context, func(PollReport, error)) {
	if t == nil {
		return ctx, func(PollReport, error) {}
	}
	start := time.Now()
	attrs := []attribute.KeyValue{attribute.String(domain.AttrProvider, connection)}
	ctx, span := t.tracer.Start(ctx, domain.SpanPoll, trace.WithAttributes(attrs...))
	return ctx, func(r PollReport, err error) {
		span.SetAttributes(attribute.Int("anthill.polled", r.Polled), attribute.Int("anthill.failed", len(r.Failed)),
			attribute.Int("anthill.events", r.Events))
		if err == nil && len(r.Failed) > 0 {
			err = errors.New("poll failed")
		}
		t.pollDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(append(attrs, resultAttr(err))...))
		endSpan(span, err)
	}
}

// StartEligibility starts the span of the evaluation of an item's
// eligibility. The returned function ends it with the result.
func (t *Telemetry) StartEligibility(ctx context.Context, item domain.Ref) (context.Context, func(domain.EligibilityResult, error)) {
	if t == nil {
		return ctx, func(domain.EligibilityResult, error) {}
	}
	ctx, span := t.tracer.Start(ctx, domain.SpanEligibility, trace.WithAttributes(attribute.String(domain.AttrItem, item.String())))
	return ctx, func(r domain.EligibilityResult, err error) {
		if err == nil {
			span.SetAttributes(attribute.Bool("anthill.eligible", r.Eligible()))
		}
		endSpan(span, err)
	}
}

// StartClaim starts the span of one claim attempt. The returned function
// ends it with the attempt's result: won without an error, lost with a
// *ClaimTakenError.
func (t *Telemetry) StartClaim(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) (context.Context, func(ports.HeldClaim, error)) {
	if t == nil {
		return ctx, func(ports.HeldClaim, error) {}
	}
	attrs := []attribute.KeyValue{refAttr(ref), attribute.String(domain.AttrClaim, string(kind))}
	ctx, span := t.tracer.Start(ctx, domain.SpanClaim, trace.WithAttributes(attrs...))
	return ctx, func(h ports.HeldClaim, err error) {
		kindAttr := attribute.String(domain.AttrClaim, string(kind))
		var taken *ClaimTakenError
		switch {
		case err == nil:
			span.SetAttributes(attribute.String(domain.AttrResult, domain.ClaimResultWon))
			if h.Instance != "" {
				span.SetAttributes(attribute.String(domain.AttrInstance, h.Instance))
			}
			t.claims.Add(ctx, 1, metric.WithAttributes(kindAttr, attribute.String(domain.AttrResult, domain.ClaimResultWon)))
			span.End()
		case errors.As(err, &taken):
			span.SetAttributes(attribute.String(domain.AttrResult, domain.ClaimResultLost))
			t.claims.Add(ctx, 1, metric.WithAttributes(kindAttr, attribute.String(domain.AttrResult, domain.ClaimResultLost)))
			span.End()
		default:
			endSpan(span, err)
		}
	}
}

// StartRun starts the span of a run, the root of the run's own trace
// (design D18), linked to the span of ctx if any, and returns the agent
// telemetry to start the run with: nil when export is off. The span ends
// when the run's end is recorded through RunLog.
func (t *Telemetry) StartRun(ctx context.Context, info RunTelemetry) *ports.AgentTelemetry {
	if t == nil {
		return nil
	}
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithAttributes(runAttrs(info)...)}
	if parent := trace.SpanContextFromContext(ctx); parent.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: parent}))
	}
	runCtx, span := t.tracer.Start(context.WithoutCancel(ctx), domain.SpanRun, opts...)
	t.mu.Lock()
	t.active[info.Run] = &runTrace{ctx: runCtx, span: span, info: info}
	t.mu.Unlock()
	attrs := map[string]string{
		domain.AttrItem:  info.Item.String(),
		domain.AttrAntID: strconv.FormatInt(int64(info.Ant), 10),
	}
	if info.Instance != "" {
		attrs[domain.AttrInstance] = info.Instance
	}
	return t.agentTelemetry(runCtx, attrs)
}

// PRAgentTelemetry returns the agent telemetry of a review or fix agent
// on pr, started under the span of ctx, such as its round's: the
// resource attributes anthill.pr and anthill.instance, and no ant id,
// since such an agent belongs to no ant. It is nil when export is off.
func (t *Telemetry) PRAgentTelemetry(ctx context.Context, pr domain.Ref, instance string) *ports.AgentTelemetry {
	if t == nil {
		return nil
	}
	attrs := map[string]string{domain.AttrPR: pr.String()}
	if instance != "" {
		attrs[domain.AttrInstance] = instance
	}
	return t.agentTelemetry(ctx, attrs)
}

func (t *Telemetry) agentTelemetry(ctx context.Context, attrs map[string]string) *ports.AgentTelemetry {
	if t.agentExport == nil {
		return nil
	}
	at := *t.agentExport
	at.Headers = maps.Clone(t.agentExport.Headers)
	at.ResourceAttributes = maps.Clone(attrs)
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	at.TraceParent = carrier.Get("traceparent")
	return &at
}

// runOf returns the active run's trace, or nil.
func (t *Telemetry) runOf(id ports.RunID) *runTrace {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active[id]
}

// StartToolCall starts the span of an ant tool call of run, a child of
// the run's span. Calls of a review or fix agent (run 0) get a span of
// their own under ctx. The returned function ends it with whether the
// call failed or was refused.
func (t *Telemetry) StartToolCall(ctx context.Context, run ports.RunID, tool string) (context.Context, func(failed bool)) {
	if t == nil {
		return ctx, func(bool) {}
	}
	attrs := []attribute.KeyValue{attribute.String(domain.AttrTool, tool)}
	parent := ctx
	if r := t.runOf(run); r != nil && run != 0 {
		attrs = append(attrs, runAttrs(r.info)...)
		parent = trace.ContextWithSpanContext(ctx, r.span.SpanContext())
	}
	ctx, span := t.tracer.Start(parent, domain.SpanToolCall, trace.WithAttributes(attrs...))
	return ctx, func(failed bool) {
		if failed {
			span.SetStatus(codes.Error, "")
		}
		span.End()
	}
}

// endRun ends the span of run r and records its metrics: duration and
// outcome, and tokens and cost where known (spec observability,
// "Metrics", "Usage metrics").
func (t *Telemetry) endRun(r ports.RunRecord) {
	t.mu.Lock()
	rt := t.active[r.ID]
	delete(t.active, r.ID)
	t.mu.Unlock()
	if rt == nil {
		return
	}
	ctx := rt.ctx
	tags := []attribute.KeyValue{
		attribute.String(domain.AttrItem, rt.info.Item.String()),
		attribute.String(domain.AttrRunner, string(rt.info.Runner)),
		attribute.String(domain.AttrRepo, rt.info.Repo.String()),
	}
	outcome := attribute.String(domain.AttrOutcome, string(r.Outcome))
	t.runs.Add(ctx, 1, metric.WithAttributes(append(tags, outcome)...))
	t.runDuration.Record(ctx, r.Duration().Seconds(), metric.WithAttributes(append(tags, outcome)...))
	if r.Usage.Tokens != nil {
		t.runTokens.Add(ctx, *r.Usage.Tokens, metric.WithAttributes(tags...))
		rt.span.SetAttributes(attribute.Int64("anthill.tokens", *r.Usage.Tokens))
	}
	if r.Usage.CostUSD != nil {
		t.runCost.Add(ctx, *r.Usage.CostUSD, metric.WithAttributes(tags...))
		rt.span.SetAttributes(attribute.Float64("anthill.cost_usd", *r.Usage.CostUSD))
	}
	rt.span.SetAttributes(outcome)
	if r.Outcome == ports.OutcomeFailed {
		rt.span.SetStatus(codes.Error, "")
	}
	rt.span.End()
}

// agentActivity exports one captured line of a run's agent activity as a
// log record linked to the run's trace, with the ant id, and only the
// event type, tool name and time (spec observability, "Logs", "Content is
// never exported").
func (t *Telemetry) agentActivity(run ports.RunID, line string) {
	rt := t.runOf(run)
	if rt == nil {
		return
	}
	event, tool := domain.ActivityEvent(line)
	var rec log.Record
	now := time.Now()
	rec.SetTimestamp(now)
	rec.SetObservedTimestamp(now)
	rec.SetSeverity(log.SeverityInfo)
	rec.SetEventName(AgentActivityEvent)
	rec.SetBody(attribute.StringValue(event))
	rec.AddAttributes(runAttrs(rt.info)...)
	rec.AddAttributes(attribute.String(domain.AttrEvent, event))
	if tool != "" {
		rec.AddAttributes(attribute.String(domain.AttrTool, tool))
	}
	t.logger.Emit(rt.ctx, rec)
}

// AgentActivityEvent is the event name of an agent activity record.
const AgentActivityEvent = "anthill.agent.activity"

func runAttrs(info RunTelemetry) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(domain.AttrItem, info.Item.String()),
		attribute.Int64(domain.AttrAntID, int64(info.Ant)),
		attribute.Int64(domain.AttrRunID, int64(info.Run)),
	}
	if info.Kind != "" {
		attrs = append(attrs, attribute.String(domain.AttrRunKind, string(info.Kind)))
	}
	if info.Runner != "" {
		attrs = append(attrs, attribute.String(domain.AttrRunner, string(info.Runner)))
	}
	if info.Repo != (domain.Repository{}) {
		attrs = append(attrs, attribute.String(domain.AttrRepo, info.Repo.String()))
	}
	if info.Instance != "" {
		attrs = append(attrs, attribute.String(domain.AttrInstance, info.Instance))
	}
	return attrs
}

// refAttr tags an item or a PR.
func refAttr(ref domain.Ref) attribute.KeyValue {
	if ref.Kind == domain.KindPR {
		return attribute.String(domain.AttrPR, ref.String())
	}
	return attribute.String(domain.AttrItem, ref.String())
}

func resultAttr(err error) attribute.KeyValue {
	if err != nil {
		return attribute.String(domain.AttrResult, "error")
	}
	return attribute.String(domain.AttrResult, "ok")
}

// SetTelemetry makes each claim attempt emit a span and count as won or
// lost (spec observability). Call it before the first claim.
func (c *Claiming) SetTelemetry(t *Telemetry) {
	c.telemetry = t
}

// SetTelemetry makes each eligibility evaluation emit a span (spec
// observability, "Traces"). Call it before the first evaluation.
func (e *Eligibility) SetTelemetry(t *Telemetry) {
	e.telemetry = t
}

// startRunTelemetry starts the span of a run of ant rec and returns the
// agent telemetry the run starts with, nil when export is off (spec
// observability, "Agent telemetry is switched on and linked").
func (s *AntSupervisor) startRunTelemetry(ctx context.Context, rec ports.AntRecord, tw ToolWorkspace, run ports.RunID, kind domain.AgentRole) *ports.AgentTelemetry {
	if s.deps.Telemetry == nil {
		return nil
	}
	// The instance only tags the telemetry: a claim that cannot be read
	// here leaves it out rather than failing the start.
	instance, _ := s.instance(ctx, rec.Item)
	return s.deps.Telemetry.StartRun(ctx, RunTelemetry{
		Item: rec.Item, Ant: rec.ID, Run: run, Kind: kind,
		Runner: domain.Runner(rec.Runner), Repo: tw.Repo, Instance: instance,
	})
}

// SetTelemetry gives each tool call a span, a child of its run's span
// (spec observability, "Ant run trace"). Call it before the first call.
func (t *AntTools) SetTelemetry(tel *Telemetry) {
	t.telemetry = tel
}
