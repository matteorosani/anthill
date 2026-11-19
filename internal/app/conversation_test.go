package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// convEnv is the supervisor's environment with the conversation service
// as its source of answers, as in the daemon.
type convEnv struct {
	*supEnv
	conv *app.Conversation
}

func newConvEnv(t *testing.T, capacity int) *convEnv {
	t.Helper()
	var conv *app.Conversation
	ants := &lazyAnts{}
	e := newSupEnv(t, capacity, func(d *app.AntSupervisorDeps) {
		conv = app.NewConversation(app.ConversationDeps{
			Store: d.Store.(*testkit.StateStore), Tracker: d.Tracker, CodeHost: d.CodeHost,
			Ants: ants, Clock: d.Clock, Notifier: d.Notifier,
		})
		d.Answers = conv.Answers()
		d.Due = nil // the supervisor's own retry and continue
	})
	ants.sup = e.sup
	return &convEnv{supEnv: e, conv: conv}
}

// lazyAnts lets the conversation reach the supervisor, which is made
// after it.
type lazyAnts struct{ sup *app.AntSupervisor }

func (l *lazyAnts) Trigger(ctx context.Context, id ports.AntID) error { return l.sup.Trigger(ctx, id) }
func (l *lazyAnts) Stop(ctx context.Context, id ports.AntID, reason string) error {
	return l.sup.Stop(ctx, id, reason)
}
func (l *lazyAnts) Retry(ctx context.Context, id ports.AntID) error { return l.sup.Retry(ctx, id) }

// do runs f in the bubble while settling, as the supervisor's actions
// wait on the fake clock.
func (e *convEnv) do(f func() error) {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	e.settle()
	if err := <-done; err != nil {
		e.t.Fatal(err)
	}
	e.settle()
}

// startAnt accepts gh-n and reports its session id.
func (e *convEnv) startAnt(n int) (ports.AntID, *testkit.FakeExecution) {
	e.t.Helper()
	id := e.mustAccept(n)
	x := e.execOf(ghRef(n))
	x.Emit(ports.ExecEvent{Kind: ports.ExecSessionID, SessionID: "sess-" + ghRef(n).String()})
	e.settle()
	return id, x
}

// ask makes the ant's active run call ask_question, as its agent would.
func (e *convEnv) ask(x *testkit.FakeExecution, text string) domain.Comment {
	e.t.Helper()
	var res app.ToolResult
	e.do(func() error {
		res = e.tools.Call(e.ctx, x.Spec.Run.ToolToken, domain.ToolAskQuestion, []byte(`{"text":"`+text+`"}`))
		return nil
	})
	if res.IsError {
		e.t.Fatalf("ask_question refused: %s", res.Text)
	}
	if err := e.exec.Stop(x.ID, 10*time.Second); err != nil {
		e.t.Fatal(err)
	}
	e.settle()
	// The claim of an ant between runs is parked (spec work-claiming,
	// "Long-lived task claims"), so it holds however long the answer takes.
	e.do(func() error { return e.claiming.Park(e.ctx, ghRef(12)) })
	item, _ := e.gh.Item(ghRef(12))
	return item.Comments[len(item.Comments)-1]
}

// comment posts a human comment and returns its change event, as
// ingestion finds it.
func (e *convEnv) comment(ref domain.Ref, author, body string) domain.ChangeEvent {
	e.clock.Advance(time.Minute)
	c := e.gh.Comment(ref, author, body)
	return domain.ChangeEvent{
		Ref: ref, Kind: domain.ChangeCommentAdded, Actor: author, At: c.CreatedAt,
		Origin: c.Origin(), CommentID: c.ID, Body: c.Body,
	}
}

