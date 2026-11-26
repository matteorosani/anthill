package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// The shepherd tests run inside a synctest bubble, because taking the
// review claim waits for the re-read delay on the fake clock.

var api = domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "api"}

// fakeRounds is a RoundRunner that records each round, posts its
// summary comment with the round's marker fields as a round does, and
// ends it with the result of result, or as a clean round.
type fakeRounds struct {
	host   ports.CodeHostPort
	mu     sync.Mutex
	starts []app.RoundStart
	result func(app.RoundStart) (app.RoundResult, error)
	// hidden makes a round end without posting its summary, as if no
	// read of the PR showed it yet.
	hidden bool
}

func (f *fakeRounds) Run(ctx context.Context, r app.RoundStart) (app.RoundResult, error) {
	f.mu.Lock()
	f.starts = append(f.starts, r)
	result, hidden := f.result, f.hidden
	f.mu.Unlock()
	res, err := app.RoundResult{}, error(nil)
	if result != nil {
		res, err = result(r)
	}
	if err != nil || hidden {
		return res, err
	}
	_, perr := f.host.AddComment(ctx, r.Round.PR, domain.AnthillComment{
		Role: domain.RoleShepherd,
		Body: "No issues found.",
		Marker: domain.Marker{
			Version: domain.MarkerVersion, Instance: r.Instance, Kind: domain.CommentSummary, Ref: r.Round.PR.String(),
			RoundFields: &domain.RoundFields{Head: r.Round.Head, Requested: r.Round.Requested},
		},
	})
	return res, perr
}

func (f *fakeRounds) Starts() []app.RoundStart {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]app.RoundStart(nil), f.starts...)
}

// fakeAcks acknowledges commands as the command service does: a reply
// whose marker ID is cmd-<comment>, posted once per PR.
type fakeAcks struct {
	host     ports.CodeHostPort
	instance string
	mu       sync.Mutex
	acked    []string
}

func (f *fakeAcks) Acknowledge(ctx context.Context, on domain.Ref, commentID, text string) error {
	f.mu.Lock()
	f.acked = append(f.acked, commentID)
	f.mu.Unlock()
	_, err := f.host.AddComment(ctx, on, domain.AnthillComment{
		Role: domain.RoleShepherd, Body: text,
		Marker: domain.Marker{Version: domain.MarkerVersion, ID: "cmd-" + commentID, Instance: f.instance, Kind: domain.CommentReply, Ref: on.String()},
	})
	return err
}

func (f *fakeAcks) Acked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acked...)
}

// shepherdEnv is one fake GitHub shared by several instances.
type shepherdEnv struct {
	t     *testing.T
	ctx   context.Context
	clock *testkit.Clock
	gh    *testkit.Provider
}

func newShepherdEnv(t *testing.T) *shepherdEnv {
	t.Helper()
	clock := testkit.NewClock(t0)
	gh := testkit.NewProvider(clock)
	gh.AddWriter(shop, "mrosani", "lbianchi", "ada")
	gh.AddWriter(api, "mrosani", "lbianchi", "ada")
	return &shepherdEnv{t: t, ctx: context.Background(), clock: clock, gh: gh}
}

// putPR puts PR n of repo at head c1, from the given source branch.
func (e *shepherdEnv) putPR(repo domain.Repository, n int, branch string, edit func(*domain.PullRequest)) domain.Ref {
	ref := domain.Ref{Repo: repo, Kind: domain.KindPR, Number: n}
	pr := domain.PullRequest{Ref: ref, Title: "Fix login", SourceBranch: branch, TargetBranch: "main", Author: "ada", HeadCommit: "c1"}
	if edit != nil {
		edit(&pr)
	}
	e.gh.PutPullRequest(pr)
	return ref
}

// push moves the head of a PR.
func (e *shepherdEnv) push(ref domain.Ref, head string) {
	e.gh.EditPullRequest(ref, func(pr *domain.PullRequest) { pr.HeadCommit = head })
}

// shepherdInstance is one instance: its own store, claims, runners,
// settings and shepherd.
type shepherdInstance struct {
	env      *shepherdEnv
	name     string
	store    *testkit.StateStore
	rounds   *fakeRounds
	acks     *fakeAcks
	shepherd *app.Shepherd

	mu   sync.Mutex
	errs []error
	// settings are the effective settings by repository.
	settings map[domain.Repository]domain.Effective
}

