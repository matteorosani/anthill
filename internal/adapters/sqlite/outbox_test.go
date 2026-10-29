package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/sqlite"
	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
	"github.com/matteorosani/anthill/internal/testkit/storetest"
)

// crashTest is one daemon's life over a database file: each restart
// closes the store, as a crash would, and opens it again.
type crashTest struct {
	t        *testing.T
	path     string
	store    *sqlite.Store
	clock    *testkit.Clock
	provider *testkit.Provider
	ant      ports.AntID
	comment  domain.ChangeEvent
}

func newCrashTest(t *testing.T) *crashTest {
	t.Helper()
	ct := &crashTest{t: t, path: filepath.Join(t.TempDir(), "anthill.db"), clock: testkit.NewClock(storetest.T0)}
	ct.provider = testkit.NewProvider(ct.clock)
	ct.provider.PutItem(domain.WorkItem{Ref: storetest.Item(12), State: domain.StateOpen})
	ct.open()
	ctx := context.Background()
	var err error
	ct.ant, err = ct.store.CreateAnt(ctx, storetest.Ant(12, domain.AntStarting, domain.AntRunning, domain.AntAwaitingInput))
	if err != nil {
		t.Fatal(err)
	}
	// Detection: a developer comments on #12, and the poll writes the
	// change event to the outbox with the new hashes.
	c := ct.provider.Comment(storetest.Item(12), "dev", "please also handle the empty password")
	ct.comment = domain.ChangeEvent{Ref: storetest.Item(12), Kind: domain.ChangeCommentAdded, Actor: "dev", At: c.CreatedAt,
		Origin: domain.OriginHuman, CommentID: c.ID, Body: c.Body}
	err = ct.store.RecordPoll(ctx, ports.PollResult{Connection: "github", Cursor: "1", At: ct.clock.Now(),
		Hashes: []ports.Hash{{Ref: storetest.Item(12), Group: "comments", Value: "h1"}}, Events: []domain.ChangeEvent{ct.comment}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ct.store.Close() })
	return ct
}

func (ct *crashTest) open() {
	s, err := sqlite.Open(context.Background(), ct.path)
	if err != nil {
		ct.t.Fatal(err)
	}
	ct.store = s
}

// restart stands in for a crash and a new daemon start.
func (ct *crashTest) restart() {
	if err := ct.store.Close(); err != nil {
		ct.t.Fatal(err)
	}
	ct.open()
}

// handle is a change-reaction handler for an ant between runs: it stores
// the change for the ant's next run and posts a status comment. Both
// effects are idempotent, keyed by the outbox ID.
func (ct *crashTest) handle(ctx context.Context, e ports.OutboxEvent) error {
	ant, err := ct.store.CurrentAnt(ctx, e.Event.Ref)
	if err != nil {
		return err
	}
	err = ct.store.StoreChange(ctx, ports.StoredChange{Ant: ant.ID, Source: e.ID, Event: e.Event, StoredAt: ct.clock.Now()})
	if err != nil {
		return err
	}
	_, err = ct.provider.Client("dev").AddComment(ctx, e.Event.Ref, domain.AnthillComment{
		Role: domain.RoleAnt,
		Body: "Change stored for the next run.",
		Marker: domain.Marker{Version: domain.MarkerVersion, ID: fmt.Sprintf("outbox-%d", e.ID),
			Instance: "dev@laptop", Kind: domain.CommentStatus, Ref: e.Event.Ref.String()},
	})
	return err
}

// effects counts the stored changes and Anthill comments the handler
// made.
func (ct *crashTest) effects() (stored, comments int) {
	ct.t.Helper()
	changes, err := ct.store.StoredChanges(context.Background(), ct.ant)
	if err != nil {
		ct.t.Fatal(err)
	}
	item, _ := ct.provider.Item(storetest.Item(12))
	for _, c := range item.Comments {
		if c.Origin() == domain.OriginAnthill {
			comments++
		}
	}
	return len(changes), comments
}

func (ct *crashTest) drain(handle app.OutboxHandler) (int, error) {
	return app.NewOutboxDispatcher(ct.store, ct.clock, handle).Drain(context.Background())
}

// Spec change-reaction, "Restart with a pending change": the daemon
// restarts after detecting an actionable comment but before acting on
// it; after restart the comment is handled once.
func TestRestartWithAPendingChange(t *testing.T) {
	ct := newCrashTest(t)
	ct.restart()

	if n, err := ct.drain(ct.handle); err != nil || n != 1 {
		t.Fatalf("Drain() after restart = %d, %v", n, err)
	}
	if stored, comments := ct.effects(); stored != 1 || comments != 1 {
		t.Errorf("effects = %d stored changes, %d comments; want 1 and 1", stored, comments)
	}
	changes, _ := ct.store.StoredChanges(context.Background(), ct.ant)
	if changes[0].Event != ct.comment {
		t.Errorf("stored change = %+v, want %+v", changes[0].Event, ct.comment)
	}

	// A later restart hands nothing out again.
	ct.restart()
	if n, err := ct.drain(ct.handle); err != nil || n != 0 {
		t.Errorf("Drain() after the second restart = %d, %v", n, err)
	}
	if stored, comments := ct.effects(); stored != 1 || comments != 1 {
		t.Errorf("effects = %d, %d after the second restart", stored, comments)
	}
}

// The harder crash: the handler's effects are written, but the daemon
// dies before marking the event done. The event is handed out again, and
// the idempotent handler does not repeat its effects.
func TestRestartAfterHandlingBeforeDone(t *testing.T) {
	ct := newCrashTest(t)
	crash := errors.New("crash before Done")
	n, err := ct.drain(func(ctx context.Context, e ports.OutboxEvent) error {
		if err := ct.handle(ctx, e); err != nil {
			return err
		}
		return crash
	})
	if !errors.Is(err, crash) || n != 0 {
		t.Fatalf("first Drain() = %d, %v", n, err)
	}
	ct.restart()

	if n, err := ct.drain(ct.handle); err != nil || n != 1 {
		t.Fatalf("Drain() after restart = %d, %v", n, err)
	}
	if stored, comments := ct.effects(); stored != 1 || comments != 1 {
		t.Errorf("effects = %d stored changes, %d comments; want 1 and 1", stored, comments)
	}
	if pending, _ := ct.store.Pending(context.Background()); len(pending) != 0 {
		t.Errorf("pending after restart = %+v", pending)
	}
}
