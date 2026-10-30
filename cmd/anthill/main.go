// Command anthill is the Anthill binary: the queen console, the daemon and
// the ant tools entry point (design D13). The console commands are thin
// clients of the daemon's control interface (design D10).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/configfile"
	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/adapters/desktop"
	"github.com/matteorosani/anthill/internal/adapters/systemclock"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// version is the Anthill version. Builds through the Makefile set it with
// -ldflags "-X main.version=..."; other builds report "dev".
var version = "dev"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], defaultEnvironment()))
}

// environment is everything the commands take from the machine. main
// uses the real one; tests build their own with temporary paths, a fake
// clock and a fake notifier.
type environment struct {
	stdout, stderr io.Writer
	// home is <anthill-home>, which holds the daemon lock.
	home string
	// configPath is the user's configuration file.
	configPath string
	// address is where the control interface listens.
	address string
	clock   ports.Clock
	// desktop builds the notifier that shows notifications.
	desktop func(*slog.Logger) ports.Notifier
	// err is set when the paths could not be found. Only the commands
	// that need them fail.
	err error
}

// defaultEnvironment returns the environment of the current user:
// ~/.anthill, the configuration in the OS configuration directory, and
// the control socket or pipe of the user.
func defaultEnvironment() *environment {
	env := &environment{
		stdout: os.Stdout,
		stderr: os.Stderr,
		clock:  systemclock.Clock{},
		desktop: func(logger *slog.Logger) ports.Notifier {
			return desktop.New(logger)
		},
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		env.err = fmt.Errorf("finding the home directory: %w", err)
		return env
	}
	env.home = filepath.Join(userHome, domain.AnthillHomeDir)
	if env.configPath, err = configfile.DefaultPath(); err != nil {
		env.err = err
		return env
	}
	if env.address, err = control.DefaultAddress(env.home); err != nil {
		env.err = fmt.Errorf("finding the control address: %w", err)
	}
	return env
}

// exitError ends the command with code after the command has printed
// what it had to say.
type exitError struct {
	code int
}

func (e *exitError) Error() string {
	return fmt.Sprintf("exit code %d", e.code)
}

// usageError is a command line the commands do not accept.
type usageError struct {
	err error
}

func (e *usageError) Error() string {
	return e.err.Error()
}

// run executes the command line args and returns the process exit code:
// 0 on success, 1 on failure, 2 for a usage error.
func run(ctx context.Context, args []string, env *environment) int {
	root := newRootCommand(env)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	var exit *exitError
	var usage *usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.code
	case errors.As(err, &usage):
		printError(env.stderr, err.Error()+"\nRun 'anthill --help' for usage.")
		return 2
	default:
		printError(env.stderr, err.Error())
		return 1
	}
}

// printError writes msg to stderr after the program name.
func printError(stderr io.Writer, msg string) {
	// If stderr itself cannot be written, there is nowhere left to report
	// it, so the error is deliberately discarded.
	_, _ = fmt.Fprintf(stderr, "anthill: %s\n", msg)
}

// newRootCommand returns the anthill command with its subcommands.
func newRootCommand(env *environment) *cobra.Command {
	root := &cobra.Command{
		Use:     "anthill",
		Short:   "Anthill runs AI coding agents on opted-in tickets and shepherds their pull requests",
		Version: version,
		// Errors are printed once, by run, and a failing command does not
		// print the usage text after its error.
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &usageError{fmt.Errorf("unknown command %q", args[0])}
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error {
			return &usageError{errors.New("a command is required")}
		},
	}
	root.SetVersionTemplate("anthill {{.Version}}\n")
	root.SetOut(env.stdout)
	root.SetErr(env.stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{err}
	})
	root.AddCommand(newVersionCommand(), newDaemonCommand(env), newStatusCommand(env))
	return root
}

// noArgs rejects any argument as a usage error.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return &usageError{fmt.Errorf("%s takes no arguments", cmd.Name())}
	}
	return nil
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Anthill version",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "anthill %s\n", version)
			return err
		},
	}
}
