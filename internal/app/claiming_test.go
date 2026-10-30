package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
	"github.com/matteorosani/anthill/internal/testkit/storetest"
)

// The claim tests run inside a synctest bubble, so that a test can tell
// when an instance waits for the re-read delay before it moves the fake
// clock on.

const (
	instanceA = "mrosani@LAPTOP-42"
	instanceB = "lbianchi@DESKTOP-7"
)

// claimEnv is one fake GitHub shared by several instances.
type claimEnv struct {
	t     *testing.T
	clock *testkit.Clock
	gh    *testkit.Provider
}

func newClaimEnv(t *testing.T) *claimEnv {
	t.Helper()
	clock := testkit.NewClock(t0)
	gh := testkit.NewProvider(clock)
	gh.PutItem(domain.WorkItem{Ref: ghRef(12), Title: "Fix login", State: domain.StateOpen, Labels: []string{"anthill"}})
	gh.PutPullRequest(domain.PullRequest{Ref: ghPR(34), Title: "Fix login", SourceBranch: "ant/gh-12-fix-login", TargetBranch: "main", Author: "mrosani"})
	gh.AddWriter(shop, "mrosani", "lbianchi", "ada")
	return &claimEnv{t: t, clock: clock, gh: gh}
}

// claimer is one instance: its own local store, notifier, loss handler
// and claim service.
type claimer struct {
	env      *claimEnv
	name     string
	store    *testkit.StateStore
	notifier *testkit.Notifier
	loss     *testkit.ClaimLossHandler
	claiming *app.Claiming
}

func (e *claimEnv) instance(name, login string) *claimer {
	return e.restart(&claimer{name: name, store: testkit.NewStateStore()}, login)
}

// restart gives the instance a new claim service over the same local
// store, as a daemon restart does.
func (e *claimEnv) restart(c *claimer, login string) *claimer {
	route := func(_ context.Context, ref domain.Ref) (app.ClaimTarget, error) {
		return app.ClaimTarget{Provider: e.gh.Client(login), Instance: c.name, Label: domain.DefaultClaimLabel}, nil
	}
	c.env, c.notifier, c.loss = e, &testkit.Notifier{}, &testkit.ClaimLossHandler{}
	c.claiming = app.NewClaiming(route, c.store, e.clock, c.notifier, c.loss, app.ClaimingOptions{})
	return c
}

// acquire runs Acquire, moving the clock past the re-read delay once the
// instance waits for it.
func (c *claimer) acquire(ref domain.Ref, kind domain.ClaimKind) (ports.HeldClaim, error) {
	type result struct {
		held ports.HeldClaim
		err  error
	}
	done := make(chan result, 1)
	go func() {
		h, err := c.claiming.Acquire(context.Background(), ref, kind)
		done <- result{h, err}
	}()
	synctest.Wait()
	c.env.clock.Advance(domain.DefaultClaimRereadDelay)
	r := <-done
	return r.held, r.err
}

func (c *claimer) mustAcquire(ref domain.Ref, kind domain.ClaimKind) ports.HeldClaim {
	c.env.t.Helper()
	h, err := c.acquire(ref, kind)
	if err != nil {
		c.env.t.Fatalf("%s: Acquire(%s) = %v", c.name, ref, err)
	}
	return h
}

func (c *claimer) held() []ports.HeldClaim {
	c.env.t.Helper()
	held, err := c.store.HeldClaims(context.Background())
	if err != nil {
		c.env.t.Fatal(err)
	}
	return held
}

// createAnt stores an ant for item 12 in the given states.
func (c *claimer) createAnt(states ...domain.AntState) ports.AntID {
	c.env.t.Helper()
	id, err := c.store.CreateAnt(context.Background(), storetest.Ant(12, states...))
	if err != nil {
		c.env.t.Fatal(err)
	}
	return id
}

func (c *claimer) ant(id ports.AntID) ports.AntRecord {
	c.env.t.Helper()
	r, err := c.store.Ant(context.Background(), id)
	if err != nil {
		c.env.t.Fatal(err)
	}
	return r
}

func (e *claimEnv) item() domain.WorkItem {
	item, _ := e.gh.Item(ghRef(12))
	return item
}

