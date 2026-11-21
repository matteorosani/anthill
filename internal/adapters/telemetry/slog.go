package telemetry

import (
	"context"
	"log/slog"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"

	"github.com/matteorosani/anthill/internal/domain"
)

// LoggerName is the instrumentation scope of Anthill's application logs.
const LoggerName = "github.com/matteorosani/anthill"

// LogHandler returns a slog.Handler that exports each record through lp,
// linked to the trace and span of the context it is logged with (spec
// observability, "Logs"). Secrets are redacted from the message and from
// every string or error attribute by the redactor in use. With export off
// lp is a no-op and so is the handler.
func LogHandler(lp log.LoggerProvider, redactor func() *domain.Redactor) slog.Handler {
	return &logHandler{logger: lp.Logger(LoggerName), redactor: redactor}
}

type logHandler struct {
	logger   log.Logger
	redactor func() *domain.Redactor
	// attrs are the handler's attributes, with their group prefix.
	attrs  []attribute.KeyValue
	groups []string
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.logger.Enabled(ctx, log.EnabledParameters{Severity: severity(level)})
}

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	var rec log.Record
	rec.SetTimestamp(r.Time)
	rec.SetSeverity(severity(r.Level))
	rec.SetSeverityText(r.Level.String())
	rec.SetBody(attribute.StringValue(h.redact(r.Message)))
	rec.AddAttributes(h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		rec.AddAttributes(h.convert(h.groups, a)...)
		return true
	})
	// The SDK takes the trace and span ids from ctx.
	h.logger.Emit(ctx, rec)
	return nil
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = slices.Clone(h.attrs)
	for _, a := range attrs {
		c.attrs = append(c.attrs, h.convert(h.groups, a)...)
	}
	return &c
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.groups = append(slices.Clone(h.groups), name)
	return &c
}

func (h *logHandler) redact(s string) string {
	if h.redactor == nil {
		return s
	}
	if r := h.redactor(); r != nil {
		return r.Redact(s)
	}
	return s
}

// convert turns a slog attribute into log key-values, flattening groups
// into dotted keys.
func (h *logHandler) convert(groups []string, a slog.Attr) []attribute.KeyValue {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return nil
	}
	if a.Value.Kind() == slog.KindGroup {
		inner := groups
		if a.Key != "" {
			inner = append(slices.Clone(groups), a.Key)
		}
		var out []attribute.KeyValue
		for _, g := range a.Value.Group() {
			out = append(out, h.convert(inner, g)...)
		}
		return out
	}
	key := a.Key
	for i := len(groups) - 1; i >= 0; i-- {
		key = groups[i] + "." + key
	}
	return []attribute.KeyValue{{Key: attribute.Key(key), Value: h.value(a.Value)}}
}

func (h *logHandler) value(v slog.Value) attribute.Value {
	switch v.Kind() {
	case slog.KindBool:
		return attribute.BoolValue(v.Bool())
	case slog.KindInt64:
		return attribute.Int64Value(v.Int64())
	case slog.KindUint64:
		return attribute.Int64Value(int64(v.Uint64())) //nolint:gosec // counters fit in int64
	case slog.KindFloat64:
		return attribute.Float64Value(v.Float64())
	case slog.KindDuration:
		return attribute.Int64Value(v.Duration().Nanoseconds())
	case slog.KindTime:
		return attribute.StringValue(v.Time().UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return attribute.StringValue(h.redact(err.Error()))
		}
	}
	return attribute.StringValue(h.redact(v.String()))
}

func severity(l slog.Level) log.Severity {
	switch {
	case l >= slog.LevelError:
		return log.SeverityError
	case l >= slog.LevelWarn:
		return log.SeverityWarn
	case l >= slog.LevelInfo:
		return log.SeverityInfo
	default:
		return log.SeverityDebug
	}
}