// instance makes an instance acting as login with the default settings,
// changed by edit, and the given runners installed.
func (e *shepherdEnv) instance(name, login string, edit func(*domain.Effective), runners ...domain.Runner) *shepherdInstance {
	settings := domain.Defaults()
	if edit != nil {
		edit(&settings)
	}
	client := e.gh.Client(login)
	in := &shepherdInstance{
		env: e, name: name, store: testkit.NewStateStore(),
		rounds: &fakeRounds{host: client}, acks: &fakeAcks{host: client, instance: name},
		settings: map[domain.Repository]domain.Effective{shop: settings, api: settings},
	}
	if len(runners) == 0 {
		runners = []domain.Runner{domain.RunnerClaudeCode}
	}
	installed := map[domain.Runner]ports.AgentRunner{}
	for _, r := range runners {
		installed[r] = testkit.NewAgentRunner(ports.Capabilities{})
	}
	claimRoute := func(context.Context, domain.Ref) (app.ClaimTarget, error) {
		return app.ClaimTarget{Provider: e.gh.Client(login), Instance: name, Label: domain.DefaultClaimLabel}, nil
	}
	claiming := app.NewClaiming(claimRoute, in.store, e.clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
	in.shepherd = app.NewShepherd(app.ShepherdDeps{
		Store:  in.store,
		Claims: claiming,
		Route: func(_ context.Context, ref domain.Ref) (app.ToolWorkspace, error) {
			return app.ToolWorkspace{Repo: ref.Repo, Settings: in.settings[ref.Repo]}, nil
		},
		CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) {
			return client, nil
		},
		WriteAccess: client.CanWrite,
		Instance:    func(domain.Repository) string { return name },
		Runners:     app.NewRunners(installed),
		Rounds:      in.rounds,
		RoundEnds:   app.NewUsageRecorder(in.store),
		Acks:        in.acks,
		Clock:       e.clock,
		Errors: func(err error) {
			in.mu.Lock()
			in.errs = append(in.errs, err)
			in.mu.Unlock()
		},
	})
	e.t.Cleanup(func() {
		in.shepherd.Wait()
		if errs := in.takeErrors(); len(errs) > 0 {
			e.t.Errorf("%s: background errors %v", name, errs)
		}
	})
	return in
}

// takeErrors returns and clears the errors of background work.
func (in *shepherdInstance) takeErrors() []error {
	in.mu.Lock()
	defer in.mu.Unlock()
	errs := in.errs
	in.errs = nil
	return errs
}

// settle lets every goroutine run, moving the clock past the claim
// re-read delay whenever one waits for it.
func (e *shepherdEnv) settle() {
	for {
		synctest.Wait()
		if e.clock.Waiters() == 0 {
			return
		}
		e.clock.Advance(domain.DefaultClaimRereadDelay)
	}
}

// run calls f in a goroutine, settling the bubble until it returns.
func (e *shepherdEnv) run(f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	e.settle()
	return <-done
}

func (in *shepherdInstance) trigger(pr domain.Ref) {
	in.env.t.Helper()
	if err := in.env.run(func() error { return in.shepherd.Trigger(in.env.ctx, pr) }); err != nil {
		in.env.t.Fatalf("%s: Trigger(%s) = %v", in.name, pr, err)
	}
}

func (in *shepherdInstance) request(pr domain.Ref, comment string) error {
	return in.env.run(func() error { return in.shepherd.RequestRound(in.env.ctx, pr, comment) })
}

// reviewed lists the heads of the instance's rounds on pr, with an R
// suffix for requested rounds.
func (in *shepherdInstance) reviewed(pr domain.Ref) []string {
	in.env.t.Helper()
	rounds, err := in.store.Rounds(in.env.ctx, pr)
	if err != nil {
		in.env.t.Fatal(err)
	}
	var heads []string
	for _, r := range rounds {
		if r.EndedAt.IsZero() {
			in.env.t.Errorf("round %d on %s not ended", r.ID, pr)
		}
		h := r.Head
		if r.Requested {
			h += "R"
		}
		heads = append(heads, h)
	}
	return heads
}

// noClaimsHeld checks that the instance holds no review claim between
// triggers.
func (in *shepherdInstance) noClaimsHeld() {
	in.env.t.Helper()
	held, err := in.store.HeldClaims(in.env.ctx)
	if err != nil {
		in.env.t.Fatal(err)
	}
	if len(held) != 0 {
		in.env.t.Errorf("%s holds %v after the trigger", in.name, held)
	}
}

func equalHeads(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: rounds on %v, want %v", what, got, want)
	}
}