// claimComments returns the claims on ref, as written on the provider.
func (e *claimEnv) claimComments(ref domain.Ref) []domain.Claim {
	var comments []domain.Comment
	if ref.Kind == domain.KindPR {
		pr, _ := e.gh.PullRequest(ref)
		comments = pr.Comments
	} else {
		item, _ := e.gh.Item(ref)
		comments = item.Comments
	}
	var out []domain.Claim
	for _, comment := range comments {
		if c, ok := domain.ParseClaim(comment); ok {
			out = append(out, c)
		}
	}
	return out
}

// ops counts the provider calls of op made by login.
func (e *claimEnv) ops(op testkit.Op, login string) int {
	n := 0
	for _, r := range e.gh.Requests() {
		if r.Op == op && r.Login == login {
			n++
		}
	}
	return n
}

// holder is who holds the claim on ref, as any instance resolves it.
func (e *claimEnv) holder(ref domain.Ref, kind domain.ClaimKind) string {
	e.t.Helper()
	page, err := e.gh.Client("ada").Claims(context.Background(), ref)
	if err != nil {
		e.t.Fatal(err)
	}
	v := domain.ClaimView{Comments: page.Comments, Labels: page.Labels, Writers: domain.NewUserSet("mrosani", "lbianchi", "ada"), Now: page.ServerTime}
	winner, _ := domain.ResolveClaim(v, ref, kind)
	return winner.Instance
}

func lastNotification(t *testing.T, n *testkit.Notifier) ports.Notification {
	t.Helper()
	sent := n.Sent()
	if len(sent) == 0 {
		t.Fatal("no notification")
	}
	return sent[len(sent)-1]
}

// Task 10.1, scenario "Claim visible to humans": the winner posts the
// claim comment, adds the claim label and assigns the developer.
func TestClaimVisibleToHumans(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		held := a.mustAcquire(ghRef(12), domain.ClaimTask)

		item := e.item()
		if len(item.Comments) != 1 || !strings.Contains(item.Comments[0].Body, "Claimed by mrosani@LAPTOP-42 until 10:45 UTC") {
			t.Errorf("comments = %+v", item.Comments)
		}
		if !slices.Contains(item.Labels, domain.DefaultClaimLabel) || !slices.Equal(item.Assignees, []string{"mrosani"}) {
			t.Errorf("labels %v, assignees %v", item.Labels, item.Assignees)
		}
		want := ports.HeldClaim{Ref: ghRef(12), Kind: domain.ClaimTask, Instance: instanceA, CommentID: item.Comments[0].ID,
			Label: domain.DefaultClaimLabel, Assigned: true, Lease: domain.DefaultLease, UpdatedAt: item.Comments[0].UpdatedAt}
		if held != want || !slices.Equal(a.held(), []ports.HeldClaim{want}) {
			t.Errorf("held = %+v, stored %+v; want %+v", held, a.held(), want)
		}
		if !held.ExpiresAt().Equal(t0.Add(15 * time.Minute)) {
			t.Errorf("expires at %v", held.ExpiresAt())
		}
	})
}

// Task 10.1, scenario "Two instances claim simultaneously", with two real
// claim services racing on the fake provider. A posts first, B's check
// does not see A's comment yet (read-after-write lag), and the post
// barrier holds both until both comments exist. After the re-read delay
// both see both comments: A wins, B retracts without touching the label,
// and B's queen is told "claimed by A".
func TestTwoInstancesClaimSimultaneously(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		e.gh.ReadLag(2 * time.Second)
		e.gh.Barrier(testkit.OpPost, 2)

		type result struct {
			held ports.HeldClaim
			err  error
		}
		results := map[string]chan result{instanceA: make(chan result, 1), instanceB: make(chan result, 1)}
		start := func(c *claimer) {
			go func() {
				h, err := c.claiming.Acquire(context.Background(), ghRef(12), domain.ClaimTask)
				results[c.name] <- result{h, err}
			}()
			synctest.Wait()
		}
		start(a) // A checked and posted; it waits at the barrier.
		start(b) // B checked, saw nothing, posted; both wait to re-read.
		if n := e.clock.Waiters(); n != 2 {
			t.Fatalf("%d instances wait for the re-read, want 2", n)
		}
		e.clock.Advance(domain.DefaultClaimRereadDelay)
		ra, rb := <-results[instanceA], <-results[instanceB]

		if ra.err != nil || ra.held.Instance != instanceA {
			t.Errorf("A: %+v, %v; want A to hold the claim", ra.held, ra.err)
		}
		var taken *app.ClaimTakenError
		if !errors.As(rb.err, &taken) || taken.Holder != instanceA || rb.err.Error() != "claimed by "+instanceA {
			t.Errorf("B: err = %v, want claimed by %s", rb.err, instanceA)
		}
		if claims := e.claimComments(ghRef(12)); len(claims) != 1 || claims[0].Instance != instanceA {
			t.Errorf("claim comments = %+v, want only A's: B retracts its comment", claims)
		}
		item := e.item()
		if !slices.Equal(item.Labels, []string{"anthill", domain.DefaultClaimLabel}) || !slices.Equal(item.Assignees, []string{"mrosani"}) {
			t.Errorf("labels %v, assignees %v; want the label once and only A's developer", item.Labels, item.Assignees)
		}
		if n := e.ops(testkit.OpMark, "lbianchi") + e.ops(testkit.OpUnmark, "lbianchi"); n != 0 {
			t.Errorf("B touched the label or assignee %d times", n)
		}
		if len(b.held()) != 0 || len(a.held()) != 1 {
			t.Errorf("held claims: A %v, B %v", a.held(), b.held())
		}
	})
}

