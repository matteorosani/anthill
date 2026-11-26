package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// twoMachines starts two instances of one developer on two machines
// (spec work-claiming, "Same developer, two machines"), with the same
// extra settings, against one provider.
func twoMachines(t *testing.T, w *world, settings string) [2]*instance {
	t.Helper()
	var out [2]*instance
	for i, machine := range []string{"machine-a", "machine-b"} {
		out[i] = newInstance(t, w, instanceOptions{login: "mrosani", machine: machine, settings: settings})
	}
	return out
}

// liveAnts are the ants of ref that are not final, on every instance.
func liveAnts(ref domain.Ref, ins ...*instance) []ports.AntRecord {
	var out []ports.AntRecord
	for _, in := range ins {
		if rec, ok := in.ant(ref); ok && !rec.State().Final() {
			out = append(out, rec)
		}
	}
	return out
}

// noHeldClaims reports whether the instance holds no claim.
func noHeldClaims(in *instance) bool {
	held, err := in.db().HeldClaims(context.Background())
	return err == nil && len(held) == 0
}

// raceLag is the provider's read-after-write lag in the race tests:
// shorter than the claim re-read delay, so that each instance's re-read
// sees both claims, but long enough that neither sees the other's claim
// before posting its own while the clock stands still.
const raceLag = domain.DefaultClaimRereadDelay - time.Second

// Task 19.2, the item: the queens of two instances accept #12 at the same
// moment. Both post their claim before either re-reads the claims, so
// they race for it; exactly one ant comes out, on the instance whose
// claim comment the provider created first (spec work-claiming, "Two
// instances claim simultaneously").
func TestIntegrationRaceForAnItem(t *testing.T) {
	w := newWorld(t)
	ins := twoMachines(t, w, "")
	optedIn(w, 12, "Fix login timeout")
	item := shopItem(12)
	for _, in := range ins {
		in.queueAgents(testkit.AgentRun{Hold: true})
		in.start()
		in.polled()
	}

	// Neither sees the other's claim before posting its own.
	w.gh.ReadLag(raceLag)
	type result struct {
		code int
		out  string
	}
	var results [2]result
	var wg sync.WaitGroup
	for i, in := range ins {
		wg.Go(func() {
			code, stdout, stderr := in.command("accept", "#"+strconv.Itoa(item.Number))
			results[i] = result{code, stdout + stderr}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	// Both have posted while the clock stood still; only then does it
	// move through the re-read delay.
	w.eventually("both claims", func() bool { return len(markedComments(w, item, domain.CommentClaim)) == 2 }, ins[0], ins[1])
	w.advanceUntil("both accepts", domain.DefaultClaimRereadDelay, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, ins[0], ins[1])

	// The winner is the instance whose claim comment came first.
	claims := markedComments(w, item, domain.CommentClaim)
	if len(claims) == 0 {
		t.Fatal("no claim comment")
	}
	first, _ := domain.FindMarker(claims[0].Body)
	winner, loser := ins[0], ins[1]
	if first.Instance == ins[1].name() {
		winner, loser = ins[1], ins[0]
	}
	wi, li := 0, 1
	if winner == ins[1] {
		wi, li = 1, 0
	}
	t.Logf("results: %s %+v, %s %+v", ins[0].name(), results[0], ins[1].name(), results[1])
	if results[wi].code != 0 {
		t.Errorf("%s: accept = %d: %s", winner.name(), results[wi].code, results[wi].out)
	}
	if results[li].code == 0 || !strings.Contains(results[li].out, winner.name()) {
		t.Errorf("%s: accept = %d: %s; want a refusal naming %s", loser.name(), results[li].code, results[li].out, winner.name())
	}

	w.eventually("the winner's run", func() bool { return len(winner.agentRuns()) == 1 }, ins[0], ins[1])
	if live := liveAnts(item, ins[0], ins[1]); len(live) != 1 {
		t.Errorf("live ants = %+v, want exactly one", live)
	}
	if _, ok := winner.ant(item); !ok {
		t.Errorf("%s has no ant", winner.name())
	}
	if n := len(loser.agentRuns()); n != 0 {
		t.Errorf("%s started %d runs, want none", loser.name(), n)
	}
	held, err := winner.db().HeldClaims(context.Background())
	if err != nil || len(held) != 1 || held[0].Instance != winner.name() {
		t.Errorf("%s holds %+v, %v", winner.name(), held, err)
	}
	if held, _ := loser.db().HeldClaims(context.Background()); len(held) != 0 {
		t.Errorf("%s holds %+v, want nothing", loser.name(), held)
	}
	// The loser retracted its comment: one live claim on the item.
	if got := markedComments(w, item, domain.CommentClaim); len(got) != 1 {
		t.Errorf("claim comments = %d, want the winner's only", len(got))
	}
}

// Task 19.2, the PR: a human PR opted in to review with the review label
// reaches both instances in their first poll. Both post a review claim
// before either re-reads, so they race for it; exactly one round runs and
// exactly one round result is posted (spec work-claiming, "Different
// machine reviews the PR"; pr-shepherd).
func TestIntegrationRaceForAPullRequest(t *testing.T) {
	w := newWorld(t)
	ins := twoMachines(t, w, "shepherd-scope: labelled\n")
	review := agent(tool(domain.ToolSubmitReview, map[string]any{
		"summary": "One problem.",
		"findings": []map[string]any{{
			"file": "cart.go", "start_line": 3, "end_line": 3, "severity": "suggestion",
			"classification": "needs-developer", "message": "Name the magic number.",
		}},
	}))
	pr := domain.Ref{Repo: shopRepo, Kind: domain.KindPR, Number: 30}
	w.gh.PutPullRequest(domain.PullRequest{
		Ref: pr, Title: "Tidy the cart", SourceBranch: "tidy-cart", TargetBranch: "main",
		Author: "ada", HeadCommit: "c0ffee", Labels: []string{domain.LabelReview}, State: domain.PROpen,
	})
	// Each instance's first poll finds the PR and claims it. The clock
	// stands still, so neither sees the other's claim before posting its
	// own; only once both claims exist does it move through the re-read
	// delay.
	w.gh.ReadLag(raceLag)
	for _, in := range ins {
		in.queueAgents(review)
		in.start()
	}
	w.eventually("both review claims", func() bool { return len(markedComments(w, pr, domain.CommentClaim)) == 2 }, ins[0], ins[1])
	w.gh.ReadLag(0)
	w.advanceUntil("the round result", domain.DefaultClaimRereadDelay, func() bool {
		return len(rounds(w, pr)) == 1 && noHeldClaims(ins[0]) && noHeldClaims(ins[1])
	}, ins[0], ins[1])

	// Later polls start no other round on the same head.
	ins[0].morePolls()
	ins[1].morePolls()
	if got := rounds(w, pr); len(got) != 1 {
		t.Errorf("round results = %d, want 1", len(got))
	}
	if got := markedComments(w, pr, domain.CommentFinding); len(got) != 1 {
		t.Errorf("finding comments = %d, want 1", len(got))
	}
	reviews := len(ins[0].agentRuns()) + len(ins[1].agentRuns())
	if reviews != 1 {
		t.Errorf("review agents = %d across both instances, want 1", reviews)
	}
	m := rounds(w, pr)[0]
	if m.Instance != ins[0].name() && m.Instance != ins[1].name() {
		t.Errorf("round by %q", m.Instance)
	}
}