// limitComments counts the round limit comments on a PR.
func (e *shepherdEnv) limitComments(ref domain.Ref) int {
	pr, _ := e.gh.PullRequest(ref)
	n := 0
	for _, c := range pr.Comments {
		if m, ok := domain.FindMarker(c.Body); ok && m.ID == app.RoundLimitMarkerID {
			n++
			if !strings.Contains(c.Body, "/anthill review") {
				e.t.Errorf("round limit comment %q does not say how to request a round", c.Body)
			}
		}
	}
	return n
}

// spec pr-shepherd, "Round on a new PR", "No new commits" and
// "Developer pushes fixes".
func TestShepherdRoundTriggers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(pr)
		equalHeads(t, "new PR", a.reviewed(pr), "c1")
		a.noClaimsHeld()
		starts := a.rounds.Starts()
		if s := starts[0]; s.Runner != domain.RunnerClaudeCode || s.Instance != instanceA || s.PR.HeadCommit != "c1" || s.Round.ID == 0 {
			t.Errorf("round start = %+v", s)
		}

		a.trigger(pr)
		equalHeads(t, "no new commits", a.reviewed(pr), "c1")

		e.push(pr, "c2")
		a.trigger(pr)
		equalHeads(t, "after a push", a.reviewed(pr), "c1", "c2")
		if got := a.rounds.Starts()[1].Earlier; len(got) != 1 || got[0].Head != "c1" {
			t.Errorf("round 2 earlier rounds = %+v, want round 1", got)
		}
	})
}

// spec pr-shepherd, "Limit reached" and "Extra round after limit".
func TestShepherdRoundLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)
		for _, head := range []string{"c1", "c2", "c3"} {
			e.push(pr, head)
			a.trigger(pr)
		}
		if n := e.limitComments(pr); n != 0 {
			t.Fatalf("%d limit comments after round 3 of 3, want none before a push", n)
		}

		e.push(pr, "c4")
		a.trigger(pr)
		equalHeads(t, "limit reached", a.reviewed(pr), "c1", "c2", "c3")
		if n := e.limitComments(pr); n != 1 {
			t.Fatalf("%d limit comments after a push past the limit, want 1", n)
		}
		a.noClaimsHeld()

		e.push(pr, "c5")
		a.trigger(pr)
		if n := e.limitComments(pr); n != 1 {
			t.Errorf("%d limit comments after a second push, want still 1", n)
		}

		if err := a.request(pr, "cmd1"); err != nil {
			t.Fatal(err)
		}
		equalHeads(t, "requested after the limit", a.reviewed(pr), "c1", "c2", "c3", "c5R")
		// The request is served: a repeated call for the same command,
		// or another trigger, starts nothing.
		if err := a.request(pr, "cmd1"); err != nil {
			t.Fatal(err)
		}
		a.trigger(pr)
		equalHeads(t, "after the request is served", a.reviewed(pr), "c1", "c2", "c3", "c5R")
		a.noClaimsHeld()
	})
}

// spec pr-shepherd, "Human PR in ant-authored scope", "Draft PR" and
// the labelled and all scopes.
func TestShepherdScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		human := e.putPR(shop, 35, "feature/login", nil)
		draft := e.putPR(shop, 36, "ant/gh-13-fix-logout", func(pr *domain.PullRequest) { pr.Draft = true })

		a.trigger(human)
		equalHeads(t, "human PR in ant-authored scope", a.reviewed(human))
		if err := a.request(human, "cmd1"); err != nil {
			t.Fatal(err)
		}
		equalHeads(t, "human PR after /anthill review", a.reviewed(human), "c1R")
		e.push(human, "c2")
		a.trigger(human)
		equalHeads(t, "human PR pushed after a requested round", a.reviewed(human), "c1R")

		a.trigger(draft)
		equalHeads(t, "draft PR", a.reviewed(draft))
		e.gh.EditPullRequest(draft, func(pr *domain.PullRequest) { pr.Draft = false })
		a.trigger(draft)
		equalHeads(t, "PR marked ready", a.reviewed(draft), "c1")

		labelled := e.instance(instanceA, "mrosani", func(s *domain.Effective) { s.ShepherdScope = domain.ScopeLabelled })
		tagged := e.putPR(shop, 37, "feature/signup", func(pr *domain.PullRequest) { pr.Labels = []string{domain.LabelReview} })
		untagged := e.putPR(shop, 38, "feature/profile", nil)
		antPR := e.putPR(shop, 39, "ant/gh-14-fix-signup", nil)
		labelled.trigger(tagged)
		labelled.trigger(untagged)
		labelled.trigger(antPR)
		equalHeads(t, "labelled scope, human PR with the label", labelled.reviewed(tagged), "c1")
		equalHeads(t, "labelled scope, human PR without the label", labelled.reviewed(untagged))
		equalHeads(t, "labelled scope, ant PR without the label", labelled.reviewed(antPR), "c1")

		all := e.instance(instanceA, "mrosani", func(s *domain.Effective) { s.ShepherdScope = domain.ScopeAll })
		all.trigger(untagged)
		equalHeads(t, "all scope", all.reviewed(untagged), "c1")
	})
}

