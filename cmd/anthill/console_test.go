package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// Without a daemon, each console command says so.
func TestConsoleCommandsWithoutDaemon(t *testing.T) {
	te := newTestEnv(t)
	if code, _, stderr := te.command("proposals"); code != 1 || stderr != "anthill: daemon not running\n" {
		t.Errorf("without daemon = %d, %q", code, stderr)
	}
}

func TestConsoleUsageErrors(t *testing.T) {
	te := newTestEnv(t)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"accept"}, "accept takes one item, such as #12"},
		{[]string{"snooze", "#12"}, "snooze takes an item and a duration, such as #12 2h"},
		{[]string{"snooze", "#12", "soon"}, `invalid duration "soon"`},
		{[]string{"snooze", "#12", "0d"}, `invalid duration "0d"`},
		{[]string{"answer", "#12"}, "answer takes an item and the answer"},
		{[]string{"extension"}, "extension needs approve, reduce or deny"},
		{[]string{"extension", "reduce", "#12", "many"}, `invalid minutes "many"`},
		{[]string{"follow-up", "accept"}, "follow-up accept takes one item, such as #12"},
		{[]string{"accept", "#12", "--budget", "-5"}, "--budget must be a positive number of minutes"},
	} {
		code, _, stderr := te.command(tt.args...)
		if code != 2 || !strings.Contains(stderr, tt.want) {
			t.Errorf("%v = %d, %q, want 2 and %q", tt.args, code, stderr, tt.want)
		}
	}
}