// Both instances check before either posts, and both post before either
// re-reads, in an order the scheduler decides: whoever posted first wins.
func TestTwoInstancesRaceInEitherOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		claimers := []*claimer{e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")}
		e.gh.Barrier(testkit.OpClaims, 2)
		e.gh.Barrier(testkit.OpPost, 2)
		errs := make(chan error, 2)
		for _, c := range claimers {
			go func() {
				_, err := c.claiming.Acquire(context.Background(), ghRef(12), domain.ClaimTask)
				errs <- err
			}()
		}
		synctest.Wait()
		e.clock.Advance(domain.DefaultClaimRereadDelay)
		var won int
		for range claimers {
			if err := <-errs; err == nil {
				won++
			} else if !errors.As(err, new(*app.ClaimTakenError)) {
				t.Errorf("Acquire() = %v", err)
			}
		}
		var first string
		for _, r := range e.gh.Requests() {
			if r.Op == testkit.OpPost {
				first = r.Login
				break
			}
		}
		item := e.item()
		if won != 1 || !slices.Equal(item.Assignees, []string{first}) || len(e.claimComments(ghRef(12))) != 1 {
			t.Errorf("%d winners, assignees %v, first poster %s", won, item.Assignees, first)
		}
	})
}

// Ant-execution "Claim lost at start": the check finds a colleague's live
// claim, so nothing is posted and the error names the holder.
func TestAcquireClaimedByAColleague(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		b := e.instance(instanceB, "lbianchi")
		b.mustAcquire(ghRef(12), domain.ClaimTask)
		a := e.instance(instanceA, "mrosani")
		_, err := a.acquire(ghRef(12), domain.ClaimTask)
		if err == nil || err.Error() != "claimed by "+instanceB {
			t.Errorf("Acquire() = %v, want claimed by %s", err, instanceB)
		}
		if n := e.ops(testkit.OpPost, "mrosani"); n != 0 {
			t.Errorf("A posted %d comments", n)
		}
	})
}

// Scenario "Forged claim marker": a copied claim marker from a user
// without write access is not a claim.
func TestForgedClaimMarkerIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		forged := domain.Claim{Kind: domain.ClaimTask, Instance: "outsider@PC", Ref: ghRef(12).String(), Lease: domain.DefaultLease}
		e.gh.Comment(ghRef(12), "outsider", forged.Comment(t0).String())
		a := e.instance(instanceA, "mrosani")
		if held, err := a.acquire(ghRef(12), domain.ClaimTask); err != nil || held.Instance != instanceA {
			t.Errorf("Acquire() = %+v, %v", held, err)
		}
	})
}

