package testkit_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

var (
	t0     = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ghRepo = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}
	item12 = domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 12}
)

func newProvider(t *testing.T) (*testkit.Provider, *testkit.Clock) {
	t.Helper()
	clock := testkit.NewClock(t0)
	p := testkit.NewProvider(clock)
	p.PutItem(domain.WorkItem{Ref: item12, Title: "Fix login", State: domain.StateOpen, Labels: []string{"anthill"}})
	p.AddWriter(ghRepo, "alice", "bob")
	return p, clock
}

func claimComment(instance string) domain.AnthillComment {
	c := domain.Claim{Kind: domain.ClaimTask, Instance: instance, Ref: item12.String(), Lease: domain.DefaultLease}
	return c.Comment(t0.Add(domain.DefaultLease))
}

// claim runs the claim protocol of work-claiming for one instance: check,
// post, re-read, earliest wins, and only the winner marks.
func claim(ctx context.Context, store ports.ClaimStore, instance string, writers domain.UserSet) (bool, error) {
	page, err := store.Claims(ctx, item12)
	if err != nil {
		return false, err
	}
	if _, held := domain.ResolveClaim(page.Comments, item12, domain.ClaimTask, writers, page.ServerTime); held {
		return false, nil
	}
	mine, err := store.Post(ctx, item12, claimComment(instance))
	if err != nil {
		return false, err
	}
	page, err = store.Claims(ctx, item12)
	if err != nil {
		return false, err
	}
	winner, _ := domain.ResolveClaim(page.Comments, item12, domain.ClaimTask, writers, page.ServerTime)
	if winner.Instance != instance {
		return false, store.Retract(ctx, item12, mine.ID)
	}
	_, err = store.Mark(ctx, item12, ports.ClaimMark{Label: "anthill:claimed", Assign: true})
	return true, err
}

func TestProviderTwoInstancesClaimAtOnce(t *testing.T) {
	ctx := context.Background()
	p, _ := newProvider(t)
	// Both instances check before either posts, and both post before
	// either re-reads.
	p.Barrier(testkit.OpClaims, 2)
	p.Barrier(testkit.OpPost, 2)
	writers := domain.NewUserSet("alice", "bob")
	instances := map[string]string{"alice@laptop": "alice", "bob@desktop": "bob"}
	won := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for instance, login := range instances {
		wg.Go(func() {
			ok, err := claim(ctx, p.Client(login), instance, writers)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			won[instance] = ok
			mu.Unlock()
		})
	}
	wg.Wait()

	var posts []testkit.Request
	for _, r := range p.Requests() {
		if r.Op == testkit.OpPost {
			posts = append(posts, r)
		}
	}
	if len(posts) != 2 {
		t.Fatalf("posts = %+v", posts)
	}
	// The provider's order decides: the first post wins.
	first := map[string]string{"alice": "alice@laptop", "bob": "bob@desktop"}[posts[0].Login]
	for instance, ok := range won {
		if ok != (instance == first) {
			t.Errorf("%s won = %v, first poster was %s", instance, ok, first)
		}
	}
	item, _ := p.Item(item12)
	if len(item.Comments) != 1 || !slices.Contains(item.Labels, "anthill:claimed") || !slices.Equal(item.Assignees, []string{posts[0].Login}) {
		t.Errorf("item after the race = %+v", item)
	}
}

func TestProviderCommentOrderAndTimes(t *testing.T) {
	ctx := context.Background()
	p, clock := newProvider(t)
	alice := p.Client("alice")
	a, _ := alice.Post(ctx, item12, claimComment("alice@laptop"))
	clock.Advance(time.Minute)
	b := p.Comment(item12, "carol", "a human comment")
	if !a.CreatedAt.Equal(t0) || !b.CreatedAt.Equal(t0.Add(time.Minute)) || a.Author != "alice" || b.Author != "carol" {
		t.Errorf("comments = %+v, %+v", a, b)
	}
	if ida, idb := atoi(t, a.ID), atoi(t, b.ID); ida >= idb {
		t.Errorf("ids %q, %q are not increasing numbers", a.ID, b.ID)
	}
	clock.Advance(time.Minute)
	edited, err := alice.Edit(ctx, item12, a.ID, claimComment("alice@laptop"))
	if err != nil || !edited.UpdatedAt.Equal(t0.Add(2*time.Minute)) || !edited.CreatedAt.Equal(t0) {
		t.Errorf("Edit() = %+v, %v", edited, err)
	}
	page, _ := alice.Claims(ctx, item12)
	if !page.ServerTime.Equal(t0.Add(2*time.Minute)) || len(page.Comments) != 2 || page.Revision == "" {
		t.Errorf("Claims() = %+v", page)
	}
	if _, err := alice.Edit(ctx, item12, "999", claimComment("x")); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Edit of a missing comment: %v", err)
	}
	if err := alice.Retract(ctx, item12, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := alice.Retract(ctx, item12, a.ID); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("second Retract: %v", err)
	}
}

