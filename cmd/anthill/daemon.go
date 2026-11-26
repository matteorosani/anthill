package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/configfile"
	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/adapters/lockfile"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
)

// lockFileName is the daemon lock's file name in <anthill-home>.
const lockFileName = "daemon.lock"

func newDaemonCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the daemon in the foreground until interrupted",
		Long: "Run the daemon in the foreground. It stops on Ctrl+C or SIGTERM. " +
			"Only one daemon runs per user and machine.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if env.err != nil {
				return env.err
			}
			// Ctrl+C (os.Interrupt) or SIGTERM cancels ctx, which shuts the
			// daemon down.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runDaemon(ctx, env)
		},
	}
}

// runDaemon takes the daemon lock, loads the configuration, builds the
// adapters and the services (buildDaemon), and serves the control
// interface and runs the services until ctx is cancelled. It returns a
// *lockfile.HeldError when another daemon is running, and the
// configuration error when the global configuration is invalid.
func runDaemon(ctx context.Context, env *environment) error {
	// The redactor follows the configuration once it is loaded; until
	// then the built-in patterns apply.
	config := app.NewConfiguration(configfile.New(env.configPath))
	logger := newLogger(env.stderr, config)

	lock, err := lockfile.Acquire(filepath.Join(env.home, lockFileName))
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			logger.Warn("daemon lock not released", "error", releaseErr)
		}
	}()

	snap, err := config.Load(ctx)
	if err != nil {
		return err
	}
	build := env.ports
	if build == nil {
		build = realPorts
	}
	p, err := build(ctx, env, snap, logger)
	if err != nil {
		return err
	}
	if p.Close != nil {
		defer func() {
			if closeErr := p.Close(); closeErr != nil {
				logger.Warn("adapters not closed", "error", closeErr)
			}
		}()
	}
	tel, err := startTelemetry(ctx, env.stderr, config, keyringCredentials, p.Store)
	if err != nil {
		return err
	}
	defer tel.shutdown()
	logger = tel.logger
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the anthill program: %w", err)
	}
	notifier := app.NewNotifications(config, env.desktop(logger))
	s := buildDaemon(p, buildOptions{
		config: config, clock: env.clock, notifier: notifier, logger: logger, telemetry: tel.tel,
		userHome: userHomeOf(env.home), environ: os.Environ(),
		mcpCommand: func(token string) []string { return []string{exe, "mcp", "--ant", token} },
	})
	console := s.console
	if env.console != nil {
		console = env.console
	}
	srv := control.NewServer(logger)
	registerAPI(srv, app.NewHealth(env.clock, version, os.Getpid()), s.tools, console)
	registerConsoleAPI(srv, console)

	ln, err := control.Listen(env.address)
	if err != nil {
		return fmt.Errorf("opening the control interface: %w", err)
	}
	logger.Info("daemon started", "pid", os.Getpid(), "address", env.address)
	defer logger.Info("daemon stopped")
	serve := []func(context.Context) error{func(ctx context.Context) error { return srv.Serve(ctx, ln) }}
	if env.console != nil {
		// A test's own console comes with its own services.
		return runAll(ctx, serve...)
	}
	loop := newRunLoop(s, p.Store, env.clock, notifier, logger)
	err = runAll(ctx, append(loop.services(), serve...)...)
	// The deferred closes of the store and telemetry run after this.
	loop.detach()
	return err
}

// runAll runs every service until ctx is cancelled or one of them fails.
// A failure cancels the others. It waits for all of them to return, and
// returns the first failure.
func runAll(ctx context.Context, services ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, len(services))
	for i, service := range services {
		wg.Go(func() {
			if err := service(ctx); err != nil {
				errs[i] = err
				cancel()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// newLogger returns the daemon's logger over newLogHandler.
func newLogger(w io.Writer, config *app.Configuration) *slog.Logger {
	return slog.New(newLogHandler(w, config))
}

// newLogHandler returns the daemon's log handler: text on w, with secrets
// redacted from the message and every string or error attribute by the
// redactor of the configuration in use. It is separate from newLogger so
// that another handler can be added beside it.
func newLogHandler(w io.Writer, config *app.Configuration) slog.Handler {
	builtin, _ := domain.NewRedactor(nil)
	redactor := func() *domain.Redactor {
		if snap := config.Current(); snap != nil && snap.Redactor != nil {
			return snap.Redactor
		}
		return builtin
	}
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch v := a.Value; v.Kind() {
			case slog.KindString:
				return slog.String(a.Key, redactor().Redact(v.String()))
			case slog.KindAny:
				if err, ok := v.Any().(error); ok {
					return slog.String(a.Key, redactor().Redact(err.Error()))
				}
			}
			return a
		},
	})
}