// spec pr-shepherd, "Machine opted out".
func TestShepherdMachineOptedOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", func(s *domain.Effective) { s.Reviewing = false })
		b := e.instance(instanceB, "lbianchi", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(pr)
		equalHeads(t, "opted-out instance", a.reviewed(pr))
		if reqs := e.gh.Requests(); len(reqs) == 0 {
			t.Fatal("no provider request recorded")
		}
		for _, r := range e.gh.Requests() {
			if r.Login == "mrosani" && r.Op != testkit.OpPullRequest {
				t.Errorf("opted-out instance made %s on %s", r.Op, r.Ref)
			}
		}
		var notReviewing *app.NotReviewingError
		if err := a.shepherd.CanReview(e.ctx, pr); !errors.As(err, &notReviewing) {
			t.Errorf("CanReview = %v, want *NotReviewingError", err)
		}
		if err := a.request(pr, "cmd1"); !errors.As(err, &notReviewing) {
			t.Errorf("RequestRound = %v, want *NotReviewingError", err)
		}
		if reqs, _ := a.store.RoundRequests(e.ctx, pr); len(reqs) != 0 {
			t.Errorf("opted-out instance recorded %v", reqs)
		}

		b.trigger(pr)
		equalHeads(t, "participating instance", b.reviewed(pr), "c1")
	})
}

// spec pr-shepherd, "Opted out for one repository".
func TestShepherdOptedOutForOneRepository(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		off := a.settings[api]
		off.Reviewing = false
		a.settings[api] = off
		inAPI := e.putPR(api, 7, "ant/gh-3-fix-auth", nil)
		inShop := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(inAPI)
		a.trigger(inShop)
		equalHeads(t, "repository opted out", a.reviewed(inAPI))
		equalHeads(t, "other repository", a.reviewed(inShop), "c1")
	})
}

// spec pr-shepherd, "Review runner missing on this machine" and
// "Different runner for reviews".
func TestShepherdReviewRunner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		codex := func(s *domain.Effective) { s.ReviewRunner = domain.RunnerCodex }
		a := e.instance(instanceA, "mrosani", codex, domain.RunnerClaudeCode)
		b := e.instance(instanceB, "lbianchi", codex, domain.RunnerClaudeCode, domain.RunnerCodex)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(pr)
		equalHeads(t, "instance without the review runner", a.reviewed(pr))
		var notReviewing *app.NotReviewingError
		if err := a.shepherd.CanReview(e.ctx, pr); !errors.As(err, &notReviewing) || notReviewing.Runner != domain.RunnerCodex {
			t.Errorf("CanReview = %v, want the missing review runner", err)
		}

		b.trigger(pr)
		equalHeads(t, "instance with the review runner", b.reviewed(pr), "c1")
		if got := b.rounds.Starts()[0].Runner; got != domain.RunnerCodex {
			t.Errorf("round runner = %s, want codex", got)
		}
	})
}

// A round runs in the background: Trigger returns while it runs, a
// trigger during the round does nothing, and when the round ends the
// shepherd asks the playbook again, so a push made during the round
// gets the next round. Another instance leaves the PR alone meanwhile.
func TestShepherdRoundInBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		b := e.instance(instanceB, "lbianchi", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		release := make(chan struct{})
		var once sync.Once
		b.rounds.result = func(app.RoundStart) (app.RoundResult, error) {
			once.Do(func() { <-release })
			return app.RoundResult{}, nil
		}
		b.trigger(pr) // returns with the round in progress
		if got := len(b.rounds.Starts()); got != 1 {
			t.Fatalf("%d rounds started, want 1 in progress", got)
		}

		a.trigger(pr)
		equalHeads(t, "instance without the claim", a.reviewed(pr))
		a.noClaimsHeld()

		e.push(pr, "c2")
		b.trigger(pr)
		if got := len(b.rounds.Starts()); got != 1 {
			t.Fatalf("%d rounds started during a round, want 1", got)
		}

		close(release)
		e.settle()
		equalHeads(t, "after the round", b.reviewed(pr), "c1", "c2")
		b.noClaimsHeld()
	})
}