func TestProviderReadLag(t *testing.T) {
	ctx := context.Background()
	p, clock := newProvider(t)
	p.ReadLag(3 * time.Second)
	alice := p.Client("alice")
	if _, err := alice.Post(ctx, item12, claimComment("alice@laptop")); err != nil {
		t.Fatal(err)
	}
	page, _ := alice.Claims(ctx, item12)
	if len(page.Comments) != 0 {
		t.Errorf("a fresh comment is visible under read lag")
	}
	clock.Advance(3 * time.Second)
	page, _ = alice.Claims(ctx, item12)
	if len(page.Comments) != 1 {
		t.Errorf("comment not visible after the lag")
	}
	if item, _ := p.Item(item12); len(item.Comments) != 1 {
		t.Errorf("Provider.Item applies the lag")
	}
}

func TestProviderMarkAndUnmark(t *testing.T) {
	ctx := context.Background()
	p, _ := newProvider(t)
	alice := p.Client("alice")
	page, _ := alice.Claims(ctx, item12)

	// A revision that moved is a lost race.
	p.EditItem(item12, func(i *domain.WorkItem) { i.Body = "edited" })
	if _, err := alice.Mark(ctx, item12, ports.ClaimMark{Label: "anthill:claimed", Assign: true, Revision: page.Revision}); !errors.Is(err, ports.ErrConflict) {
		t.Errorf("Mark with an old revision: %v", err)
	}
	page, _ = alice.Claims(ctx, item12)
	assigned, err := alice.Mark(ctx, item12, ports.ClaimMark{Label: "anthill:claimed", Assign: true, Revision: page.Revision})
	if err != nil || !assigned {
		t.Errorf("Mark() = %v, %v", assigned, err)
	}
	if again, _ := alice.Mark(ctx, item12, ports.ClaimMark{Label: "anthill:claimed", Assign: true}); again {
		t.Error("Mark reported an assignment that already existed")
	}
	if err := alice.Unmark(ctx, item12, "anthill:claimed", true); err != nil {
		t.Fatal(err)
	}
	item, _ := p.Item(item12)
	if slices.Contains(item.Labels, "anthill:claimed") || len(item.Assignees) != 0 {
		t.Errorf("after Unmark: %+v", item)
	}

	// A developer assigned by hand stays assigned when unassign is false.
	p.EditItem(item12, func(i *domain.WorkItem) { i.Assignees = []string{"alice"} })
	if assigned, _ := alice.Mark(ctx, item12, ports.ClaimMark{Label: "anthill:claimed", Assign: true}); assigned {
		t.Error("Mark reported making a manual assignment")
	}
	_ = alice.Unmark(ctx, item12, "anthill:claimed", false)
	if item, _ := p.Item(item12); !slices.Equal(item.Assignees, []string{"alice"}) {
		t.Errorf("assignees = %v", item.Assignees)
	}
}

func TestProviderRateLimit(t *testing.T) {
	ctx := context.Background()
	p, clock := newProvider(t)
	reset := t0.Add(10 * time.Minute)
	p.RateLimit(reset)
	alice := p.Client("alice")
	_, err := alice.Items(ctx, ghRepo, "")
	var limited *ports.RateLimitError
	if !errors.As(err, &limited) || !limited.Reset.Equal(reset) || !errors.Is(err, ports.ErrRateLimited) {
		t.Fatalf("Items() under a rate limit: %v", err)
	}
	clock.Set(reset)
	if _, err := alice.Items(ctx, ghRepo, ""); err != nil {
		t.Fatalf("Items() after the reset: %v", err)
	}
	// The log shows every request, so a test can check none was sent
	// before the reset.
	reqs := p.Requests()
	if len(reqs) != 2 || !reqs[0].At.Equal(t0) || !reqs[1].At.Equal(reset) {
		t.Errorf("Requests() = %+v", reqs)
	}
}

