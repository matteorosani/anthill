package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/control"
)

// The ant commands (spec queen-console, "Monitoring ants" and
// "Controlling ants").

func newAntsCommand(env *environment) *cobra.Command {
	var p antsParams
	cmd := &cobra.Command{
		Use:   "ants",
		Short: "List the ants with their state, runner, budget and branch",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res antsResult
			if err := consoleCall(cmd, env, methodAnts, p, &res); err != nil {
				return err
			}
			if len(res.Ants) == 0 {
				return printf(cmd, "no ants\n")
			}
			t := newTable(cmd.OutOrStdout(), "ITEM", "STATE", "RUNNER", "ELAPSED", "REMAINING", "BRANCH")
			for _, a := range res.Ants {
				t.row(a.Item.Name, antState(a), a.Runner, formatDuration(time.Duration(a.Used)*time.Second), remaining(a), orNone(a.Branch))
			}
			return t.flush()
		},
	}
	cmd.Flags().BoolVar(&p.All, "all", false, "also list done and abandoned ants")
	return cmd
}

// antState is the ant's state, with a follow-up proposal waiting for the
// queen.
func antState(a antJSON) string {
	if a.FollowUp == "pending" {
		return a.State + " (follow-up proposed)"
	}
	return a.State
}

// remaining is the ant's remaining budget, flagged once the ant was
// warned at its warning threshold.
func remaining(a antJSON) string {
	s := formatDuration(time.Duration(a.Remaining) * time.Second)
	if a.NearLimit {
		s += " (near limit)"
	}
	return s
}

func newAntCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "ant <item>",
		Short: "Show an item's ant with every transition and run",
		Args:  exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res antDetailResult
			if err := consoleCall(cmd, env, methodAnt, itemParams{Item: args[0]}, &res); err != nil {
				return err
			}
			a := res.Ant
			if err := printf(cmd, "%s: %s, runner %s, branch %s\nbudget: %s used, %s remaining of %s\n",
				a.Item.Name, antState(a), a.Runner, orNone(a.Branch),
				formatDuration(time.Duration(a.Used)*time.Second), remaining(a), formatDuration(time.Duration(a.Total)*time.Second)); err != nil {
				return err
			}
			if a.PR != nil {
				if err := printf(cmd, "pull request: %s\n", a.PR.Name); err != nil {
					return err
				}
			}
			if err := printf(cmd, "\ntransitions:\n"); err != nil {
				return err
			}
			t := newTable(cmd.OutOrStdout(), "AT", "FROM", "TO", "REASON")
			for _, tr := range res.History {
				t.row(formatTime(tr.At), orDash(tr.From), tr.To, tr.Reason)
			}
			if err := t.flush(); err != nil {
				return err
			}
			if len(res.Runs) == 0 {
				return nil
			}
			if err := printf(cmd, "\nruns:\n"); err != nil {
				return err
			}
			t = newTable(cmd.OutOrStdout(), "RUN", "KIND", "STARTED", "DURATION", "OUTCOME")
			for _, r := range res.Runs {
				duration, outcome := "active", "-"
				if !r.EndedAt.IsZero() {
					duration, outcome = formatDuration(r.EndedAt.Sub(r.StartedAt)), r.Outcome
					if r.Reason != "" && r.Reason != r.Outcome {
						outcome += " (" + r.Reason + ")"
					}
				}
				t.row(fmt.Sprint(r.ID), r.Kind, formatTime(r.StartedAt), duration, outcome)
			}
			return t.flush()
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newFollowCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "follow <item>",
		Short: "Stream the activity of an ant's active run until you detach with Ctrl+C",
		Long: "Stream the activity of the active run of an item's ant until the run ends or you detach with Ctrl+C. " +
			"Detaching does not affect the ant.",
		Args: exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if env.err != nil {
				return env.err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			var res followResult
			err := control.NewClient(env.address).Stream(ctx, methodFollow, itemParams{Item: args[0]}, func(raw json.RawMessage) error {
				var it followItem
				if err := json.Unmarshal(raw, &it); err != nil {
					return err
				}
				if it.Dropped > 0 {
					if err := printf(cmd, "[%d lines skipped]\n", it.Dropped); err != nil {
						return err
					}
				}
				return printf(cmd, "%s\n", it.Line)
			}, &res)
			if errors.Is(err, context.Canceled) {
				return printf(cmd, "detached; the ant goes on\n")
			}
			if err := consoleError(cmd, err); err != nil {
				return err
			}
			if !res.Ended {
				return printf(cmd, "the daemon stopped\n")
			}
			ended := "run ended: " + res.Outcome
			if res.Reason != "" && res.Reason != res.Outcome {
				ended += " (" + res.Reason + ")"
			}
			return printf(cmd, "%s\n", ended)
		},
	}
}

// newAntActionCommand is a queen action on an item's ant that prints the
// ant's state after it.
func newAntActionCommand(env *environment, use, short, method, done string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <item>",
		Short: short,
		Args:  exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res antResult
			if err := consoleCall(cmd, env, method, itemParams{Item: args[0]}, &res); err != nil {
				return err
			}
			return printAction(cmd, done, res.Ant)
		},
	}
}

// printAction reports a queen action carried out: the ant and its state.
func printAction(cmd *cobra.Command, done string, a antJSON) error {
	return printf(cmd, "%s the ant for %s: it is %s\n", done, a.Item.Name, antState(a))
}

func newContinueCommand(env *environment) *cobra.Command {
	var p continueParams
	cmd := &cobra.Command{
		Use:   "continue <item>",
		Short: "Start a new run for a timed-out ant with a new budget",
		Args:  exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if p.BudgetMinutes < 0 {
				return &usageError{fmt.Errorf("--budget must be a positive number of minutes")}
			}
			p.Item = args[0]
			var res antResult
			if err := consoleCall(cmd, env, methodContinue, p, &res); err != nil {
				return err
			}
			return printf(cmd, "continued the ant for %s with %s of budget: it is %s\n",
				res.Ant.Item.Name, formatDuration(time.Duration(res.Ant.Remaining)*time.Second), antState(res.Ant))
		},
	}
	cmd.Flags().IntVar(&p.BudgetMinutes, "budget", 0, "new budget in minutes (default: the ant's base budget)")
	return cmd
}

func newCleanupCommand(env *environment) *cobra.Command {
	var p cleanupParams
	cmd := &cobra.Command{
		Use:   "cleanup <item>",
		Short: "Remove the worktree and local branch of a done or abandoned ant",
		Long: "Remove the worktree and local branch of a done or abandoned ant. " +
			"Unpushed commits and uncommitted changes are deleted only with --confirm.",
		Args: exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			p.Item = args[0]
			var res cleanupResult
			if err := consoleCall(cmd, env, methodCleanup, p, &res); err != nil {
				return err
			}
			var did []string
			if res.RemoveWorktree {
				did = append(did, "removed the worktree")
			}
			if res.DeleteLocalBranch {
				did = append(did, "deleted the local branch")
			}
			if res.DeleteRemoteBranch {
				did = append(did, "deleted the remote branch")
			}
			if len(did) == 0 {
				return printf(cmd, "nothing to clean up for %s\n", args[0])
			}
			return printf(cmd, "cleaned up %s: %s\n", args[0], strings.Join(did, ", "))
		},
	}
	cmd.Flags().BoolVar(&p.Confirm, "confirm", false, "also delete unpushed commits and uncommitted changes")
	return cmd
}