// input is what the change reactor's intake hands on for ev, as this
// instance sees it: the item, its current ant unless final, and whether
// this instance holds the item's task claim.
func (e *convEnv) input(store *testkit.StateStore, ev domain.ChangeEvent) app.ChangeInput {
	e.t.Helper()
	in := app.ChangeInput{Outbox: 1, Event: ev, Item: ev.Ref}
	if ev.Ref.Kind == domain.KindPR {
		if pr, ok := e.gh.PullRequest(ev.Ref); ok && pr.Item != nil {
			in.Item = *pr.Item
		}
	}
	if rec, err := store.CurrentAnt(e.ctx, in.Item); err == nil && !rec.State().Final() {
		in.Ant = &rec
	}
	held, err := store.HeldClaims(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	in.Responsible = slices.ContainsFunc(held, func(h ports.HeldClaim) bool { return h.Ref == in.Item && h.Kind == domain.ClaimTask })
	return in
}

func (e *convEnv) answer(ev domain.ChangeEvent) bool {
	e.t.Helper()
	var handled bool
	e.do(func() (err error) {
		handled, err = e.conv.HandleAnswer(e.ctx, e.input(e.store, ev))
		return err
	})
	return handled
}

func (e *convEnv) question(ant ports.AntID) ports.QuestionRecord {
	e.t.Helper()
	qs, err := e.store.AntQuestions(e.ctx, ant)
	if err != nil || len(qs) == 0 {
		e.t.Fatalf("questions of ant %d: %v, %v", ant, qs, err)
	}
	return qs[len(qs)-1]
}

// Spec agent-conversation, "Ambiguous requirement", "Unrelated comment"
// and "Developer replies in thread" (here with a mention, on a provider
// without threads); spec ant-execution, "Resume after an answer".
func TestAnswerResumesTheSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		id, x := e.startAnt(12)
		asked := e.ask(x, "Does timeout mean 30 or 60 seconds?")
		rec := e.wantState(id, domain.AntAwaitingInput)
		if rec.Budget.Counting() {
			t.Error("the budget counts while the ant awaits input")
		}
		if q := e.question(id); q.CommentID != asked.ID || q.Text != "Does timeout mean 30 or 60 seconds?" || !q.AnsweredAt.IsZero() {
			t.Fatalf("question = %+v", q)
		}

		// Comments that do not answer leave the question pending.
		for _, ev := range []domain.ChangeEvent{
			e.comment(ghRef(12), "lbianchi", "CI is flaky today."),
			e.comment(ghRef(12), "ada", "@anthill 30 seconds"), // no write access
			e.comment(ghRef(13), "lbianchi", "@anthill 60 seconds"),
		} {
			if e.answer(ev) {
				t.Errorf("%q on %s handled as the answer", ev.Body, ev.Ref)
			}
		}
		e.wantState(id, domain.AntAwaitingInput)
		if n := len(e.exec.Executions()); n != 1 {
			t.Fatalf("%d executions, want 1", n)
		}

		ev := e.comment(ghRef(12), "lbianchi", "@anthill 60 seconds")
		if !e.answer(ev) {
			t.Fatal("the answer was not handled")
		}
		q := e.question(id)
		if q.AnswerID != ev.CommentID || q.AnsweredAt.IsZero() || q.Answer == nil || q.Answer.Body != "@anthill 60 seconds" {
			t.Errorf("answered question = %+v", q)
		}
		e.wantState(id, domain.AntRunning)
		resumed := e.execOf(ghRef(12))
		if resumed == x || resumed.Spec.Session != "sess-gh-12" {
			t.Fatalf("run = %+v, want a new run resuming sess-gh-12", resumed.Spec)
		}
		if b := e.briefs.last(); b.kind != "resume" || b.answer == nil || b.answer.CommentID != ev.CommentID {
			t.Errorf("brief = %+v, want the resume input with the answer", b)
		}

		// The same change handed out again by the outbox is still the
		// answer, and starts no second run.
		if !e.answer(ev) {
			t.Error("the redelivered answer was not handled")
		}
		if n := len(e.exec.Executions()); n != 2 {
			t.Errorf("%d executions, want 2", n)
		}
	})
}

// A quote of the question answers it, and on a threaded provider a reply
// in the question's thread does.
func TestAnswerByQuoteOrThreadReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		id, x := e.startAnt(12)
		e.ask(x, "Keep the old endpoint? It is still used by the mobile app.")
		ev := e.comment(ghRef(12), "mrosani", "> It is still used by the mobile app.\n\nNo, it was retired last week.")
		if !e.answer(ev) {
			t.Fatal("the quoting reply was not handled as the answer")
		}
		e.wantState(id, domain.AntRunning)

		// The next question opened a thread.
		x = e.execOf(ghRef(12))
		asked := e.ask(x, "Which status code for a retired endpoint?")
		q := e.question(id)
		if q.CommentID != asked.ID {
			t.Fatalf("question = %+v", q)
		}
		q.Thread = "7"
		if err := e.store.SaveQuestion(e.ctx, q); err != nil {
			t.Fatal(err)
		}
		other := e.comment(ghRef(12), "mrosani", "410")
		other.ThreadID = "8"
		if e.answer(other) {
			t.Error("a reply in another thread was the answer")
		}
		reply := e.comment(ghRef(12), "mrosani", "410 Gone")
		reply.ThreadID = "7"
		if !e.answer(reply) {
			t.Fatal("the reply in the thread was not the answer")
		}
		e.wantState(id, domain.AntRunning)
		if b := e.briefs.last(); b.answer == nil || b.answer.Body != "410 Gone" {
			t.Errorf("brief = %+v", b)
		}
	})
}