// Scenario "Azure DevOps optimistic concurrency": the mark is sent with
// the revision of the re-read, so a change of the work item between the
// re-read and the mark is a lost race: the comment is retracted and
// nothing else is written.
func TestAzureDevOpsOptimisticConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		ado12 := adoRef(12)
		e.gh.PutItem(domain.WorkItem{Ref: ado12, Title: "Fix login", State: domain.StateOpen, Labels: []string{"anthill"}})
		e.gh.AddWriter(adoShop, "mrosani", "lbianchi")
		a := e.instance(instanceA, "mrosani")
		done := make(chan error, 1)
		go func() {
			_, err := a.claiming.Acquire(context.Background(), ado12, domain.ClaimTask)
			done <- err
		}()
		synctest.Wait() // A posted and waits to re-read
		e.gh.Barrier(testkit.OpClaims, 2)
		e.clock.Advance(domain.DefaultClaimRereadDelay)
		synctest.Wait() // A re-read and is held at the barrier
		e.gh.EditItem(ado12, func(w *domain.WorkItem) { w.Labels = append(w.Labels, "bug") })
		if _, err := e.gh.Client("lbianchi").Claims(context.Background(), ado12); err != nil {
			t.Fatal(err)
		}
		err := <-done
		if !errors.As(err, new(*app.ClaimTakenError)) {
			t.Errorf("Acquire() = %v, want a lost race", err)
		}
		item, _ := e.gh.Item(ado12)
		if len(e.claimComments(ado12)) != 0 || slices.Contains(item.Labels, domain.DefaultClaimLabel) || len(item.Assignees) != 0 || len(a.held()) != 0 {
			t.Errorf("after the conflict: %+v, held %v", item, a.held())
		}
		if n := e.ops(testkit.OpMark, "mrosani"); n != 1 {
			t.Errorf("%d marks, want 1, never retried", n)
		}
	})
}

// A failure after the post undoes it: the comment is retracted and the
// label removed (spec ant-execution, "Starting an ant on acceptance").
func TestAcquireMarkFailureUndone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		e.gh.FailNext(testkit.OpMark, errors.New("server error"))
		if _, err := a.acquire(ghRef(12), domain.ClaimTask); err == nil || errors.As(err, new(*app.ClaimTakenError)) {
			t.Errorf("Acquire() = %v, want the mark failure", err)
		}
		if len(e.claimComments(ghRef(12))) != 0 || len(a.held()) != 0 {
			t.Errorf("comments %v, held %v", e.claimComments(ghRef(12)), a.held())
		}
	})
}

// A crash between the post and the mark leaves this instance's comment;
// the next acquisition takes it up instead of posting a second one.
func TestAcquireTakesUpItsOwnComment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		claim := domain.Claim{Kind: domain.ClaimTask, Instance: instanceA, Ref: ghRef(12).String(), Lease: domain.DefaultLease}
		left := e.gh.Comment(ghRef(12), "mrosani", claim.Comment(t0).String())
		a := e.instance(instanceA, "mrosani")
		held := a.mustAcquire(ghRef(12), domain.ClaimTask)
		if held.CommentID != left.ID || e.ops(testkit.OpPost, "mrosani") != 0 || !slices.Contains(e.item().Labels, domain.DefaultClaimLabel) {
			t.Errorf("held %+v, posts %d", held, e.ops(testkit.OpPost, "mrosani"))
		}
	})
}

// Task 10.2: renewal edits the claim comment, which moves its provider
// timestamp, and posts nothing (spec work-claiming, "Leases and renewal").
func TestRenewalEditsTheClaimComment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			a.claiming.Run(ctx)
			close(done)
		}()
		synctest.Wait() // the first renewal ran at once
		for range 4 {
			e.clock.Advance(5 * time.Minute) // a third of the lease
			synctest.Wait()
		}
		cancel()
		<-done

		claims := e.claimComments(ghRef(12))
		renewedAt := t0.Add(domain.DefaultClaimRereadDelay + 20*time.Minute)
		if len(claims) != 1 || !claims[0].UpdatedAt.Equal(renewedAt) || claims[0].Status(renewedAt.Add(14*time.Minute)) != domain.ClaimLive {
			t.Errorf("claims = %+v, want one comment last edited at %v", claims, renewedAt)
		}
		if held := a.held(); len(held) != 1 || !held[0].UpdatedAt.Equal(renewedAt) {
			t.Errorf("held = %+v", held)
		}
		if n := e.ops(testkit.OpEdit, "mrosani"); n != 5 {
			t.Errorf("%d edits, want 5", n)
		}
		if !strings.Contains(e.item().Comments[0].Body, "until 11:05 UTC") {
			t.Errorf("comment = %q, want the new lease end", e.item().Comments[0].Body)
		}
	})
}

