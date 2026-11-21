package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// printConsoleStatus writes the rest of the daemon's health (spec
// queen-console, "Status overview"): each provider's last successful
// poll, last error and rate limit, the ants with an active run, the
// queued ants, each held claim with its lease expiry, and the active
// shepherd rounds.
func printConsoleStatus(w io.Writer, s *consoleStatus) error {
	var b strings.Builder
	b.WriteString("\nproviders:\n")
	if len(s.Providers) == 0 {
		b.WriteString("  none polled yet\n")
	}
	for _, p := range s.Providers {
		last := "never"
		if !p.LastSuccess.IsZero() {
			last = formatTime(p.LastSuccess)
		}
		fmt.Fprintf(&b, "  %s: last successful poll %s\n", p.Connection, last)
		if p.LastError != "" {
			fmt.Fprintf(&b, "    last error at %s: %s\n", formatTime(p.LastErrorAt), p.LastError)
		}
		if p.RateLimitedUntil.After(s.Now) {
			fmt.Fprintf(&b, "    polling paused for the rate limit until %s\n", formatTime(p.RateLimitedUntil))
		}
	}
	b.WriteString("\nactive runs:\n")
	if len(s.ActiveRuns) == 0 {
		b.WriteString("  none\n")
	}
	for _, a := range s.ActiveRuns {
		fmt.Fprintf(&b, "  %s: %s, %s elapsed, %s remaining\n", a.Item.Name, a.State,
			formatDuration(time.Duration(a.Used)*time.Second), remaining(a))
	}
	b.WriteString("\nqueued ants:\n")
	if len(s.Queued) == 0 {
		b.WriteString("  none\n")
	}
	for _, a := range s.Queued {
		fmt.Fprintf(&b, "  %s: queued since %s\n", a.Item.Name, formatTime(a.Since))
	}
	b.WriteString("\nheld claims:\n")
	if len(s.Claims) == 0 {
		b.WriteString("  none\n")
	}
	for _, c := range s.Claims {
		lease := "lease expires " + formatTime(c.Expires)
		if c.Parked {
			lease = "parked"
		}
		fmt.Fprintf(&b, "  %s: %s claim, %s\n", c.Item.Name, c.Kind, lease)
	}
	b.WriteString("\nactive rounds:\n")
	if len(s.Rounds) == 0 {
		b.WriteString("  none\n")
	}
	for _, pr := range s.Rounds {
		fmt.Fprintf(&b, "  %s (%s)\n", pr.Name, pr.Repository)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func newUsageCommand(env *environment) *cobra.Command {
	var p usageParams
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show the duration, tokens and cost of runs and rounds, for a day or an item",
		Long: "Show the recorded usage of each ant run and shepherd round started on a day (today by default), " +
			"or of every run and round of an item, with totals. Unknown values are shown as unknown and left out of the totals.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if p.Date != "" && p.Item != "" {
				return &usageError{fmt.Errorf("usage takes --date or --item, not both")}
			}
			var res usageResult
			if err := consoleCall(cmd, env, methodUsage, p, &res); err != nil {
				return err
			}
			heading := "usage of " + p.Item
			if res.Date != "" {
				heading = "usage on " + res.Date
			}
			if err := printf(cmd, "%s:\n", heading); err != nil {
				return err
			}
			if len(res.Lines) == 0 {
				return printf(cmd, "no runs or rounds\n")
			}
			t := newTable(cmd.OutOrStdout(), "STARTED", "ITEM", "KIND", "DURATION", "TOKENS", "COST")
			for _, l := range res.Lines {
				duration := formatDuration(time.Duration(l.Duration) * time.Second)
				if l.Active {
					duration = "active"
				}
				t.row(formatTime(l.StartedAt), l.Item.Name, l.Kind, duration, formatTokens(l.Tokens), formatCost(l.CostUSD))
			}
			if err := t.flush(); err != nil {
				return err
			}
			return printf(cmd, "total: %s, %s, %s\n", formatDuration(time.Duration(res.Duration)*time.Second),
				withUnknown(fmt.Sprintf("%d tokens", res.Tokens), "tokens", res.TokensUnknown),
				withUnknown(fmt.Sprintf("$%.2f", res.CostUSD), "cost", res.CostUnknown))
		},
	}
	cmd.Flags().StringVar(&p.Date, "date", "", "the day to report, as YYYY-MM-DD (default: today)")
	cmd.Flags().StringVar(&p.Item, "item", "", "report every run and round of this item instead, such as #12")
	return cmd
}

func formatTokens(t *int64) string {
	if t == nil {
		return "unknown"
	}
	return fmt.Sprint(*t)
}

func formatCost(c *float64) string {
	if c == nil {
		return "unknown"
	}
	return fmt.Sprintf("$%.2f", *c)
}

// withUnknown adds to a total how many runs and rounds it misses.
func withUnknown(total, what string, unknown int) string {
	switch unknown {
	case 0:
		return total
	case 1:
		return fmt.Sprintf("%s (1 run or round with unknown %s, missing from the total)", total, what)
	}
	return fmt.Sprintf("%s (%d runs or rounds with unknown %s, missing from the total)", total, unknown, what)
}
