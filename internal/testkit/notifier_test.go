package testkit_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestNotifier(t *testing.T) {
	ctx := context.Background()
	n := &testkit.Notifier{}
	var wg sync.WaitGroup
	for _, kind := range ports.NotificationKinds {
		wg.Go(func() {
			if err := n.Notify(ctx, ports.Notification{Kind: kind, Title: string(kind)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	kinds := n.Kinds()
	slices.Sort(kinds)
	want := slices.Clone(ports.NotificationKinds)
	slices.Sort(want)
	if !slices.Equal(kinds, want) || len(n.Sent()) != len(want) {
		t.Errorf("Kinds() = %v", kinds)
	}
	boom := errors.New("no desktop")
	n.Err = boom
	if err := n.Notify(ctx, ports.Notification{Kind: ports.NotifyQuestion}); !errors.Is(err, boom) {
		t.Errorf("Notify() = %v", err)
	}
	if len(n.Sent()) != len(want) {
		t.Error("a failed notification was recorded")
	}
}
