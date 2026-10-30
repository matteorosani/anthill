package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/control"
)

// statusTimeout bounds how long status waits for a daemon that does not
// answer.
const statusTimeout = 10 * time.Second

func newStatusCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the daemon's health",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if env.err != nil {
				return env.err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), statusTimeout)
			defer cancel()
			var st statusResult
			err := control.NewClient(env.address).Call(ctx, methodStatus, nil, &st)
			if errors.Is(err, control.ErrNotRunning) {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon not running"); err != nil {
					return err
				}
				return &exitError{code: 1}
			}
			if err != nil {
				return fmt.Errorf("asking the daemon for its status: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "daemon: running (pid %d, anthill %s)\nuptime: %s\n",
				st.PID, st.Version, time.Duration(st.Uptime)*time.Second)
			return err
		},
	}
}
