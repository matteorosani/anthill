package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// errClosedStore is what closingStore returns once closed, as the SQLite
// store returns sql.ErrConnDone.
var errClosedStore = errors.New("store closed")

// closingStore is a store that can be closed, as the daemon closes its
// store when it stops. Once closed, the calls that the work after a run
// or a round uses fail with errClosedStore, and count as late uses.
type closingStore struct {
	*testkit.StateStore
	closed atomic.Bool
	late   atomic.Int32
}

func (c *closingStore) close()  { c.closed.Store(true) }
func (c *closingStore) reopen() { c.closed.Store(false) }

func (c *closingStore) check() error {
	if c.closed.Load() {
		c.late.Add(1)
		return errClosedStore
	}
	return nil
}

func (c *closingStore) Ant(ctx context.Context, id ports.AntID) (ports.AntRecord, error) {
	if err := c.check(); err != nil {
		return ports.AntRecord{}, err
	}
	return c.StateStore.Ant(ctx, id)
}

func (c *closingStore) Ants(ctx context.Context, states ...domain.AntState) ([]ports.AntRecord, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	return c.StateStore.Ants(ctx, states...)
}

func (c *closingStore) SaveAnt(ctx context.Context, r ports.AntRecord) error {
	if err := c.check(); err != nil {
		return err
	}
	return c.StateStore.SaveAnt(ctx, r)
}

func (c *closingStore) Runs(ctx context.Context, ant ports.AntID) ([]ports.RunRecord, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	return c.StateStore.Runs(ctx, ant)
}

func (c *closingStore) EndRun(ctx context.Context, r ports.RunRecord) error {
	if err := c.check(); err != nil {
		return err
	}
	return c.StateStore.EndRun(ctx, r)
}

func (c *closingStore) Rounds(ctx context.Context, pr domain.Ref) ([]ports.RoundRecord, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	return c.StateStore.Rounds(ctx, pr)
}

func (c *closingStore) BeginRound(ctx context.Context, r ports.RoundRecord) (ports.RoundID, error) {
	if err := c.check(); err != nil {
		return 0, err
	}
	return c.StateStore.BeginRound(ctx, r)
}

func (c *closingStore) EndRound(ctx context.Context, r ports.RoundRecord) error {
	if err := c.check(); err != nil {
		return err
	}
	return c.StateStore.EndRound(ctx, r)
}

func (c *closingStore) HeldClaims(ctx context.Context) ([]ports.HeldClaim, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	return c.StateStore.HeldClaims(ctx)
}

func (c *closingStore) DeleteClaim(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) error {
	if err := c.check(); err != nil {
		return err
	}
	return c.StateStore.DeleteClaim(ctx, ref, kind)
}

// Task 11.6: when the daemon stops, the supervisor lets go of its active
// runs. A run that ends after Detach and Wait is not recorded and its end
// never reaches the closed store: the ant keeps its active run for the
// next start's Reconcile ("interrupted by restart"), and no queued ant
// starts in the freed slot.
func TestDetachLeavesRunsToTheRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &closingStore{}
		e := newSupEnv(t, 1, func(d *app.AntSupervisorDeps) {
			store.StateStore = d.Store.(*testkit.StateStore)
			d.Store = store
		})
		running := e.mustAccept(12)
		e.wantState(running, domain.AntRunning)
		queued := e.mustAccept(13)
		e.wantState(queued, domain.AntQueued)

		e.sup.Detach()
		e.sup.Detach() // a second call does nothing
		done := make(chan struct{})
		go func() {
			e.sup.Wait()
			close(done)
		}()
		e.settle()
		select {
		case <-done:
		default:
			t.Fatal("Wait did not return after Detach with a run still active")
		}
		store.close()

		// The agent exits after the daemon closed its store.
		e.end(ghRef(12), ports.ExecExited)
		if n := store.late.Load(); n != 0 {
			t.Fatalf("the store was used %d times after it was closed", n)
		}
		if got := len(e.exec.Executions()); got != 1 {
			t.Errorf("executions = %d, want only the first run's", got)
		}
		if got := e.ends.all(); len(got) != 0 {
			t.Errorf("run ends recorded = %+v, want none", got)
		}

		store.reopen()
		e.wantState(running, domain.AntRunning)
		e.wantState(queued, domain.AntQueued)
		if runs := e.runs(running); len(runs) != 1 || !runs[0].EndedAt.IsZero() {
			t.Errorf("runs = %+v, want the run still open", runs)
		}
	})
}

