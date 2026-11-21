// Package telemetry sets up the OpenTelemetry SDK for Anthill's own
// traces, metrics and logs, exported over OTLP/HTTP (design D18; spec
// observability). Export is off until the global configuration sets an
// OTLP endpoint: Setup then returns no-op providers, and nothing leaves
// the machine. The endpoint's headers, such as an API key, come from the
// OS credential store, never from a configuration file.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Protocol is the OTLP protocol Anthill exports with, and the one it
// gives agents (OTEL_EXPORTER_OTLP_PROTOCOL).
const Protocol = "http/protobuf"

// ServiceName is the service.name resource attribute of Anthill's own
// telemetry.
const ServiceName = "anthill"

// The OTLP/HTTP paths of each signal, appended to the endpoint as the
// OTEL_EXPORTER_OTLP_ENDPOINT convention does, so Anthill and its agents
// read the same endpoint the same way.
const (
	PathTraces  = "/v1/traces"
	PathMetrics = "/v1/metrics"
	PathLogs    = "/v1/logs"
)

// MetricInterval is how often metrics are exported.
const MetricInterval = 30 * time.Second

// Config is what Setup needs.
type Config struct {
	// Endpoint is the OTLP endpoint of the global configuration, an http
	// or https URL. Empty means export is off.
	Endpoint string
	// Version is Anthill's version, the service.version resource
	// attribute.
	Version string
	// Instance is the instance id, the anthill.instance resource
	// attribute, or empty when not known yet.
	Instance string
}

// Providers are the OpenTelemetry providers the application services
// use. When export is off they are no-ops and AgentExport is nil.
type Providers struct {
	Tracer trace.TracerProvider
	Meter  metric.MeterProvider
	Logger log.LoggerProvider
	// AgentExport is where agents export their own telemetry: the same
	// endpoint, protocol and headers (design D18). It is nil when export
	// is off. Callers add the resource attributes and trace context of
	// each run.
	AgentExport *ports.AgentTelemetry

	shutdown []func(context.Context) error
}

// Enabled reports whether export is on.
func (p *Providers) Enabled() bool {
	return p.AgentExport != nil
}

// Shutdown flushes what is still buffered and stops the exporters. It
// does nothing when export is off.
func (p *Providers) Shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range p.shutdown {
		errs = append(errs, f(ctx))
	}
	return errors.Join(errs...)
}

// Setup returns the providers for cfg. Without an endpoint it returns
// no-op providers and builds no exporter, so no telemetry leaves the
// machine whatever the OTEL_* environment variables say. With one, it
// reads the OTLP headers from creds (domain.OTLPHeadersCredential); a
// missing credential means no headers. The headers are sent with every
// export and never logged: errors name the credential, not its value.
func Setup(ctx context.Context, cfg Config, creds ports.CredentialSource) (*Providers, error) {
	if cfg.Endpoint == "" {
		return &Providers{
			Tracer: tracenoop.NewTracerProvider(),
			Meter:  metricnoop.NewMeterProvider(),
			Logger: lognoop.NewLoggerProvider(),
		}, nil
	}
	base, err := endpointBase(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	headers, err := readHeaders(ctx, creds)
	if err != nil {
		return nil, err
	}
	res := resource.NewSchemaless(resourceAttributes(cfg)...)

	traces, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(base+PathTraces),
		otlptracehttp.WithHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("creating the OTLP trace exporter: %w", err)
	}
	metrics, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(base+PathMetrics),
		otlpmetrichttp.WithHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("creating the OTLP metric exporter: %w", err)
	}
	logs, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(base+PathLogs),
		otlploghttp.WithHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("creating the OTLP log exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(traces))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics, sdkmetric.WithInterval(MetricInterval))))
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)))
	return &Providers{
		Tracer: tp,
		Meter:  mp,
		Logger: lp,
		AgentExport: &ports.AgentTelemetry{
			Endpoint: base,
			Protocol: Protocol,
			Headers:  headers,
		},
		// Traces first, so the spans of the last runs are flushed before
		// the logs that refer to them.
		shutdown: []func(context.Context) error{tp.Shutdown, mp.Shutdown, lp.Shutdown},
	}, nil
}

// endpointBase checks the endpoint and returns it without a trailing
// slash, so each signal's path can be appended.
func endpointBase(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("OTLP endpoint %q must be an http or https URL", endpoint)
	}
	return strings.TrimRight(endpoint, "/"), nil
}

// readHeaders reads the OTLP headers from the credential store.
func readHeaders(ctx context.Context, creds ports.CredentialSource) (map[string]string, error) {
	if creds == nil {
		return nil, nil
	}
	key := domain.OTLPHeadersCredential()
	secret, err := creds.Credential(ctx, key)
	if errors.Is(err, ports.ErrNoCredential) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s from the credential store: %w", key, err)
	}
	headers, err := ParseHeaders(secret.Reveal())
	if err != nil {
		return nil, fmt.Errorf("the %s credential: %w", key, err)
	}
	return headers, nil
}

// ParseHeaders parses the OTEL_EXPORTER_OTLP_HEADERS format,
// k1=v1,k2=v2, with percent-encoded values. Its errors never quote a
// value, because a value is usually a secret.
func ParseHeaders(s string) (map[string]string, error) {
	headers := map[string]string{}
	for i, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("header %d is not name=value", i+1)
		}
		decoded, err := url.PathUnescape(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("header %q has a value that is not percent-encoded", name)
		}
		headers[name] = decoded
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return headers, nil
}

func resourceAttributes(cfg Config) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.String("service.name", ServiceName)}
	if cfg.Version != "" {
		attrs = append(attrs, attribute.String("service.version", cfg.Version))
	}
	if cfg.Instance != "" {
		attrs = append(attrs, attribute.String(domain.AttrInstance, cfg.Instance))
	}
	return attrs
}