func TestParseSnooze(t *testing.T) {
	for in, want := range map[string]time.Duration{"90m": 90 * time.Minute, "2h": 2 * time.Hour, "3d": 72 * time.Hour} {
		if got, err := parseSnooze(in); err != nil || got != want {
			t.Errorf("parseSnooze(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "30s", "-1h", "3dd", "d"} {
		if _, err := parseSnooze(in); err == nil {
			t.Errorf("parseSnooze(%q) accepted", in)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "0s", 30 * time.Second: "30s", 45 * time.Minute: "45m",
		90 * time.Minute: "1h30m", 51 * time.Hour: "2d3h",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// Spec queen-console, "Viewing proposals", "Why is an item not proposed"
// and "Accept with a longer budget".
func TestProposalCommands(t *testing.T) {
	e := newConsoleEnv(t)
	e.gh.PutItem(domain.WorkItem{Ref: shopItem(10), Title: "Base work", State: domain.StateOpen})
	e.gh.EditItem(shopItem(13), func(i *domain.WorkItem) { i.Dependencies = []domain.Ref{shopItem(10)} })
	e.clock.Advance(26 * time.Hour)

	want := "ITEM  TITLE       REPOSITORY  TYPE  PRIORITY  AGE   RUNNER\n" +
		"#12   Fix bug 12  acme/shop   bug   -         1d2h  claude-code\n" +
		"#14   Fix bug 14  acme/shop   bug   -         1d2h  claude-code\n" +
		"#15   Fix bug 15  acme/shop   bug   -         1d2h  claude-code\n"
	if got := e.mustRun("proposals"); got != want {
		t.Errorf("proposals =\n%s\nwant\n%s", got, want)
	}

	got := e.mustRun("explain", "#13")
	for _, line := range []string{
		"#13 Fix bug 13\n",
		"opted-in: met\n",
		"dependencies-satisfied: unmet (dependency #10 is open)\n",
		"no-active-work: met\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("explain = %q, missing %q", got, line)
		}
	}
	if strings.Contains(got, "eligible\n") && !strings.Contains(got, "unmet") {
		t.Errorf("explain = %q", got)
	}

	got = e.mustRun("accept", "#12", "--budget", "90")
	if !strings.HasPrefix(got, "accepted #12: the ant is ") || !strings.Contains(got, "with 1h30m of budget and runner claude-code, on branch ant/gh-12-") {
		t.Errorf("accept = %q", got)
	}
	if rec := e.ant(12); rec.Base != 90*time.Minute || !rec.State().HasActiveRun() {
		t.Errorf("ant = %+v", rec)
	}
	if n := len(e.exec.Executions()); n != 1 {
		t.Errorf("%d runs, want the first one", n)
	}
	if got := e.mustRun("proposals"); strings.Contains(got, "#12") {
		t.Errorf("an accepted item is still proposed:\n%s", got)
	}
	if !strings.Contains(e.mustRun("explain", "#12"), "no-active-work: unmet") {
		t.Error("explain does not show the ant's work")
	}

	// The scenario "Became ineligible in the meantime" (task-eligibility).
	e.gh.EditItem(shopItem(14), func(i *domain.WorkItem) { i.State = domain.StateClosedCompleted })
	if code, _, stderr := e.command("accept", "#14"); code != 1 || stderr != "anthill: no longer eligible: open\n" {
		t.Errorf("accept of a closed item = %d, %q", code, stderr)
	}

	if code, _, stderr := e.command("explain", "other/repo#12"); code != 1 || !strings.Contains(stderr, "repository other/repo is not configured") {
		t.Errorf("explain of an unknown repository = %d, %q", code, stderr)
	}
}

// Spec queen-console, "Decline is local"; snooze and un-hide.
func TestDeclineSnoozeAndUnhideCommands(t *testing.T) {
	e := newConsoleEnv(t)
	before := len(e.gh.Requests())
	if got := e.mustRun("decline", "#12"); got != "declined #12 on this machine\n" {
		t.Errorf("decline = %q", got)
	}
	for _, r := range e.gh.Requests()[before:] {
		if r.Op != testkit.OpItem {
			t.Errorf("the decline sent %s to the provider", r.Op)
		}
	}
	until := e.clock.Now().Add(2 * time.Hour)
	if got := e.mustRun("snooze", "#13", "2h"); got != "snoozed #13 until "+formatTime(until)+"\n" {
		t.Errorf("snooze = %q", got)
	}
	got := e.mustRun("proposals")
	if strings.Contains(got, "#12") || strings.Contains(got, "#13") || !strings.Contains(got, "#14") {
		t.Errorf("proposals after decline and snooze =\n%s", got)
	}
	if got := e.mustRun("explain", "#12"); !strings.HasSuffix(got, "hidden on this machine: declined until the item changes\n") {
		t.Errorf("explain of a declined item = %q", got)
	}
	if got := e.mustRun("unhide", "#13"); got != "#13 is proposed again when eligible\n" {
		t.Errorf("unhide = %q", got)
	}
	if got := e.mustRun("unhide", "#13"); got != "#13 was not hidden\n" {
		t.Errorf("second unhide = %q", got)
	}
	if got := e.mustRun("proposals"); !strings.Contains(got, "#13") {
		t.Errorf("proposals after unhide =\n%s", got)
	}
}

// Spec queen-console, "Follow an ant": the activity is streamed until the
// queen detaches, and detaching does not affect the ant.
func TestFollowAnAnt(t *testing.T) {
	e := newConsoleEnv(t)
	e.mustRun("accept", "#12")
	x := e.execOf(12)
	e.eventually("the run to start", func() bool { return e.ant(12).State() == domain.AntRunning })

	ctx, detach := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan int, 1)
	env := *e.environment
	env.stdout, env.stderr = out, out
	go func() { done <- run(ctx, []string{"follow", "#12"}, &env) }()
	// Lines emitted before the follow attaches are not shown; emit until
	// one is.
	e.eventually("the first line", func() bool {
		x.Emit(ports.ExecEvent{Kind: ports.ExecActivity, Line: "editing login.go"})
		time.Sleep(5 * time.Millisecond)
		return strings.Contains(out.String(), "editing login.go\n")
	})
	detach()
	if code := <-done; code != 0 || !strings.HasSuffix(out.String(), "detached; the ant goes on\n") {
		t.Errorf("follow = %d, %q", code, out.String())
	}
	x.Emit(ports.ExecEvent{Kind: ports.ExecActivity, Line: "running tests"})
	if rec := e.ant(12); rec.State() != domain.AntRunning || x.Ended() || len(x.Stops()) > 0 {
		t.Errorf("detaching affected the ant: %s, ended %v", rec.State(), x.Ended())
	}

	// Following until the run ends.
	out = &syncBuffer{}
	env.stdout, env.stderr = out, out
	go func() { done <- run(context.Background(), []string{"follow", "#12"}, &env) }()
	e.eventually("the second follow", func() bool {
		x.Emit(ports.ExecEvent{Kind: ports.ExecActivity, Line: "still here"})
		time.Sleep(5 * time.Millisecond)
		return strings.Contains(out.String(), "still here\n")
	})
	if got := e.mustRun("stop", "#12"); got != "stopped the ant for #12: it is stopped\n" {
		t.Errorf("stop = %q", got)
	}
	if code := <-done; code != 0 || !strings.HasSuffix(out.String(), "run ended: stopped (stopped by the queen)\n") {
		t.Errorf("follow to the end = %d, %q", code, out.String())
	}

	if code, _, stderr := e.command("follow", "#12"); code != 1 || stderr != "anthill: the ant for #12 has no active run (it is stopped)\n" {
		t.Errorf("follow of a stopped ant = %d, %q", code, stderr)
	}
	if code, _, stderr := e.command("follow", "#13"); code != 1 || stderr != "anthill: no ant for #13\n" {
		t.Errorf("follow without an ant = %d, %q", code, stderr)
	}
}

// The ant commands: list, show, stop, retry, continue, abandon and clean
// up.
func TestAntCommands(t *testing.T) {
	e := newConsoleEnv(t)
	if got := e.mustRun("ants"); got != "no ants\n" {
		t.Errorf("ants = %q", got)
	}
	e.mustRun("accept", "#12")
	e.eventually("the run to start", func() bool { return e.ant(12).State() == domain.AntRunning })
	e.execOf(12).Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "sess-12"})
	// Within the claim's lease: the daemon renews it, and this test has
	// no renewal.
	e.clock.Advance(10 * time.Minute)

	got := e.mustRun("ants")
	branch := e.ant(12).Branch
	want := "ITEM  STATE    RUNNER       ELAPSED  REMAINING  BRANCH\n" +
		"#12   running  claude-code  10m      50m        " + branch + "\n"
	if got != want {
		t.Errorf("ants =\n%s\nwant\n%s", got, want)
	}

	if got := e.mustRun("stop", "#12"); got != "stopped the ant for #12: it is stopped\n" {
		t.Errorf("stop = %q", got)
	}
	if code, _, stderr := e.command("continue", "#12"); code != 1 || stderr != "anthill: cannot continue the ant for #12: it is stopped\n" {
		t.Errorf("continue of a stopped ant = %d, %q", code, stderr)
	}
	if code, _, stderr := e.command("cleanup", "#12"); code != 1 || stderr != "anthill: cannot clean up the ant for #12: it is stopped; abandon it first\n" {
		t.Errorf("cleanup of a stopped ant = %d, %q", code, stderr)
	}
	if got := e.mustRun("retry", "#12"); !strings.HasPrefix(got, "retried the ant for #12: it is ") {
		t.Errorf("retry = %q", got)
	}
	e.eventually("the retry's run", func() bool { return len(e.exec.Executions()) == 2 })

	show := e.mustRun("ant", "#12")
	for _, part := range []string{"#12: ", "runner claude-code", "transitions:", "stopped by the queen", "retry", "runs:", "stopped"} {
		if !strings.Contains(show, part) {
			t.Errorf("ant = %q, missing %q", show, part)
		}
	}

	if got := e.mustRun("abandon", "#12"); got != "abandoned the ant for #12: it is abandoned\n" {
		t.Errorf("abandon = %q", got)
	}
	if got := e.mustRun("ants"); got != "no ants\n" {
		t.Errorf("ants after abandon = %q", got)
	}
	if got := e.mustRun("ants", "--all"); !strings.Contains(got, "#12   abandoned") {
		t.Errorf("ants --all = %q", got)
	}

	// Uncommitted changes need confirmation (spec branching-policy,
	// "Unpushed work").
	e.git.SetDirty(e.ant(12).Worktree, true)
	if code, _, stderr := e.command("cleanup", "#12"); code != 1 ||
		stderr != "anthill: the worktree has uncommitted changes: confirm to delete them; run again with --confirm\n" {
		t.Errorf("cleanup with changes = %d, %q", code, stderr)
	}
	if got := e.mustRun("cleanup", "#12", "--confirm"); got != "cleaned up #12: removed the worktree, deleted the local branch\n" {
		t.Errorf("cleanup --confirm = %q", got)
	}
}

// Spec queen-console, "Answer from the console".
func TestAnswerFromTheConsole(t *testing.T) {
	e := newConsoleEnv(t)
	if got := e.mustRun("questions"); got != "no pending questions\n" {
		t.Errorf("questions = %q", got)
	}
	e.mustRun("accept", "#12")
	e.eventually("the run to start", func() bool { return e.ant(12).State() == domain.AntRunning })
	first := e.execOf(12)
	first.Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "sess-12"})
	e.eventually("the session", func() bool { return e.ant(12).SessionID == "sess-12" })
	if res := e.call(12, domain.ToolAskQuestion, `{"text":"Does timeout mean 30 or 60 seconds?"}`); res.IsError {
		t.Fatalf("ask_question = %s", res.Text)
	}
	e.eventually("the question to end the run", func() bool { return e.ant(12).State() == domain.AntAwaitingInput })
	// The claim of an ant between runs is parked, so it holds however
	// long the answer takes (spec work-claiming, "Long-lived task
	// claims").
	if err := e.claiming.Park(e.ctx, shopItem(12)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Hour)

	want := "ITEM  ASKED ON  AGE   QUESTION\n#12   #12       2h0m  Does timeout mean 30 or 60 seconds?\n"
	if got := e.mustRun("questions"); got != want {
		t.Errorf("questions =\n%s\nwant\n%s", got, want)
	}

	got := e.mustRun("answer", "#12", "60", "seconds.")
	if !strings.HasPrefix(got, "answered the question of the ant for #12: https://fake.test/comment/") {
		t.Errorf("answer = %q", got)
	}
	item, _ := e.gh.Item(shopItem(12))
	last := item.Comments[len(item.Comments)-1]
	if last.Author != "mrosani" || last.Body != "> Does timeout mean 30 or 60 seconds?\n\n60 seconds." {
		t.Errorf("posted = %+v", last)
	}
	e.eventually("the run that resumes the session", func() bool { return len(e.exec.Executions()) == 2 })
	resumed := e.execOf(12)
	if resumed.Spec.Session != "sess-12" || resumed.Spec.Run.Brief != "answer: "+last.Body {
		t.Errorf("resumed run = %+v", resumed.Spec)
	}
	if got := e.mustRun("questions"); got != "no pending questions\n" {
		t.Errorf("questions after the answer = %q", got)
	}
	if code, _, stderr := e.command("answer", "#12", "again"); code != 1 || stderr != "anthill: the ant for #12 has no pending question\n" {
		t.Errorf("second answer = %d, %q", code, stderr)
	}
}