// Spec agent-conversation, "No answer for three days".
func TestStaleQuestion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		id, x := e.startAnt(12)
		e.ask(x, "Which timeout?")
		before := len(e.notifier.Sent())

		e.clock.Advance(domain.DefaultQuestionStaleness - time.Minute)
		if err := e.conv.CheckStale(e.ctx); err != nil {
			t.Fatal(err)
		}
		if q := e.question(id); !q.StaleAt.IsZero() {
			t.Fatalf("question stale after less than 3 days: %+v", q)
		}

		e.clock.Advance(time.Minute)
		for range 2 {
			if err := e.conv.CheckStale(e.ctx); err != nil {
				t.Fatal(err)
			}
		}
		if q := e.question(id); !q.StaleAt.Equal(e.clock.Now()) || !q.AnsweredAt.IsZero() {
			t.Errorf("question = %+v, want stale and still pending", q)
		}
		sent := e.notifier.Sent()[before:]
		if len(sent) != 1 || sent[0].Kind != ports.NotifyStaleQuestion || *sent[0].Ref != ghRef(12) {
			t.Errorf("notifications = %+v, want one stale question", sent)
		}
		// No run starts on its own.
		e.settle()
		e.wantState(id, domain.AntAwaitingInput)
		if n := len(e.exec.Executions()); n != 1 {
			t.Errorf("%d executions, want 1", n)
		}

		// The answer still resumes the session.
		if !e.answer(e.comment(ghRef(12), "lbianchi", "@anthill 60 seconds")) {
			t.Fatal("the late answer was not handled")
		}
		e.wantState(id, domain.AntRunning)
	})
}

// commands runs Commands in the bubble, for the instance whose store is
// given.
func (e *convEnv) commands(c *app.Conversation, store *testkit.StateStore, ev domain.ChangeEvent) app.CommandResult {
	e.t.Helper()
	var res app.CommandResult
	in := e.input(store, ev)
	e.do(func() (err error) {
		res, err = c.HandleCommands(e.ctx, in)
		return err
	})
	return res
}

// withCommands gives the conversation the commands' dependencies on the
// env's claiming.
func (e *convEnv) withCommands() {
	e.conv = app.NewConversation(app.ConversationDeps{
		Store: e.store, Tracker: e.trackerRoute("mrosani"), CodeHost: e.codeHostRoute("mrosani"),
		Claims: e.claiming, Ants: e.sup, Clock: e.clock, Notifier: e.notifier,
	})
}

func (e *convEnv) trackerRoute(login string) app.TrackerRoute {
	return func(context.Context, domain.Ref) (ports.TrackerPort, error) { return e.gh.Client(login), nil }
}

func (e *convEnv) codeHostRoute(login string) app.CodeHostRoute {
	return func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return e.gh.Client(login), nil }
}

// acks returns the acknowledgements of the command comment id on ref.
func (e *convEnv) acks(ref domain.Ref, id string) []domain.AnthillComment {
	var comments []domain.Comment
	if ref.Kind == domain.KindPR {
		pr, _ := e.gh.PullRequest(ref)
		comments = pr.Comments
		for _, th := range pr.Threads {
			comments = append(comments, th.Comments...)
		}
	} else {
		item, _ := e.gh.Item(ref)
		comments = item.Comments
	}
	var out []domain.AnthillComment
	for _, c := range comments {
		if ac, ok := domain.ParseAnthillComment(c.Body); ok && ac.Marker.ID == domain.CommandAckID(id) {
			out = append(out, ac)
		}
	}
	return out
}

