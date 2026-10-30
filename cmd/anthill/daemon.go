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
	"github.com/matteorosani/anthill/internal/ports"
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

// daemon holds what the daemon's services share. It is the composition
// root: adapters are built here and handed to the application services
// as ports.
type daemon struct {
	logger *slog.Logger
	config *app.Configuration
	// notifier is the notifier the services that raise notifications
	// get: the desktop one behind the notifications setting.
	notifier ports.Notifier
	health   *app.Health
	control  *control.Server
}

// runDaemon takes the daemon lock, loads the configuration, and serves
// the control interface until ctx is cancelled. It returns a
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

	if _, err := config.Load(ctx); err != nil {
		return err
	}
	d := &daemon{
		logger:   logger,
		config:   config,
		notifier: app.NewNotifications(config, env.desktop(logger)),
		health:   app.NewHealth(env.clock, version, os.Getpid()),
		control:  control.NewServer(logger),
	}
	registerAPI(d.control, d.health)

	ln, err := control.Listen(env.address)
	if err != nil {
		return fmt.Errorf("opening the control interface: %w", err)
	}
	logger.Info("daemon started", "pid", os.Getpid(), "address", env.address)
	defer logger.Info("daemon stopped")
	return runAll(ctx,
		func(ctx context.Context) error { return d.control.Serve(ctx, ln) },
		// The services that run until shutdown, such as the poll
		// scheduler, join here.
	)
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

// newLogger returns the daemon's logger: text on w, with secrets redacted
// from the message and every string or error attribute by the redactor of
// the configuration in use.
func newLogger(w io.Writer, config *app.Configuration) *slog.Logger {
	builtin, _ := domain.NewRedactor(nil)
	redactor := func() *domain.Redactor {
		if snap := config.Current(); snap != nil && snap.Redactor != nil {
			return snap.Redactor
		}
		return builtin
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
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
	}))
}
