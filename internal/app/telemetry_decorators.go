package app

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RunLog wraps the run log that the usage recorder stores in: recording
// the end of a run also ends the run's span and records its duration,
// outcome, tokens and cost, the run's own as the recorder computed them
// (spec observability, "Usage metrics"). With a nil *Telemetry it returns
// inner.
func (t *Telemetry) RunLog(inner ports.RunLog) ports.RunLog {
	if t == nil {
		return inner
	}
	return &telemetryRunLog{RunLog: inner, t: t}
}

type telemetryRunLog struct {
	ports.RunLog
	t *Telemetry
}

func (l *telemetryRunLog) EndRun(ctx context.Context, r ports.RunRecord) error {
	err := l.RunLog.EndRun(ctx, r)
	l.t.endRun(r)
	return err
}

// Activity wraps the receiver of runs' activity: each line is also
// exported as an agent activity record, linked to the run's trace (spec
// observability, "Agent activity linked to its run"). inner may be nil.
// With a nil *Telemetry it returns inner.
func (t *Telemetry) Activity(inner RunActivity) RunActivity {
	if t == nil {
		return inner
	}
	return &telemetryActivity{inner: inner, t: t}
}

type telemetryActivity struct {
	inner RunActivity
	t     *Telemetry
}

func (a *telemetryActivity) Activity(ant ports.AntID, run ports.RunID, line string) {
	if a.inner != nil {
		a.inner.Activity(ant, run, line)
	}
	a.t.agentActivity(run, line)
}

// Rounds wraps the runner of shepherd rounds: each round gets a span
// tagged with its PR, and the rounds and their findings by severity are
// counted (spec observability, "Traces", "Metrics"). With a nil
// *Telemetry it returns inner.
func (t *Telemetry) Rounds(inner RoundRunner) RoundRunner {
	if t == nil {
		return inner
	}
	return &telemetryRounds{inner: inner, t: t}
}

type telemetryRounds struct {
	inner RoundRunner
	t     *Telemetry
}

// Round kinds, the values of AttrRoundKind.
const (
	roundAutomatic = "automatic"
	roundRequested = "requested"
)

func (r *telemetryRounds) Run(ctx context.Context, start RoundStart) (RoundResult, error) {
	kind := roundAutomatic
	if start.Round.Requested {
		kind = roundRequested
	}
	repo := attribute.String(domain.AttrRepo, start.Round.PR.Repo.String())
	ctx, span := r.t.tracer.Start(ctx, domain.SpanRound, trace.WithAttributes(
		attribute.String(domain.AttrPR, start.Round.PR.String()),
		attribute.Int64(domain.AttrRound, int64(start.Round.ID)),
		attribute.String(domain.AttrRoundKind, kind),
		attribute.String(domain.AttrRunner, string(start.Runner)),
		attribute.String(domain.AttrInstance, start.Instance),
		repo,
	))
	res, err := r.inner.Run(ctx, start)
	r.t.rounds.Add(ctx, 1, metric.WithAttributes(attribute.String(domain.AttrRoundKind, kind), repo, resultAttr(err)))
	for _, f := range res.Findings {
		r.t.findings.Add(ctx, 1, metric.WithAttributes(attribute.String(domain.AttrSeverity, string(f.Severity)), repo))
	}
	span.SetAttributes(attribute.Int("anthill.findings", len(res.Findings)))
	endSpan(span, err)
	return res, err
}

// Tracker wraps a tracker port: each call gets a span and its latency and
// failures are recorded, tagged with the connection's name (spec
// observability, "Traces", "Metrics"). With a nil *Telemetry it returns
// inner.
func (t *Telemetry) Tracker(connection string, inner ports.TrackerPort) ports.TrackerPort {
	if t == nil || inner == nil {
		return inner
	}
	t.watchRateLimit(connection, inner)
	return &telemetryTracker{inner: inner, calls: providerCalls{t: t, connection: connection}}
}

// CodeHost wraps a code host port as Tracker wraps a tracker port.
func (t *Telemetry) CodeHost(connection string, inner ports.CodeHostPort) ports.CodeHostPort {
	if t == nil || inner == nil {
		return inner
	}
	t.watchRateLimit(connection, inner)
	return &telemetryCodeHost{inner: inner, calls: providerCalls{t: t, connection: connection}}
}