// Spec agent-conversation, "Stop command", and retry after it.
func TestStopAndRetryCommands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		e.withCommands()
		id, _ := e.startAnt(12)

		stop := e.comment(ghRef(12), "lbianchi", "Wrong approach.\n/anthill stop")
		if res := e.commands(e.conv, e.store, stop); !res.Handled || res.Route {
			t.Fatalf("result = %+v", res)
		}
		rec := e.wantState(id, domain.AntStopped)
		if r := lastReason(rec); r != "/anthill stop by lbianchi" {
			t.Errorf("reason = %q", r)
		}
		held, _ := e.store.HeldClaims(e.ctx)
		if len(held) != 1 || !held[0].Parked {
			t.Errorf("held claims = %+v, want the task claim parked", held)
		}
		acks := e.acks(ghRef(12), stop.CommentID)
		if len(acks) != 1 || acks[0].Marker.Kind != domain.CommentReply || acks[0].Marker.Instance != instanceA ||
			!strings.Contains(acks[0].Body, domain.ReplyStopped) || !strings.Contains(acks[0].Body, "/anthill retry") {
			t.Fatalf("acknowledgements = %+v", acks)
		}

		// Handled again, as after a restart: nothing more happens.
		e.commands(e.conv, e.store, stop)
		if n := len(e.acks(ghRef(12), stop.CommentID)); n != 1 {
			t.Errorf("%d acknowledgements after a redelivery, want 1", n)
		}

		retry := e.comment(ghRef(12), "lbianchi", "/anthill retry")
		e.commands(e.conv, e.store, retry)
		e.wantState(id, domain.AntRunning)
		if acks := e.acks(ghRef(12), retry.CommentID); len(acks) != 1 || !strings.Contains(acks[0].Body, domain.ReplyRetried) {
			t.Errorf("acknowledgements = %+v", acks)
		}

		// A comment without a command is not one.
		if res := e.commands(e.conv, e.store, e.comment(ghRef(12), "lbianchi", "Thanks!")); res.Handled {
			t.Errorf("plain comment handled: %+v", res)
		}
	})
}

// Spec agent-conversation, "Unknown command", and a command from a user
// without write access.
func TestUnknownAndUnauthorizedCommands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		e.withCommands()
		id, _ := e.startAnt(12)

		deploy := e.comment(ghRef(12), "lbianchi", "/anthill deploy")
		e.commands(e.conv, e.store, deploy)
		acks := e.acks(ghRef(12), deploy.CommentID)
		if len(acks) != 1 || !strings.Contains(acks[0].Body, "Unknown command `/anthill deploy`") || !strings.Contains(acks[0].Body, "/anthill review") {
			t.Errorf("acknowledgements = %+v, want the help text", acks)
		}

		outsider := e.comment(ghRef(12), "ada", "/anthill stop")
		e.commands(e.conv, e.store, outsider)
		e.wantState(id, domain.AntRunning)
		acks = e.acks(ghRef(12), outsider.CommentID)
		if len(acks) != 1 || !strings.Contains(acks[0].Body, "requires write access") {
			t.Errorf("acknowledgements = %+v, want missing permission", acks)
		}
	})
}

// Spec agent-conversation, "Fix on an ant PR": the change goes to the
// owning ant's routing.
func TestFixOnAnAntPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		e.withCommands()
		id, _ := e.startAnt(12)
		pr := ghPR(34)
		item := ghRef(12)
		rec := e.ant(id)
		e.gh.PutPullRequest(domain.PullRequest{
			Ref: pr, SourceBranch: rec.Branch, TargetBranch: "main", Author: "mrosani", Item: &item,
			Threads: []domain.ReviewThread{{ID: "thread-7", Path: "src/auth.go", Line: 42}},
		})
		rec.PR = &pr
		if err := e.store.SaveAnt(e.ctx, rec); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		var c domain.Comment
		e.gh.EditPullRequest(pr, func(p *domain.PullRequest) {
			c = domain.Comment{ID: "9001", Author: "lbianchi", Body: "/anthill fix", CreatedAt: e.clock.Now(), UpdatedAt: e.clock.Now()}
			p.Threads[0].Comments = append(p.Threads[0].Comments, c)
		})
		ev := domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: "lbianchi", At: c.CreatedAt, Origin: domain.OriginHuman, CommentID: c.ID, ThreadID: "thread-7", Body: c.Body}
		if res := e.commands(e.conv, e.store, ev); !res.Handled || !res.Route {
			t.Fatalf("result = %+v, want the change routed to the ant", res)
		}
		if acks := e.acks(pr, c.ID); len(acks) != 1 || !strings.Contains(acks[0].Body, domain.ReplyFixForwarded) {
			t.Errorf("acknowledgements = %+v", acks)
		}
	})
}

