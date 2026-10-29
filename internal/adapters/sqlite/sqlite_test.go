package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit/storetest"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "anthill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) ports.StateStore { return openTemp(t) })
}

func userVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrationsOnANewDatabase(t *testing.T) {
	s := openTemp(t)
	list, err := listMigrations(migrations)
	if err != nil {
		t.Fatal(err)
	}
	if got := userVersion(t, s); got != len(list) || got == 0 {
		t.Errorf("user_version = %d, want %d", got, len(list))
	}
	var journal string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Errorf("journal_mode = %q, %v", journal, err)
	}
	var fk int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v", fk, err)
	}
	for _, table := range []string{"ants", "transitions", "runs", "rounds", "poll_state", "hashes", "outbox",
		"stored_changes", "questions", "extension_requests", "hidden_proposals"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s: %d, %v", table, n, err)
		}
	}
}

func TestReopenKeepsDataAndSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anthill.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Hide(ctx, ports.HiddenProposal{Item: storetest.Item(1), HiddenAt: storetest.T0}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if hidden, err := s.Hidden(ctx); err != nil || len(hidden) != 1 {
		t.Errorf("Hidden() after reopen = %v, %v", hidden, err)
	}
}

func TestMigrationUpgradeAndRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anthill.db")
	v1 := fstest.MapFS{"migrations/0001_init.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")}}
	s, err := open(ctx, path, v1)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// A second migration applies on the next open; a failing third one
	// rolls back whole and leaves the version at 2.
	v3 := fstest.MapFS{
		"migrations/0001_init.sql": v1["migrations/0001_init.sql"],
		"migrations/0002_b.sql":    {Data: []byte("CREATE TABLE b (y INTEGER);")},
		"migrations/0003_bad.sql":  {Data: []byte("CREATE TABLE c (z INTEGER); INSERT INTO missing VALUES (1);")},
	}
	if _, err := open(ctx, path, v3); err == nil || !strings.Contains(err.Error(), "0003_bad.sql") {
		t.Fatalf("open with a bad migration: %v", err)
	}
	s, err = open(ctx, path, fstest.MapFS{
		"migrations/0001_init.sql": v3["migrations/0001_init.sql"],
		"migrations/0002_b.sql":    v3["migrations/0002_b.sql"],
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if v := userVersion(t, s); v != 2 {
		t.Errorf("user_version = %d, want 2", v)
	}
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('b', 'c')").Scan(&n)
	if n != 1 {
		t.Errorf("tables b and c: %d exist, want only b", n)
	}
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anthill.db")
	s := openTempAt(t, path)
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("Open() of a newer database: %v", err)
	}
}

func openTempAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrationNames(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"no number": {"migrations/init.sql": {}},
		"gap":       {"migrations/0001_a.sql": {}, "migrations/0003_c.sql": {}},
		"not sql":   {"migrations/0001_a.txt": {}},
	} {
		if _, err := listMigrations(fsys); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A failure while writing a poll, here a full disk on the outbox insert,
// leaves neither the new hashes, the event nor the cursor, so the next
// poll detects the same change again (design D7).
func TestRecordPollIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, err := s.db.Exec("CREATE TRIGGER full_disk BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT, 'disk full'); END")
	if err != nil {
		t.Fatal(err)
	}
	err = s.RecordPoll(ctx, ports.PollResult{
		Connection: "github", Cursor: "2", At: storetest.T0,
		Hashes: []ports.Hash{{Ref: storetest.Item(12), Group: "comments", Value: "h2"}},
		Events: []domain.ChangeEvent{{Ref: storetest.Item(12), Kind: domain.ChangeCommentAdded}},
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("RecordPoll() = %v", err)
	}
	hashes, _ := s.Hashes(ctx, storetest.Item(12))
	pending, _ := s.Pending(ctx)
	status, _ := s.PollStatus(ctx, "github")
	if len(hashes) != 0 || len(pending) != 0 || !status.LastSuccess.IsZero() {
		t.Errorf("a failed poll left hashes %v, events %v, status %+v", hashes, pending, status)
	}
}