// watchRateLimit adds port to the remaining rate limit gauge of the
// connection when it reports one (ports.RateLimitReporter).
func (t *Telemetry) watchRateLimit(connection string, port any) {
	if r, ok := port.(ports.RateLimitReporter); ok {
		t.mu.Lock()
		t.rateLimits[connection] = r
		t.mu.Unlock()
	}
}

// providerCalls measures the calls of one provider connection.
type providerCalls struct {
	t          *Telemetry
	connection string
}

// start starts the span of one provider call on ref (zero for none). The
// returned function ends it with the call's error.
func (p providerCalls) start(ctx context.Context, call string, ref domain.Ref) (context.Context, func(error)) {
	attrs := []attribute.KeyValue{
		attribute.String(domain.AttrProvider, p.connection),
		attribute.String(domain.AttrCall, call),
	}
	spanAttrs := attrs
	if ref != (domain.Ref{}) {
		spanAttrs = append(spanAttrs, refAttr(ref))
	}
	begin := time.Now()
	ctx, span := p.t.tracer.Start(ctx, domain.SpanProviderCall, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(spanAttrs...))
	return ctx, func(err error) {
		p.t.providerLatency.Record(ctx, time.Since(begin).Seconds(), metric.WithAttributes(append(attrs, resultAttr(err))...))
		if err != nil {
			p.t.providerErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
		}
		endSpan(span, err)
	}
}

type telemetryTracker struct {
	inner ports.TrackerPort
	calls providerCalls
}

var _ ports.TrackerPort = (*telemetryTracker)(nil)

func (w *telemetryTracker) Items(ctx context.Context, repo domain.Repository, cursor string) (ports.ItemPage, error) {
	ctx, end := w.calls.start(ctx, "items", domain.Ref{})
	page, err := w.inner.Items(ctx, repo, cursor)
	end(err)
	return page, err
}

func (w *telemetryTracker) Item(ctx context.Context, ref domain.Ref) (domain.WorkItem, time.Time, error) {
	ctx, end := w.calls.start(ctx, "item", ref)
	item, at, err := w.inner.Item(ctx, ref)
	end(err)
	return item, at, err
}

func (w *telemetryTracker) AddComment(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error) {
	ctx, end := w.calls.start(ctx, "add_comment", ref)
	out, err := w.inner.AddComment(ctx, ref, c)
	end(err)
	return out, err
}

func (w *telemetryTracker) EditComment(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error) {
	ctx, end := w.calls.start(ctx, "edit_comment", ref)
	out, err := w.inner.EditComment(ctx, ref, commentID, c)
	end(err)
	return out, err
}

func (w *telemetryTracker) AddLabel(ctx context.Context, ref domain.Ref, label string) error {
	ctx, end := w.calls.start(ctx, "add_label", ref)
	err := w.inner.AddLabel(ctx, ref, label)
	end(err)
	return err
}

func (w *telemetryTracker) RemoveLabel(ctx context.Context, ref domain.Ref, label string) error {
	ctx, end := w.calls.start(ctx, "remove_label", ref)
	err := w.inner.RemoveLabel(ctx, ref, label)
	end(err)
	return err
}

func (w *telemetryTracker) CanWrite(ctx context.Context, repo domain.Repository, login string) (bool, error) {
	ctx, end := w.calls.start(ctx, "can_write", domain.Ref{})
	ok, err := w.inner.CanWrite(ctx, repo, login)
	end(err)
	return ok, err
}

type telemetryCodeHost struct {
	inner ports.CodeHostPort
	calls providerCalls
}

var _ ports.CodeHostPort = (*telemetryCodeHost)(nil)

func (w *telemetryCodeHost) PullRequests(ctx context.Context, repo domain.Repository, cursor string) (ports.PullRequestPage, error) {
	ctx, end := w.calls.start(ctx, "pull_requests", domain.Ref{})
	page, err := w.inner.PullRequests(ctx, repo, cursor)
	end(err)
	return page, err
}