// fakeShepherd starts a requested round at once, as the shepherd does
// once triggered: the round takes the PR's review claim, and the instance
// that wins it runs the round and acknowledges the command.
type fakeShepherd struct {
	claims *app.Claiming
	acks   app.CommandAcks
	name   string
	mu     *sync.Mutex
	rounds *[]string
}

func (s *fakeShepherd) CanReview(context.Context, domain.Ref) error { return nil }

func (s *fakeShepherd) RequestRound(ctx context.Context, pr domain.Ref, commentID string) error {
	_, err := s.claims.Acquire(ctx, pr, domain.ClaimReview)
	var taken *app.ClaimTakenError
	if errors.As(err, &taken) {
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	*s.rounds = append(*s.rounds, s.name+":"+commentID)
	s.mu.Unlock()
	return s.acks.Acknowledge(ctx, pr, commentID, domain.ReplyRoundStarted)
}

// Spec agent-conversation, "Two instances see the same command": two
// instances poll the same PR with an unacknowledged /anthill review. And
// "External contributor": a command from a user without write access
// starts no round, and one reply says so.
func TestTwoInstancesSeeTheSameCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		clock := testkit.NewClock(t0)
		gh := testkit.NewProvider(clock)
		gh.AddWriter(shop, "mrosani", "lbianchi", "gverdi")
		pr := ghPR(40)
		gh.PutPullRequest(domain.PullRequest{Ref: pr, SourceBranch: "feature/login", TargetBranch: "main", Author: "gverdi"})

		var (
			mu     sync.Mutex
			rounds []string
		)
		type instance struct {
			conv  *app.Conversation
			store *testkit.StateStore
		}
		newInstance := func(login, id string) instance {
			route := func(context.Context, domain.Ref) (app.ClaimTarget, error) {
				return app.ClaimTarget{Provider: gh.Client(login), Instance: id, Label: domain.DefaultClaimLabel}, nil
			}
			store := testkit.NewStateStore()
			claiming := app.NewClaiming(route, store, clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
			shepherd := &fakeShepherd{claims: claiming, name: id, mu: &mu, rounds: &rounds}
			conv := app.NewConversation(app.ConversationDeps{
				Store:    store,
				Tracker:  func(context.Context, domain.Ref) (ports.TrackerPort, error) { return gh.Client(login), nil },
				CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) { return gh.Client(login), nil },
				Claims:   claiming, Reviews: shepherd, Clock: clock, Notifier: &testkit.Notifier{},
			})
			shepherd.acks = conv
			return instance{conv, store}
		}
		instances := []instance{newInstance("mrosani", instanceA), newInstance("lbianchi", "lbianchi@DESKTOP-7")}

		// both hands ev to both instances at the same time, as their polls
		// find it, and then again, as a later poll would.
		both := func(ev domain.ChangeEvent) {
			t.Helper()
			in := app.ChangeInput{Outbox: 1, Event: ev, Item: ev.Ref}
			var wg sync.WaitGroup
			errs := make([]error, len(instances))
			for i, x := range instances {
				wg.Go(func() { _, errs[i] = x.conv.HandleCommands(ctx, in) })
			}
			for {
				synctest.Wait()
				if clock.Waiters() == 0 {
					break
				}
				clock.Advance(domain.DefaultClaimRereadDelay)
			}
			wg.Wait()
			for _, x := range instances {
				_, err := x.conv.HandleCommands(ctx, in)
				errs = append(errs, err)
			}
			if err := errors.Join(errs...); err != nil {
				t.Fatal(err)
			}
		}
		acks := func(commentID string) []domain.AnthillComment {
			got, _ := gh.PullRequest(pr)
			var out []domain.AnthillComment
			for _, c := range got.Comments {
				if ac, ok := domain.ParseAnthillComment(c.Body); ok && ac.Marker.ID == domain.CommandAckID(commentID) {
					out = append(out, ac)
				}
			}
			return out
		}
		event := func(c domain.Comment) domain.ChangeEvent {
			return domain.ChangeEvent{Ref: pr, Kind: domain.ChangeCommentAdded, Actor: c.Author, At: c.CreatedAt, Origin: domain.OriginHuman, CommentID: c.ID, Body: c.Body}
		}

		clock.Advance(time.Minute)
		review := gh.Comment(pr, "lbianchi", "/anthill review")
		both(event(review))
		got := acks(review.ID)
		if len(rounds) != 1 || len(got) != 1 || got[0].Role != domain.RoleShepherd || !strings.HasPrefix(rounds[0], got[0].Marker.Instance+":") {
			t.Fatalf("rounds = %v, acknowledgements = %+v: want one of each, by the same instance", rounds, got)
		}

		clock.Advance(time.Hour) // the round ended and its claim expired
		outsider := gh.Comment(pr, "ada", "/anthill review")
		both(event(outsider))
		if len(rounds) != 1 {
			t.Errorf("rounds = %v: the outsider's request started one", rounds)
		}
		if got := acks(outsider.ID); len(got) != 1 || !strings.Contains(got[0].Body, "requires write access") {
			t.Errorf("acknowledgements = %+v, want one reply that permission is missing", got)
		}
		for _, x := range instances {
			if held, _ := x.store.HeldClaims(ctx); len(held) != 0 && held[0].Kind == domain.ClaimReview && !held[0].ExpiresAt().Before(clock.Now()) {
				t.Errorf("the review claim taken to reply is still held: %+v", held)
			}
		}
	})
}