func TestProviderInjectedErrors(t *testing.T) {
	ctx := context.Background()
	p, _ := newProvider(t)
	alice := p.Client("alice")
	boom, again := errors.New("boom"), errors.New("again")
	p.FailNext(testkit.OpItem, boom)
	p.FailNext(testkit.OpItem, again)
	if _, _, err := alice.Item(ctx, item12); !errors.Is(err, boom) {
		t.Errorf("first call: %v", err)
	}
	if _, _, err := alice.Item(ctx, item12); !errors.Is(err, again) {
		t.Errorf("second call: %v", err)
	}
	if _, _, err := alice.Item(ctx, item12); err != nil {
		t.Errorf("third call: %v", err)
	}
	p.Fail(testkit.OpItems, boom)
	for range 2 {
		if _, err := alice.Items(ctx, ghRepo, ""); !errors.Is(err, boom) {
			t.Errorf("Items(): %v", err)
		}
	}
	p.Fail(testkit.OpItems, nil)
	if _, err := alice.Items(ctx, ghRepo, ""); err != nil {
		t.Errorf("Items() after clearing: %v", err)
	}
	if _, _, err := alice.Item(ctx, domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 99}); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("missing item: %v", err)
	}
}

func TestProviderPollingCursor(t *testing.T) {
	ctx := context.Background()
	p, _ := newProvider(t)
	alice := p.Client("alice")
	other := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "other"}
	p.PutItem(domain.WorkItem{Ref: domain.Ref{Repo: other, Kind: domain.KindItem, Number: 1}})
	page, err := alice.Items(ctx, ghRepo, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Ref != item12 || !page.ServerTime.Equal(t0) {
		t.Fatalf("first poll = %+v, %v", page, err)
	}
	if next, _ := alice.Items(ctx, ghRepo, page.Cursor); len(next.Items) != 0 {
		t.Errorf("no change, but the poll returned %+v", next.Items)
	}
	p.Comment(item12, "carol", "hi")
	next, _ := alice.Items(ctx, ghRepo, page.Cursor)
	if len(next.Items) != 1 || len(next.Items[0].Comments) != 1 || next.Items[0].Revision == page.Items[0].Revision {
		t.Errorf("poll after a comment = %+v", next.Items)
	}
	// Returned items are copies.
	next.Items[0].Labels[0] = "changed"
	if item, _ := p.Item(item12); item.Labels[0] != "anthill" {
		t.Error("changing a returned item changed the provider")
	}
}

func TestProviderCommentsAndLabels(t *testing.T) {
	ctx := context.Background()
	p, _ := newProvider(t)
	alice := p.Client("alice")
	question := domain.AnthillComment{Role: domain.RoleAnt, Body: "30 or 60 seconds?", Marker: domain.Marker{
		Version: domain.MarkerVersion, ID: "q-1", Instance: "alice@laptop", Kind: domain.CommentQuestion, Ref: item12.String(),
	}}
	first, err := alice.AddComment(ctx, item12, question)
	if err != nil {
		t.Fatal(err)
	}
	// The same marker ID is not posted twice (design D7).
	second, _ := alice.AddComment(ctx, item12, question)
	if second.ID != first.ID {
		t.Errorf("second post made comment %s, want %s", second.ID, first.ID)
	}
	if item, _ := p.Item(item12); len(item.Comments) != 1 || item.Comments[0].Origin() != domain.OriginAnthill {
		t.Errorf("comments = %+v", item.Comments)
	}
	question.Body = "updated"
	if c, err := alice.EditComment(ctx, item12, first.ID, question); err != nil || c.Body != question.String() {
		t.Errorf("EditComment() = %+v, %v", c, err)
	}

	if err := alice.AddLabel(ctx, item12, "anthill:hold"); err != nil {
		t.Fatal(err)
	}
	_ = alice.AddLabel(ctx, item12, "anthill:hold")
	if item, _ := p.Item(item12); !slices.Equal(item.Labels, []string{"anthill", "anthill:hold"}) {
		t.Errorf("labels = %v", item.Labels)
	}
	_ = alice.RemoveLabel(ctx, item12, "anthill:hold")
	_ = alice.RemoveLabel(ctx, item12, "missing")
	if item, _ := p.Item(item12); !slices.Equal(item.Labels, []string{"anthill"}) {
		t.Errorf("labels = %v", item.Labels)
	}

	for login, want := range map[string]bool{"bob": true, "mallory": false} {
		if ok, err := alice.CanWrite(ctx, ghRepo, login); err != nil || ok != want {
			t.Errorf("CanWrite(%s) = %v, %v", login, ok, err)
		}
	}
}

