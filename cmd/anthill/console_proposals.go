package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// The proposal commands (spec queen-console, "Viewing proposals" and
// "Accepting and declining").

func newProposalsCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "proposals",
		Short: "List the proposals: the eligible items not hidden on this machine",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res proposalsResult
			if err := consoleCall(cmd, env, methodProposals, nil, &res); err != nil {
				return err
			}
			for _, w := range res.Warnings {
				printError(cmd.ErrOrStderr(), "warning: "+w)
			}
			if len(res.Proposals) == 0 {
				return printf(cmd, "no proposals\n")
			}
			t := newTable(cmd.OutOrStdout(), "ITEM", "TITLE", "REPOSITORY", "TYPE", "PRIORITY", "AGE", "RUNNER")
			for _, p := range res.Proposals {
				t.row(p.Item.Name, p.Title, p.Repository, p.Type, p.Priority, formatDuration(res.Now.Sub(p.CreatedAt)), p.Runner)
			}
			return t.flush()
		},
	}
}

func newExplainCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "explain <item>",
		Short: "Show every prerequisite of an item, met and unmet",
		Long:  "Show why an item is or is not proposed: every prerequisite with its status and reason, evaluated on fresh provider data.",
		Args:  exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res explainResult
			if err := consoleCall(cmd, env, methodExplain, itemParams{Item: args[0]}, &res); err != nil {
				return err
			}
			if err := printf(cmd, "%s %s\n", res.Item.Name, res.Title); err != nil {
				return err
			}
			eligible := true
			for _, c := range res.Checks {
				line := c.Prerequisite + ": met"
				if !c.Met {
					eligible = false
					line = fmt.Sprintf("%s: unmet (%s)", c.Prerequisite, c.Reason)
				}
				if err := printf(cmd, "%s\n", line); err != nil {
					return err
				}
			}
			switch {
			case res.Hidden && !res.Until.IsZero():
				return printf(cmd, "hidden on this machine: snoozed until %s\n", formatTime(res.Until))
			case res.Hidden:
				return printf(cmd, "hidden on this machine: declined until the item changes\n")
			case eligible:
				return printf(cmd, "eligible\n")
			}
			return nil
		},
	}
}

func newAcceptCommand(env *environment) *cobra.Command {
	var p acceptParams
	cmd := &cobra.Command{
		Use:   "accept <item>",
		Short: "Accept a proposal: create its ant and start its first run",
		Args:  exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if p.BudgetMinutes < 0 {
				return &usageError{fmt.Errorf("--budget must be a positive number of minutes")}
			}
			p.Item = args[0]
			var res antResult
			if err := consoleCall(cmd, env, methodAccept, p, &res); err != nil {
				return err
			}
			a := res.Ant
			return printf(cmd, "accepted %s: the ant is %s, with %s of budget and runner %s, on branch %s\n",
				a.Item.Name, a.State, formatDuration(time.Duration(a.Total)*time.Second), a.Runner, orNone(a.Branch))
		},
	}
	cmd.Flags().StringVar(&p.Runner, "runner", "", "use this runner instead of the configured one (claude-code, codex or opencode)")
	cmd.Flags().IntVar(&p.BudgetMinutes, "budget", 0, "base budget in minutes, instead of the configured one")
	cmd.Flags().StringVar(&p.BaseBranch, "base", "", "base branch, instead of the configured one")
	return cmd
}

// orNone prints "none" for an empty value.
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func newDeclineCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "decline <item>",
		Short: "Hide a proposal on this machine until the item changes",
		Long:  "Hide a proposal on this machine only, until its title or description changes or you un-hide it. Colleagues' machines still propose it.",
		Args:  exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := consoleCall(cmd, env, methodDecline, itemParams{Item: args[0]}, nil); err != nil {
				return err
			}
			return printf(cmd, "declined %s on this machine\n", args[0])
		},
	}
}

func newSnoozeCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "snooze <item> <duration>",
		Short: "Hide a proposal on this machine for a while, such as 2h or 3d",
		Args:  exactArgs(2, "an item and a duration, such as #12 2h"),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseSnooze(args[1])
			if err != nil {
				return &usageError{err}
			}
			var res snoozeResult
			if err := consoleCall(cmd, env, methodSnooze, snoozeParams{Item: args[0], Seconds: int64(d / time.Second)}, &res); err != nil {
				return err
			}
			return printf(cmd, "snoozed %s until %s\n", args[0], formatTime(res.Until))
		},
	}
}

// parseSnooze reads a snooze duration: a Go duration such as 90m or 2h,
// or a number of days such as 3d.
func parseSnooze(s string) (time.Duration, error) {
	var days int
	if n, err := fmt.Sscanf(s, "%dd", &days); err == nil && n == 1 && fmt.Sprintf("%dd", days) == s {
		if days <= 0 {
			return 0, fmt.Errorf("invalid duration %q: it must be positive", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("invalid duration %q: use minutes, hours or days, such as 90m, 2h or 3d", s)
	}
	return d, nil
}

func newUnhideCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "unhide <item>",
		Short: "Show a declined or snoozed proposal again",
		Args:  exactArgs(1, "one item, such as #12"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res unhideResult
			if err := consoleCall(cmd, env, methodUnhide, itemParams{Item: args[0]}, &res); err != nil {
				return err
			}
			if !res.WasHidden {
				return printf(cmd, "%s was not hidden\n", args[0])
			}
			return printf(cmd, "%s is proposed again when eligible\n", args[0])
		},
	}
}