// Only the instance holding the item's task claim acts on its commands.
func TestCommandOnAnItemAnotherInstanceHolds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		e.withCommands()
		id, _ := e.startAnt(12)

		// Instance B, on the same provider, with no claim on gh-12.
		route := func(context.Context, domain.Ref) (app.ClaimTarget, error) {
			return app.ClaimTarget{Provider: e.gh.Client("lbianchi"), Instance: "lbianchi@DESKTOP-7", Label: domain.DefaultClaimLabel}, nil
		}
		store := testkit.NewStateStore()
		b := app.NewConversation(app.ConversationDeps{
			Store: store, Tracker: e.trackerRoute("lbianchi"), CodeHost: e.codeHostRoute("lbianchi"),
			Claims: app.NewClaiming(route, store, e.clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{}),
			Ants:   &lazyAnts{}, Clock: e.clock,
		})
		ev := e.comment(ghRef(12), "lbianchi", "/anthill stop\n/anthill help")
		if res := e.commands(b, store, ev); !res.Handled {
			t.Fatalf("result = %+v", res)
		}
		if acks := e.acks(ghRef(12), ev.CommentID); len(acks) != 0 {
			t.Fatalf("instance B acknowledged: %+v", acks)
		}
		e.wantState(id, domain.AntRunning)

		// The holder acts, once, with one reply for both commands.
		e.commands(e.conv, e.store, ev)
		e.wantState(id, domain.AntStopped)
		acks := e.acks(ghRef(12), ev.CommentID)
		if len(acks) != 1 || !strings.Contains(acks[0].Body, "`/anthill stop`") || !strings.Contains(acks[0].Body, "`/anthill help`: Anthill commands") {
			t.Errorf("acknowledgements = %+v", acks)
		}
	})
}

// /anthill release is acknowledged by the instance whose claim it
// released, even once Claiming has dropped the claim.
func TestReleaseCommandAcknowledgedByTheHolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newConvEnv(t, 2)
		e.withCommands()
		e.startAnt(12)
		ev := e.comment(ghRef(12), "lbianchi", "/anthill release")
		e.do(func() error { return e.claiming.HandleChange(e.ctx, ev) })
		if e.claimed(12) {
			t.Fatal("the claim is still held")
		}
		e.commands(e.conv, e.store, ev)
		acks := e.acks(ghRef(12), ev.CommentID)
		if len(acks) != 1 || !strings.Contains(acks[0].Body, domain.ReplyReleased) {
			t.Errorf("acknowledgements = %+v", acks)
		}
		if !slices.ContainsFunc(e.notifier.Sent(), func(n ports.Notification) bool { return n.Kind == ports.NotifyClaimLost }) {
			t.Error("the queen was not told about the lost claim")
		}
	})
}
