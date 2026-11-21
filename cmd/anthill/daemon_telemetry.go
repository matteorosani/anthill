package main

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/keyring"
	"github.com/matteorosani/anthill/internal/adapters/telemetry"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// telemetryShutdownTimeout bounds the flush of the telemetry still
// buffered when the daemon stops.
const telemetryShutdownTimeout = 5 * time.Second

// daemonTelemetry is the daemon's telemetry: the hook the services use,
// the logger that also exports, and the shutdown that flushes both.
type daemonTelemetry struct {
	tel      *app.Telemetry
	logger   *slog.Logger
	shutdown func()
}

// startTelemetry sets up telemetry export from the global configuration
// (spec observability): off without otlp-endpoint, so that nothing
// leaves the machine, and with the OTLP headers from creds otherwise.
// The returned logger writes to w as newLogger does, and also exports
// each record. store counts the queued ants for their gauge and gives
// the poll state for the rate limit gauge.
func startTelemetry(ctx context.Context, w io.Writer, config *app.Configuration, creds ports.CredentialSource, store ports.StateStore) (daemonTelemetry, error) {
	endpoint := ""
	if snap := config.Current(); snap != nil {
		endpoint = snap.Global.OTLPEndpoint
	}
	p, err := telemetry.Setup(ctx, telemetry.Config{Endpoint: endpoint, Version: version}, creds)
	if err != nil {
		return daemonTelemetry{}, err
	}
	tel, err := app.NewTelemetry(app.TelemetryDeps{
		Tracer: p.Tracer, Meter: p.Meter, Logger: p.Logger, AgentExport: p.AgentExport,
		QueuedAnts: func(ctx context.Context) (int, error) {
			ants, err := store.Ants(ctx, domain.AntQueued)
			return len(ants), err
		},
		PollStatuses: store.PollStatuses,
	})
	if err != nil {
		return daemonTelemetry{}, err
	}
	logger := newExportLogger(w, config, telemetry.LogHandler(p.Logger, configRedactor(config)))
	return daemonTelemetry{
		tel:    tel,
		logger: logger,
		shutdown: func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryShutdownTimeout)
			defer cancel()
			if err := p.Shutdown(ctx); err != nil {
				logger.Warn("telemetry not flushed", "error", err)
			}
		},
	}, nil
}

// newExportLogger returns the daemon's logger over newLogHandler and the
// telemetry log handler, so each record is written to w and exported.
func newExportLogger(w io.Writer, config *app.Configuration, export slog.Handler) *slog.Logger {
	return slog.New(slog.NewMultiHandler(newLogHandler(w, config), export))
}

// configRedactor returns the redactor of the configuration in use, or
// the built-in one until the configuration is loaded.
func configRedactor(config *app.Configuration) func() *domain.Redactor {
	builtin, _ := domain.NewRedactor(nil)
	return func() *domain.Redactor {
		if snap := config.Current(); snap != nil && snap.Redactor != nil {
			return snap.Redactor
		}
		return builtin
	}
}

// keyringCredentials is where the daemon reads the OTLP headers: the OS
// credential store only (spec anthill-configuration).
var keyringCredentials ports.CredentialSource = keyring.Store{}
