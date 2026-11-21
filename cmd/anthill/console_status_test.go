package main

import (
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Spec queen-console, "Status overview": for each provider the time of
// its last successful poll, and for each held claim its item and lease
// expiry; with the rate limit, the active runs, the queued ants and the
// active rounds.
func TestStatusOverview(t *testing.T) {
	e := newConsoleEnv(t)
	now := e.clock.Now()
	if err := e.store.RecordPoll(e.ctx, ports.PollResult{Connection: "github", At: now}); err != nil {
		t.Fatal(err)
	}
	limited := now.Add(30 * time.Minute)
	if err := e.store.RecordPollFailure(e.ctx, "azure-devops/contoso", now, "rate limited", limited); err != nil {
		t.Fatal(err)
	}
	e.mustRun("accept", "#12")
	e.mustRun("accept", "#13")
	e.mustRun("accept", "#14") // capacity 2: queued
	e.eventually("the runs to start", func() bool {
		return e.ant(12).State() == domain.AntRunning && e.ant(13).State() == domain.AntRunning
	})
	e.rounds.set(domain.Ref{Repo: shopRepo, Kind: domain.KindPR, Number: 34})
	held, err := e.store.HeldClaims(e.ctx)
	if err != nil || len(held) != 2 {
		t.Fatalf("held claims = %+v, %v", held, err)
	}
	e.clock.Advance(5 * time.Minute)

	code, got, stderr := e.command("status")
	if code != 0 {
		t.Fatalf("status = %d, %q", code, stderr)
	}
	for _, want := range []string{
		"daemon: running",
		"\nproviders:\n" +
			"  azure-devops/contoso: last successful poll never\n" +
			"    last error at " + formatTime(now) + ": rate limited\n" +
			"    polling paused for the rate limit until " + formatTime(limited) + "\n" +
			"  github: last successful poll " + formatTime(now) + "\n",
		// #12's run started first, and the later acceptances moved the
		// driven clock past their claim re-read delays, so its numbers
		// can differ by some seconds.
		"\nactive runs:\n  #12: running, 5m elapsed, ",
		"\n  #13: running, 5m elapsed, 55m remaining\n",
		"\nqueued ants:\n  #14: queued since " + formatTime(now) + "\n",
		"\nheld claims:\n" +
			"  #12: task claim, lease expires " + formatTime(held[0].UpdatedAt.Add(held[0].Lease)) + "\n" +
			"  #13: task claim, lease expires " + formatTime(held[1].UpdatedAt.Add(held[1].Lease)) + "\n",
		"\nactive rounds:\n  #34 (acme/shop)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status =\n%s\nmissing\n%s", got, want)
		}
	}
}

func int64p(v int64) *int64       { return &v }
func float64p(v float64) *float64 { return &v }

// Spec queen-console, "Daily usage": each run and round of the day with
// its tokens and cost, and a total that states how many had unknown cost.
func TestDailyUsage(t *testing.T) {
	e := newConsoleEnv(t)
	now := e.clock.Now()
	pr := domain.Ref{Repo: shopRepo, Kind: domain.KindPR, Number: 34}
	ant, err := e.store.CreateAnt(e.ctx, ports.AntRecord{Item: shopItem(12), Runner: "claude-code", PR: &pr,
		History: []domain.Transition{{To: domain.AntSucceeded, At: now}}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(start time.Time, d time.Duration, u ports.Usage) {
		id, err := e.store.StartRun(e.ctx, ports.RunRecord{Ant: ant, Kind: ports.RunTask, StartedAt: start})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.store.EndRun(e.ctx, ports.RunRecord{ID: id, Ant: ant, EndedAt: start.Add(d), Outcome: ports.OutcomeComplete, Usage: u}); err != nil {
			t.Fatal(err)
		}
	}
	run(now.AddDate(0, 0, -1), time.Hour, ports.Usage{Tokens: int64p(99), CostUSD: float64p(9)}) // yesterday
	run(now, 20*time.Minute, ports.Usage{Tokens: int64p(12000), CostUSD: float64p(0.42)})
	round, err := e.store.StartRound(e.ctx, ports.RoundRecord{PR: pr, Head: "c1", StartedAt: now.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.EndRound(e.ctx, ports.RoundRecord{ID: round, PR: pr, Head: "c1", StartedAt: now.Add(30 * time.Minute),
		EndedAt: now.Add(35 * time.Minute), Usage: ports.Usage{Tokens: int64p(3000)}}); err != nil {
		t.Fatal(err)
	}

	want := "usage on " + now.Local().Format(time.DateOnly) + ":\n" +
		"STARTED                     ITEM  KIND      DURATION  TOKENS  COST\n" +
		formatTime(now) + "  #12   task run  20m       12000   $0.42\n" +
		formatTime(now.Add(30*time.Minute)) + "  #34   round     5m        3000    unknown\n" +
		"total: 25m, 15000 tokens, $0.42 (1 run or round with unknown cost, missing from the total)\n"
	if got := e.mustRun("usage"); !sameTable(got, want) {
		t.Errorf("usage =\n%s\nwant\n%s", got, want)
	}
	got := e.mustRun("usage", "--item", "#12")
	if !strings.HasPrefix(got, "usage of #12:\n") || !strings.Contains(got, "total: 1h25m, 15099 tokens, $9.42 (1 run or round") {
		t.Errorf("usage --item =\n%s", got)
	}
	if got := e.mustRun("usage", "--date", "2020-01-01"); got != "usage on 2020-01-01:\nno runs or rounds\n" {
		t.Errorf("usage of an empty day = %q", got)
	}
	if code, _, stderr := e.command("usage", "--date", "yesterday"); code != 1 || !strings.Contains(stderr, "invalid date yesterday") {
		t.Errorf("usage --date yesterday = %d, %q", code, stderr)
	}
	if code, _, _ := e.command("usage", "--date", "2020-01-01", "--item", "#12"); code != 2 {
		t.Errorf("usage with both = %d", code)
	}
}

// sameTable compares two outputs with their runs of spaces collapsed, as
// column widths depend on the local time zone's name.
func sameTable(a, b string) bool {
	norm := func(s string) string {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = strings.Join(strings.Fields(l), " ")
		}
		return strings.Join(lines, "\n")
	}
	return norm(a) == norm(b)
}