// While the provider is unreachable, the claim is kept and nothing ends;
// the next successful read decides.
func TestRenewalWhileProviderUnreachable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		e.gh.Fail(testkit.OpClaims, errors.New("connection refused"))
		e.clock.Advance(5 * time.Minute)
		if err := a.claiming.Renew(context.Background()); err == nil {
			t.Error("Renew() succeeded while the provider is unreachable")
		}
		if len(a.held()) != 1 || len(a.loss.Losses()) != 0 || e.ops(testkit.OpEdit, "mrosani") != 0 {
			t.Errorf("held %v, losses %v", a.held(), a.loss.Losses())
		}
		e.gh.Fail(testkit.OpClaims, nil)
		if err := a.claiming.Renew(context.Background()); err != nil || e.ops(testkit.OpEdit, "mrosani") != 1 {
			t.Errorf("Renew() = %v after the provider came back", err)
		}
	})
}

// Task 10.2, scenario "Ant finished, PR open": the parked claim is not
// renewed, and other instances treat it as live however long it lasts.
func TestAntFinishedPROpenClaimParked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		if err := a.claiming.Park(context.Background(), ghRef(12)); err != nil {
			t.Fatal(err)
		}
		if claims := e.claimComments(ghRef(12)); len(claims) != 1 || !claims[0].Parked || !a.held()[0].Parked {
			t.Fatalf("claims %+v, held %+v", claims, a.held())
		}
		edits := e.ops(testkit.OpEdit, "mrosani")
		e.clock.Advance(7 * 24 * time.Hour)
		if err := a.claiming.Renew(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := e.ops(testkit.OpEdit, "mrosani"); n != edits {
			t.Errorf("a parked claim was renewed %d times", n-edits)
		}
		if _, err := b.acquire(ghRef(12), domain.ClaimTask); err == nil || err.Error() != "claimed by "+instanceA {
			t.Errorf("B: Acquire() = %v, want claimed by A", err)
		}

		// A follow-up run: the claim is renewed again.
		if err := a.claiming.Unpark(context.Background(), ghRef(12)); err != nil {
			t.Fatal(err)
		}
		if claims := e.claimComments(ghRef(12)); claims[0].Parked || !claims[0].UpdatedAt.Equal(e.clock.Now()) {
			t.Errorf("after unpark: %+v", claims[0])
		}
	})
}

// releaseCase acquires a claim for A, releases it as the trigger of one
// release case does, and checks what the release left: the comment
// marked released, the label removed, A's assignment undone, nothing held,
// and the item or PR free for B.
func releaseCase(t *testing.T, ref domain.Ref, kind domain.ClaimKind, end func(e *claimEnv)) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		a.mustAcquire(ref, kind)
		if end != nil {
			end(e)
		}
		if err := a.claiming.Release(context.Background(), ref, kind); err != nil {
			t.Fatal(err)
		}
		claims := e.claimComments(ref)
		if len(claims) != 1 || !claims[0].Released {
			t.Errorf("claims = %+v, want A's comment marked released", claims)
		}
		var labels, assignees []string
		if ref.Kind == domain.KindPR {
			pr, _ := e.gh.PullRequest(ref)
			labels = pr.Labels
		} else {
			labels, assignees = e.item().Labels, e.item().Assignees
		}
		if slices.Contains(labels, domain.DefaultClaimLabel) || len(assignees) != 0 || len(a.held()) != 0 {
			t.Errorf("labels %v, assignees %v, held %v", labels, assignees, a.held())
		}
		if got := e.holder(ref, kind); got != "" {
			t.Errorf("held by %q after the release", got)
		}
		b.mustAcquire(ref, kind)
	})
}

// Task 10.2, work-claiming "Release": the ticket is closed.
func TestReleaseTicketClosed(t *testing.T) {
	releaseCase(t, ghRef(12), domain.ClaimTask, func(e *claimEnv) {
		e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.State = domain.StateClosedCompleted })
	})
}

// Task 10.2, work-claiming "Release": the PR is merged or closed, which
// ends the review claim on it.
func TestReleasePRMergedOrClosed(t *testing.T) {
	releaseCase(t, ghPR(34), domain.ClaimReview, nil)
}

// Task 10.2, scenario "Queen abandons an ant": the claim on #12 is
// released and #12 can be proposed to other queens again.
func TestReleaseQueenAbandonsAnAnt(t *testing.T) {
	releaseCase(t, ghRef(12), domain.ClaimTask, nil)
}