// A round that fails is ended with the usage it reported, its error goes
// to Errors, and the claim is released.
func TestShepherdRoundFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)
		tokens := int64(1200)
		a.rounds.result = func(app.RoundStart) (app.RoundResult, error) {
			return app.RoundResult{Usage: ports.Usage{Tokens: &tokens}}, errors.New("agent crashed")
		}

		a.trigger(pr)
		errs := a.takeErrors()
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "agent crashed") {
			t.Fatalf("errors = %v, want the round's error", errs)
		}
		rounds, _ := a.store.Rounds(e.ctx, pr)
		if len(rounds) != 1 || rounds[0].EndedAt.IsZero() || rounds[0].Usage.Tokens == nil || *rounds[0].Usage.Tokens != tokens {
			t.Errorf("rounds = %+v, want one ended round with its tokens", rounds)
		}
		a.noClaimsHeld()
	})
}

// Rounds run by another instance count: their summary comments carry
// the head commit, so the round limit and reviewed heads hold across
// instances, and a forged summary by a user without write access does
// not count.
func TestShepherdRoundsAcrossInstances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		b := e.instance(instanceB, "lbianchi", nil)
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		a.trigger(pr)
		b.trigger(pr)
		equalHeads(t, "round on c1 by A, then B", b.reviewed(pr))

		e.push(pr, "c2")
		b.trigger(pr)
		e.push(pr, "c3")
		a.trigger(pr)
		e.push(pr, "c4")
		b.trigger(pr)
		equalHeads(t, "A's rounds", a.reviewed(pr), "c1", "c3")
		equalHeads(t, "B's rounds", b.reviewed(pr), "c2")
		if n := e.limitComments(pr); n != 1 {
			t.Errorf("%d limit comments after 3 rounds by two instances and a push, want 1", n)
		}

		forged := domain.AnthillComment{Role: domain.RoleShepherd, Body: "No issues found.", Marker: domain.Marker{
			Version: 1, Instance: "mallory@PC", Kind: domain.CommentSummary, Ref: pr.String(),
			RoundFields: &domain.RoundFields{Head: "c9"},
		}}
		other := e.putPR(shop, 40, "ant/gh-15-fix-search", func(p *domain.PullRequest) { p.HeadCommit = "c9" })
		e.gh.Comment(other, "mallory", forged.String())
		a.trigger(other)
		equalHeads(t, "PR with a forged summary", a.reviewed(other), "c9")
	})
}

// spec agent-conversation, "Two instances see the same command": the
// instance that wins the review claim acknowledges /anthill review and
// runs the round; the other drops its request once it sees the
// acknowledgement.
func TestShepherdRequestedRoundRunsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		b := e.instance(instanceB, "lbianchi", nil)
		pr := e.putPR(shop, 35, "feature/login", nil)

		if err := a.request(pr, "cmd1"); err != nil {
			t.Fatal(err)
		}
		if err := b.request(pr, "cmd1"); err != nil {
			t.Fatal(err)
		}
		equalHeads(t, "first instance", a.reviewed(pr), "c1R")
		equalHeads(t, "second instance", b.reviewed(pr))
		if got := a.acks.Acked(); len(got) != 1 || got[0] != "cmd1" {
			t.Errorf("acknowledged %v, want cmd1", got)
		}
		if reqs, _ := b.store.RoundRequests(e.ctx, pr); len(reqs) != 0 {
			t.Errorf("second instance still has %v pending", reqs)
		}
		e.push(pr, "c2")
		b.trigger(pr)
		equalHeads(t, "second instance after a push", b.reviewed(pr))
	})
}

// A request whose acknowledgement this instance posted, but whose round
// was not stored, as after a crash, still runs.
func TestShepherdOwnAcknowledgementKeepsTheRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		a := e.instance(instanceA, "mrosani", nil)
		pr := e.putPR(shop, 35, "feature/login", nil)
		if err := a.acks.Acknowledge(e.ctx, pr, "cmd1", "Starting a requested review round on c1."); err != nil {
			t.Fatal(err)
		}
		if err := a.request(pr, "cmd1"); err != nil {
			t.Fatal(err)
		}
		equalHeads(t, "request acknowledged before a crash", a.reviewed(pr), "c1R")
	})
}
