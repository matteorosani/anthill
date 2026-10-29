package testkit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
	"github.com/matteorosani/anthill/internal/testkit/storetest"
)

func TestStateStoreContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) ports.StateStore { return testkit.NewStateStore() })
}

func TestStateStoreReturnsCopies(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewStateStore()
	r := storetest.Ant(1, domain.AntStarting)
	id, _ := s.CreateAnt(ctx, r)
	got, _ := s.Ant(ctx, id)
	got.History[0].Reason = "changed"
	again, _ := s.Ant(ctx, id)
	if again.History[0].Reason != "test" {
		t.Error("changing a returned record changed the store")
	}
}

func TestStateStoreConcurrentUse(t *testing.T) {
	ctx := context.Background()
	s := testkit.NewStateStore()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			r := storetest.Ant(i%5+1, domain.AntQueued)
			if _, err := s.CreateAnt(ctx, r); err != nil {
				t.Error(err)
			}
			_ = s.RecordPoll(ctx, ports.PollResult{Connection: "github", Events: []domain.ChangeEvent{{Ref: r.Item}}})
			_, _ = s.Ants(ctx)
		})
	}
	wg.Wait()
	if ants, _ := s.Ants(ctx); len(ants) != 5 {
		t.Errorf("Ants() = %d current ants, want 5", len(ants))
	}
	if pending, _ := s.Pending(ctx); len(pending) != 20 {
		t.Errorf("Pending() = %d events, want 20", len(pending))
	}
}