// Task 10.2, work-claiming "Release": the ant finishes with no-change.
func TestReleaseAntFinishesWithNoChange(t *testing.T) {
	releaseCase(t, ghRef(12), domain.ClaimTask, func(e *claimEnv) {
		e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.Labels = append(w.Labels, "anthill:hold") })
	})
}

// Task 10.2, work-claiming "Release": a review activity finishes.
func TestReleaseReviewActivityFinished(t *testing.T) {
	releaseCase(t, ghPR(34), domain.ClaimReview, nil)
}

// Task 10.2: the release unassigns the developer only if Anthill made the
// assignment, and remembers it across a restart.
func TestReleaseUnassignsOnlyWhenAnthillAssigned(t *testing.T) {
	tests := []struct {
		name          string
		preassigned   bool
		wantAssignees []string
	}{
		{"assigned by Anthill", false, nil},
		{"already assigned by a developer", true, []string{"mrosani"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newClaimEnv(t)
				if tt.preassigned {
					e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.Assignees = []string{"mrosani"} })
				}
				a := e.instance(instanceA, "mrosani")
				if held := a.mustAcquire(ghRef(12), domain.ClaimTask); held.Assigned == tt.preassigned {
					t.Errorf("Assigned = %v", held.Assigned)
				}
				e.restart(a, "mrosani")
				if err := a.claiming.Release(context.Background(), ghRef(12), domain.ClaimTask); err != nil {
					t.Fatal(err)
				}
				if got := e.item().Assignees; !slices.Equal(got, tt.wantAssignees) {
					t.Errorf("assignees = %v, want %v", got, tt.wantAssignees)
				}
			})
		})
	}
}

// A release after another instance took the claim marks only this
// instance's comment, and leaves the label and assignee to the holder.
func TestReleaseLeavesAnotherHoldersLabel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		e.clock.Advance(time.Hour) // A's lease expired
		b.mustAcquire(ghRef(12), domain.ClaimTask)
		if err := a.claiming.Release(context.Background(), ghRef(12), domain.ClaimTask); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(e.item().Labels, domain.DefaultClaimLabel) || e.ops(testkit.OpUnmark, "mrosani") != 0 || e.holder(ghRef(12), domain.ClaimTask) != instanceB {
			t.Errorf("item %+v", e.item())
		}
	})
}

// Releasing a claim not held does nothing.
func TestReleaseNotHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		if err := a.claiming.Release(context.Background(), ghRef(12), domain.ClaimTask); err != nil {
			t.Fatal(err)
		}
		if len(e.gh.Requests()) != 0 {
			t.Errorf("requests %v", e.gh.Requests())
		}
	})
}

// Task 10.3, scenario "Holder returns after expiry": A's laptop slept past
// its lease, and B claimed #12 meanwhile. At its next renewal A ends any
// active run, sets its ant to abandoned with reason "claim lost", keeps
// its worktree and tells its queen. It leaves B's label alone.
func TestHolderReturnsAfterExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		ant := a.createAnt(domain.AntStarting, domain.AntRunning)
		e.clock.Advance(40 * time.Minute) // the laptop sleeps
		b.mustAcquire(ghRef(12), domain.ClaimTask)

		if err := a.claiming.Renew(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := a.loss.Losses(); len(got) != 1 || got[0] != (testkit.ClaimLoss{Ref: ghRef(12), Kind: domain.ClaimTask, Reason: "claim lost"}) {
			t.Errorf("losses = %v, want the active run ended", got)
		}
		r := a.ant(ant)
		if last := r.History[len(r.History)-1]; r.State() != domain.AntAbandoned || last.Reason != "claim lost" || r.Worktree != "/work/gh-12" {
			t.Errorf("ant: state %s, reason %q, worktree %q", r.State(), last.Reason, r.Worktree)
		}
		note := lastNotification(t, a.notifier)
		if note.Kind != ports.NotifyClaimLost || note.Body != "claimed by "+instanceB || note.Title != "Claim lost on gh-12" {
			t.Errorf("notification = %+v", note)
		}
		if len(a.held()) != 0 || e.ops(testkit.OpUnmark, "mrosani") != 0 || e.holder(ghRef(12), domain.ClaimTask) != instanceB {
			t.Errorf("held %v, A unmarked %d times", a.held(), e.ops(testkit.OpUnmark, "mrosani"))
		}
		if item := e.item(); !slices.Contains(item.Labels, domain.DefaultClaimLabel) || !slices.Contains(item.Assignees, "lbianchi") {
			t.Errorf("B's label or assignee was removed: %+v", item)
		}
	})
}

