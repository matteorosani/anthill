package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestOutboxDispatcherDrain(t *testing.T) {
	ctx := context.Background()
	store := testkit.NewStateStore()
	clock := testkit.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	repo := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "shop"}
	var events []domain.ChangeEvent
	for n := 1; n <= 3; n++ {
		events = append(events, domain.ChangeEvent{Ref: domain.Ref{Repo: repo, Kind: domain.KindItem, Number: n}, Kind: domain.ChangeCommentAdded})
	}
	if err := store.RecordPoll(ctx, ports.PollResult{Connection: "github", At: clock.Now(), Events: events}); err != nil {
		t.Fatal(err)
	}

	var handled []int
	boom := errors.New("provider down")
	fail := 2
	d := app.NewOutboxDispatcher(store, clock, func(_ context.Context, e ports.OutboxEvent) error {
		if e.Event.Ref.Number == fail {
			return boom
		}
		handled = append(handled, e.Event.Ref.Number)
		return nil
	})

	// The second event fails: the first is done, the second and third
	// stay pending in order.
	n, err := d.Drain(ctx)
	if !errors.Is(err, boom) || n != 1 || !slices.Equal(handled, []int{1}) {
		t.Fatalf("Drain() = %d, %v; handled %v", n, err, handled)
	}
	pending, _ := store.Pending(ctx)
	if len(pending) != 2 || pending[0].Event.Ref.Number != 2 {
		t.Fatalf("pending = %+v", pending)
	}

	fail = 0
	if n, err := d.Drain(ctx); err != nil || n != 2 || !slices.Equal(handled, []int{1, 2, 3}) {
		t.Errorf("second Drain() = %d, %v; handled %v", n, err, handled)
	}
	if n, err := d.Drain(ctx); err != nil || n != 0 {
		t.Errorf("Drain() of an empty outbox = %d, %v", n, err)
	}
}