func (w *telemetryCodeHost) PullRequest(ctx context.Context, ref domain.Ref) (domain.PullRequest, time.Time, error) {
	ctx, end := w.calls.start(ctx, "pull_request", ref)
	pr, at, err := w.inner.PullRequest(ctx, ref)
	end(err)
	return pr, at, err
}

func (w *telemetryCodeHost) OpenPullRequest(ctx context.Context, pr ports.NewPullRequest) (domain.PullRequest, error) {
	ctx, end := w.calls.start(ctx, "open_pull_request", domain.Ref{})
	out, err := w.inner.OpenPullRequest(ctx, pr)
	end(err)
	return out, err
}

func (w *telemetryCodeHost) UpdatePullRequest(ctx context.Context, ref domain.Ref, u ports.PullRequestUpdate) (domain.PullRequest, error) {
	ctx, end := w.calls.start(ctx, "update_pull_request", ref)
	out, err := w.inner.UpdatePullRequest(ctx, ref, u)
	end(err)
	return out, err
}

func (w *telemetryCodeHost) AddComment(ctx context.Context, ref domain.Ref, c domain.AnthillComment) (domain.Comment, error) {
	ctx, end := w.calls.start(ctx, "add_comment", ref)
	out, err := w.inner.AddComment(ctx, ref, c)
	end(err)
	return out, err
}

func (w *telemetryCodeHost) EditComment(ctx context.Context, ref domain.Ref, commentID string, c domain.AnthillComment) (domain.Comment, error) {
	ctx, end := w.calls.start(ctx, "edit_comment", ref)
	out, err := w.inner.EditComment(ctx, ref, commentID, c)
	end(err)
	return out, err
}

func (w *telemetryCodeHost) AddReviewComment(ctx context.Context, ref domain.Ref, c ports.ReviewComment) (domain.Comment, error) {
	ctx, end := w.calls.start(ctx, "add_review_comment", ref)
	out, err := w.inner.AddReviewComment(ctx, ref, c)
	end(err)
	return out, err
}

func (w *telemetryCodeHost) ReplyToThread(ctx context.Context, ref domain.Ref, threadID string, c domain.AnthillComment) (domain.Comment, error) {
	ctx, end := w.calls.start(ctx, "reply_to_thread", ref)
	out, err := w.inner.ReplyToThread(ctx, ref, threadID, c)
	end(err)
	return out, err
}

func (w *telemetryCodeHost) ResolveThread(ctx context.Context, ref domain.Ref, threadID string) error {
	ctx, end := w.calls.start(ctx, "resolve_thread", ref)
	err := w.inner.ResolveThread(ctx, ref, threadID)
	end(err)
	return err
}

func (w *telemetryCodeHost) Repositories(ctx context.Context, scope domain.Repository) ([]domain.Repository, error) {
	ctx, end := w.calls.start(ctx, "repositories", domain.Ref{})
	out, err := w.inner.Repositories(ctx, scope)
	end(err)
	return out, err
}

func (w *telemetryCodeHost) CanWrite(ctx context.Context, repo domain.Repository, login string) (bool, error) {
	ctx, end := w.calls.start(ctx, "can_write", domain.Ref{})
	ok, err := w.inner.CanWrite(ctx, repo, login)
	end(err)
	return ok, err
}

// TrackerRoute wraps a tracker route so that every tracker it finds is
// wrapped as Tracker does, named by its connection. With a nil
// *Telemetry it returns route.
func (t *Telemetry) TrackerRoute(route TrackerRoute) TrackerRoute {
	if t == nil || route == nil {
		return route
	}
	return func(ctx context.Context, item domain.Ref) (ports.TrackerPort, error) {
		p, err := route(ctx, item)
		if err != nil {
			return p, err
		}
		return t.Tracker(domain.ConnectionName(item.Repo), p), nil
	}
}

// CodeHostRoute wraps a code host route as TrackerRoute wraps a tracker
// route.
func (t *Telemetry) CodeHostRoute(route CodeHostRoute) CodeHostRoute {
	if t == nil || route == nil {
		return route
	}
	return func(ctx context.Context, repo domain.Repository) (ports.CodeHostPort, error) {
		p, err := route(ctx, repo)
		if err != nil {
			return p, err
		}
		return t.CodeHost(domain.ConnectionName(repo), p), nil
	}
}