// stoppableRounds is a RoundRunner whose round lasts until its context
// ends, as a review agent that Anthill stops, and then fails with the
// context's error.
type stoppableRounds struct {
	started chan app.RoundStart
}

func (r *stoppableRounds) Run(ctx context.Context, s app.RoundStart) (app.RoundResult, error) {
	r.started <- s
	<-ctx.Done()
	return app.RoundResult{}, ctx.Err()
}

// Task 11.6: when the daemon stops, the shepherd ends the round in
// progress, records its end, releases its review claim and starts
// nothing new, though a round is due on the PR's new head. After Wait,
// it uses the store no more.
func TestShepherdDetachEndsTheRoundAndStartsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newShepherdEnv(t)
		store := &closingStore{StateStore: testkit.NewStateStore()}
		client := e.gh.Client("mrosani")
		claiming := app.NewClaiming(func(context.Context, domain.Ref) (app.ClaimTarget, error) {
			return app.ClaimTarget{Provider: client, Instance: instanceA, Label: domain.DefaultClaimLabel}, nil
		}, store, e.clock, &testkit.Notifier{}, &testkit.ClaimLossHandler{}, app.ClaimingOptions{})
		rounds := &stoppableRounds{started: make(chan app.RoundStart, 4)}
		var errs []error
		shepherd := app.NewShepherd(app.ShepherdDeps{
			Store:  store,
			Claims: claiming,
			Route: func(_ context.Context, ref domain.Ref) (app.ToolWorkspace, error) {
				return app.ToolWorkspace{Repo: ref.Repo, Settings: domain.Defaults()}, nil
			},
			CodeHost: func(context.Context, domain.Repository) (ports.CodeHostPort, error) {
				return client, nil
			},
			WriteAccess: client.CanWrite,
			Instance:    func(domain.Repository) string { return instanceA },
			Runners:     app.NewRunners(map[domain.Runner]ports.AgentRunner{domain.RunnerClaudeCode: testkit.NewAgentRunner(ports.Capabilities{})}),
			Rounds:      rounds,
			RoundEnds:   app.NewUsageRecorder(store),
			Clock:       e.clock,
			// Errors is only called from the shepherd's goroutines,
			// which have all returned when it is read below.
			Errors: func(err error) { errs = append(errs, err) },
		})
		pr := e.putPR(shop, 34, "ant/gh-12-fix-login", nil)

		if err := e.run(func() error { return shepherd.Trigger(e.ctx, pr) }); err != nil {
			t.Fatal(err)
		}
		if len(rounds.started) != 1 {
			t.Fatalf("%d rounds started, want 1 in progress", len(rounds.started))
		}
		// A new head: a round would be due once this one ends.
		e.push(pr, "c2")

		shepherd.Detach()
		shepherd.Detach() // a second call does nothing
		_ = e.run(func() error {
			shepherd.Wait()
			return nil
		})
		store.close()
		if err := e.run(func() error { return shepherd.Trigger(e.ctx, pr) }); err != nil {
			t.Errorf("Trigger after Detach = %v, want nil", err)
		}
		if n := store.late.Load(); n != 0 {
			t.Fatalf("the store was used %d times after it was closed", n)
		}
		if len(errs) != 0 {
			t.Errorf("background errors = %v, want none", errs)
		}
		if got := len(rounds.started); got != 1 {
			t.Errorf("rounds started = %d, want only the one stopped", got)
		}

		store.reopen()
		ended, err := store.Rounds(e.ctx, pr)
		if err != nil || len(ended) != 1 || ended[0].EndedAt.IsZero() {
			t.Errorf("rounds = %+v, %v; want the round ended", ended, err)
		}
		if held, _ := store.HeldClaims(e.ctx); len(held) != 0 {
			t.Errorf("held claims = %+v, want the review claim released", held)
		}
	})
}