// A lease that ran out with nobody else claiming is lost too: the holder
// never revives it by editing.
func TestExpiredClaimNotRevived(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		e.clock.Advance(20 * time.Minute)
		if err := a.claiming.Renew(context.Background()); err != nil {
			t.Fatal(err)
		}
		if claims := e.claimComments(ghRef(12)); len(claims) != 1 || !claims[0].Released {
			t.Errorf("claims = %+v", claims)
		}
		if lastNotification(t, a.notifier).Body != "lease expired" || slices.Contains(e.item().Labels, domain.DefaultClaimLabel) || len(e.item().Assignees) != 0 {
			t.Errorf("item %+v", e.item())
		}
	})
}

// Task 10.3, scenario "Stuck claim released by a colleague": a developer
// comments /anthill release on #12, which an offline machine still holds
// as parked. All instances treat #12 as unclaimed from the next poll, and
// the holder, when it returns, abandons its ant.
func TestStuckClaimReleasedByAColleague(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		ant := a.createAnt(domain.AntStarting, domain.AntRunning, domain.AntFinishing, domain.AntSucceeded)
		if err := a.claiming.Park(context.Background(), ghRef(12)); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(72 * time.Hour) // A goes offline for days
		release := e.gh.Comment(ghRef(12), "ada", "This machine is gone.\n/anthill release")

		if got := e.holder(ghRef(12), domain.ClaimTask); got != "" {
			t.Errorf("held by %q after the release command", got)
		}
		b.mustAcquire(ghRef(12), domain.ClaimTask)

		// A comes back and handles the comment from its outbox.
		event := domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: "ada", Origin: domain.OriginHuman, CommentID: release.ID, Body: release.Body}
		if err := a.claiming.HandleChange(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		r := a.ant(ant)
		if r.State() != domain.AntAbandoned || r.History[len(r.History)-1].Reason != "claim lost" || len(a.loss.Losses()) != 1 {
			t.Errorf("ant %s, losses %v", r.State(), a.loss.Losses())
		}
		if note := lastNotification(t, a.notifier); note.Body != "released by ada" {
			t.Errorf("notification = %+v", note)
		}
		// B holds the claim now, so A leaves the label and assignee alone.
		if item := e.item(); !slices.Contains(item.Labels, domain.DefaultClaimLabel) || !slices.Contains(item.Assignees, "lbianchi") || e.holder(ghRef(12), domain.ClaimTask) != instanceB {
			t.Errorf("item after A returned: %+v", item)
		}
	})
}

// Work-claiming "Release does not hand over the PR": the force-release
// frees the claim on #12 but leaves the ant PR as it is.
func TestForceReleaseDoesNotHandOverThePR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		before, _ := e.gh.PullRequest(ghPR(34))
		release := e.gh.Comment(ghRef(12), "lbianchi", "/anthill release")
		event := domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Actor: "lbianchi", Origin: domain.OriginHuman, Body: release.Body}
		if err := a.claiming.HandleChange(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		after, _ := e.gh.PullRequest(ghPR(34))
		if e.holder(ghRef(12), domain.ClaimTask) != "" || slices.Contains(e.item().Labels, domain.DefaultClaimLabel) || len(e.item().Assignees) != 0 {
			t.Errorf("item %+v", e.item())
		}
		if after.Revision != before.Revision {
			t.Errorf("the ant PR was changed: %+v", after)
		}
	})
}

// Work-claiming "Manual override": removing the claim label from a live
// claim is noticed by the holder at the change event.
func TestForceReleaseByRemovingTheLabel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		ant := a.createAnt(domain.AntStarting, domain.AntRunning)
		e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.Labels = []string{"anthill"} })
		event := domain.ChangeEvent{Ref: ghRef(12), Kind: domain.ChangeLabelRemoved, Actor: "lbianchi", Origin: domain.OriginHuman, Label: domain.DefaultClaimLabel}
		if err := a.claiming.HandleChange(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if a.ant(ant).State() != domain.AntAbandoned || lastNotification(t, a.notifier).Body != "claim label removed" {
			t.Errorf("ant %s, notifications %v", a.ant(ant).State(), a.notifier.Sent())
		}
		if claims := e.claimComments(ghRef(12)); !claims[0].Released || len(e.item().Assignees) != 0 || len(a.held()) != 0 {
			t.Errorf("claims %+v, item %+v", claims, e.item())
		}
	})
}