func TestProviderPullRequests(t *testing.T) {
	ctx := context.Background()
	p, _ := newProvider(t)
	alice := p.Client("alice")
	pr, err := alice.OpenPullRequest(ctx, ports.NewPullRequest{
		Repo: ghRepo, Source: "ant/gh-12-fix-login", Target: "main", Title: "Fix login", Body: "Closes #12", Labels: []string{"anthill"},
	})
	if err != nil || pr.Ref.Kind != domain.KindPR || pr.Author != "alice" || pr.Draft {
		t.Fatalf("OpenPullRequest() = %+v, %v", pr, err)
	}
	draft := true
	if updated, err := alice.UpdatePullRequest(ctx, pr.Ref, ports.PullRequestUpdate{Draft: &draft}); err != nil || !updated.Draft || updated.Title != "Fix login" {
		t.Errorf("UpdatePullRequest() = %+v, %v", updated, err)
	}
	finding := domain.AnthillComment{Role: domain.RoleShepherd, Body: "null dereference", Marker: domain.Marker{
		Version: domain.MarkerVersion, Instance: "bob@desktop", Kind: domain.CommentFinding, Ref: pr.Ref.String(),
	}}
	if _, err := alice.AddComment(ctx, pr.Ref, finding); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.AddReviewComment(ctx, pr.Ref, ports.ReviewComment{Path: "src/auth.ts", StartLine: 42, Comment: finding}); err != nil {
		t.Fatal(err)
	}
	got, now, _ := alice.PullRequest(ctx, pr.Ref)
	if len(got.Threads) != 1 || got.Threads[0].Line != 42 || len(got.Comments) != 1 || !now.Equal(t0) {
		t.Fatalf("PullRequest() = %+v", got)
	}
	thread := got.Threads[0].ID
	if _, err := alice.ReplyToThread(ctx, pr.Ref, thread, finding); err != nil {
		t.Fatal(err)
	}
	if err := alice.ResolveThread(ctx, pr.Ref, thread); err != nil {
		t.Fatal(err)
	}
	if err := alice.ResolveThread(ctx, pr.Ref, "none"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("ResolveThread of a missing thread: %v", err)
	}
	page, _ := alice.PullRequests(ctx, ghRepo, "")
	if len(page.PullRequests) != 1 || !page.PullRequests[0].Threads[0].Resolved || len(page.PullRequests[0].Threads[0].Comments) != 2 {
		t.Errorf("PullRequests() = %+v", page)
	}
	p.EditPullRequest(pr.Ref, func(pr *domain.PullRequest) { pr.HeadCommit = "def" })
	if next, _ := alice.PullRequests(ctx, ghRepo, page.Cursor); len(next.PullRequests) != 1 || next.PullRequests[0].HeadCommit != "def" {
		t.Errorf("poll after a push = %+v", next)
	}
	// PR numbers continue after PRs put by the test.
	p.PutPullRequest(domain.PullRequest{Ref: domain.Ref{Repo: ghRepo, Kind: domain.KindPR, Number: 40}})
	if next, _ := alice.OpenPullRequest(ctx, ports.NewPullRequest{Repo: ghRepo}); next.Ref.Number != 41 {
		t.Errorf("next PR number = %d", next.Ref.Number)
	}
}

func TestProviderBarrierHonoursContext(t *testing.T) {
	p, _ := newProvider(t)
	p.Barrier(testkit.OpPost, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Client("alice").Post(ctx, item12, claimComment("alice@laptop")); !errors.Is(err, context.Canceled) {
		t.Errorf("Post() with a lone caller and a cancelled context: %v", err)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("id %q is not a number", s)
	}
	return n
}