// The answer can come from standard input.
func TestAnswerFromStandardInput(t *testing.T) {
	e := newConsoleEnv(t)
	e.mustRun("accept", "#12")
	e.eventually("the run to start", func() bool { return e.ant(12).State() == domain.AntRunning })
	if res := e.call(12, domain.ToolAskQuestion, `{"text":"Which timeout?"}`); res.IsError {
		t.Fatalf("ask_question = %s", res.Text)
	}
	e.eventually("the question to end the run", func() bool { return e.ant(12).State() == domain.AntAwaitingInput })
	code, _, stderr := e.commandWithInput("Thirty seconds,\nas on the server.\n", "answer", "#12", "-")
	if code != 0 {
		t.Fatalf("answer - = %d, %q", code, stderr)
	}
	item, _ := e.gh.Item(shopItem(12))
	if body := item.Comments[len(item.Comments)-1].Body; body != "> Which timeout?\n\nThirty seconds,\nas on the server." {
		t.Errorf("posted = %q", body)
	}
}

// Extension requests listed and decided from the console (spec
// ant-timebox, "Late extension decisions").
func TestExtensionCommands(t *testing.T) {
	e := newConsoleEnv(t)
	if got := e.mustRun("extensions"); got != "no open extension requests\n" {
		t.Errorf("extensions = %q", got)
	}
	e.mustRun("accept", "#12")
	e.eventually("the run to start", func() bool { return e.ant(12).State() == domain.AntRunning })
	e.clock.Advance(30 * time.Minute)
	// The request waits the extension wait for the queen, which the
	// driven clock passes at once; it stays open.
	if res := e.call(12, domain.ToolRequestExtension, `{"minutes":45,"reason":"integration tests"}`); res.IsError || !strings.Contains(res.Text, "pending") {
		t.Fatalf("request_extension = %s", res.Text)
	}
	got := e.mustRun("extensions")
	if !strings.Contains(got, "#12   45       ") || !strings.Contains(got, "integration tests") {
		t.Errorf("extensions = %q", got)
	}
	if code, _, stderr := e.command("extension", "reduce", "#12", "50"); code != 1 || !strings.Contains(stderr, "fewer than the 45 asked") {
		t.Errorf("reduce over the request = %d, %q", code, stderr)
	}
	if got := e.mustRun("extension", "reduce", "#12", "20"); got != "reduced the extension of the ant for #12: 20 minutes granted\n" {
		t.Errorf("reduce = %q", got)
	}
	if code, _, stderr := e.command("extension", "deny", "#12"); code != 1 || stderr != "anthill: the ant for #12 has no open extension request\n" {
		t.Errorf("deny without a request = %d, %q", code, stderr)
	}
	if got := e.mustRun("extensions"); got != "no open extension requests\n" {
		t.Errorf("extensions after the decision = %q", got)
	}
}

func TestFollowUpCommandsWithoutProposal(t *testing.T) {
	e := newConsoleEnv(t)
	if got := e.mustRun("follow-ups"); got != "no follow-up proposals\n" {
		t.Errorf("follow-ups = %q", got)
	}
	e.mustRun("accept", "#12")
	if code, _, stderr := e.command("follow-up", "accept", "#12"); code != 1 || stderr != "anthill: the ant for #12 has no follow-up proposal\n" {
		t.Errorf("follow-up accept = %d, %q", code, stderr)
	}
}