// A parked claim whose label was removed is released for every instance,
// even while its holder is offline.
func TestParkedClaimLabelRemovedReleasedForEveryone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a, b := e.instance(instanceA, "mrosani"), e.instance(instanceB, "lbianchi")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		if err := a.claiming.Park(context.Background(), ghRef(12)); err != nil {
			t.Fatal(err)
		}
		e.gh.EditItem(ghRef(12), func(w *domain.WorkItem) { w.Labels = []string{"anthill"} })
		if held := b.mustAcquire(ghRef(12), domain.ClaimTask); held.Instance != instanceB {
			t.Errorf("B: %+v", held)
		}
	})
}

// Renewal catches a force-release whose event was missed, and a release
// command from a user without write access changes nothing.
func TestRenewalChecksReleaseCommands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		e.gh.Comment(ghRef(12), "outsider", "/anthill release")
		if err := a.claiming.Renew(context.Background()); err != nil || len(a.held()) != 1 || len(a.loss.Losses()) != 0 {
			t.Fatalf("outsider's release: err %v, held %v", err, a.held())
		}
		e.gh.Comment(ghRef(12), "ada", "/anthill release")
		if err := a.claiming.Renew(context.Background()); err != nil || len(a.held()) != 0 || len(a.loss.Losses()) != 1 {
			t.Errorf("writer's release: err %v, held %v", err, a.held())
		}
	})
}

// Other events, and changes Anthill made, do not reach the provider.
func TestHandleChangeIgnoresOtherEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		before := len(e.gh.Requests())
		for _, event := range []domain.ChangeEvent{
			{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Origin: domain.OriginHuman, Body: "thanks"},
			{Ref: ghRef(12), Kind: domain.ChangeLabelRemoved, Origin: domain.OriginHuman, Label: "bug"},
			{Ref: ghRef(12), Kind: domain.ChangeCommentAdded, Origin: domain.OriginAnthill, Body: "/anthill release"},
			{Ref: ghRef(13), Kind: domain.ChangeLabelRemoved, Origin: domain.OriginHuman, Label: domain.DefaultClaimLabel},
		} {
			if err := a.claiming.HandleChange(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		if n := len(e.gh.Requests()); n != before || len(a.held()) != 1 {
			t.Errorf("%d requests, held %v", n-before, a.held())
		}
	})
}

// Until the loss handler has ended the work, the claim stays held, and
// the next renewal tries again.
func TestClaimLossRetriedAfterHandlerFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		ant := a.createAnt(domain.AntStarting, domain.AntRunning)
		e.clock.Advance(time.Hour)
		a.loss.Err = errors.New("run did not stop")
		if err := a.claiming.Renew(context.Background()); err == nil || len(a.held()) != 1 || a.ant(ant).State() != domain.AntRunning {
			t.Fatalf("err %v, held %v, ant %s", err, a.held(), a.ant(ant).State())
		}
		a.loss.Err = nil
		if err := a.claiming.Renew(context.Background()); err != nil || len(a.held()) != 0 || a.ant(ant).State() != domain.AntAbandoned {
			t.Errorf("err %v, held %v, ant %s", err, a.held(), a.ant(ant).State())
		}
		if n := len(a.notifier.Sent()); n != 1 {
			t.Errorf("%d notifications, want 1", n)
		}
	})
}

// Parking a claim that was lost ends its work and fails.
func TestParkLostClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newClaimEnv(t)
		a := e.instance(instanceA, "mrosani")
		a.mustAcquire(ghRef(12), domain.ClaimTask)
		e.clock.Advance(time.Hour)
		if err := a.claiming.Park(context.Background(), ghRef(12)); !errors.As(err, new(*app.ClaimTakenError)) {
			t.Errorf("Park() = %v", err)
		}
		if len(a.loss.Losses()) != 1 || len(a.held()) != 0 {
			t.Errorf("losses %v, held %v", a.loss.Losses(), a.held())
		}
	})
}
