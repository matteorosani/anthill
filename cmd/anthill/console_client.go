package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/control"
)

// consoleTimeout bounds how long a console command waits for the daemon.
// An acceptance claims the item, which waits the claim re-read delay.
const consoleTimeout = 2 * time.Minute

// consoleCall calls a console method of the running daemon, and turns its
// errors into what the queen should read.
func consoleCall(cmd *cobra.Command, env *environment, method string, params, result any) error {
	if env.err != nil {
		return env.err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), consoleTimeout)
	defer cancel()
	return consoleError(cmd, control.NewClient(env.address).Call(ctx, method, params, result))
}

// consoleError explains a console call's error: no daemon, cleanup that
// needs confirmation, or the daemon's own message.
func consoleError(cmd *cobra.Command, err error) error {
	var rpc *control.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, control.ErrNotRunning):
		return errors.New("daemon not running")
	case errors.As(err, &rpc) && rpc.Code == codeNeedsConfirmation:
		return errors.New(rpc.Message + "; run again with --confirm")
	case errors.As(err, &rpc):
		return errors.New(rpc.Message)
	}
	return err
}

// commandName is the command line without the program name, such as
// "extension approve".
func commandName(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

// exactArgs accepts exactly n arguments, named in the usage error.
func exactArgs(n int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return &usageError{fmt.Errorf("%s takes %s", commandName(cmd), names)}
		}
		return nil
	}
}

// table writes aligned columns.
type table struct {
	w *tabwriter.Writer
}

func newTable(out io.Writer, header ...string) *table {
	t := &table{w: tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)}
	t.row(header...)
	return t
}

func (t *table) row(cells ...string) {
	_, _ = fmt.Fprintln(t.w, strings.Join(cells, "\t"))
}

// flush writes the table; its error is the output's.
func (t *table) flush() error {
	return t.w.Flush()
}

// printf writes to the command's output.
func printf(cmd *cobra.Command, format string, args ...any) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), format, args...)
	return err
}

// formatDuration prints a duration the way the console shows ages and
// budgets: 2d3h, 1h30m, 45m, or 30s under a minute.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", d/(24*time.Hour), d%(24*time.Hour)/time.Hour)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", d/time.Hour, d%time.Hour/time.Minute)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}

// formatTime prints a time of the daemon in the local time zone.
func formatTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04 MST")
}
