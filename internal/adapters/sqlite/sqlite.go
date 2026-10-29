// Package sqlite is the local store: the ports.StateStore adapter over an
// SQLite file, using the pure-Go modernc.org/sqlite driver so that no C
// compiler is needed (design D2, D13).
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	// The driver registers itself as "sqlite".
	_ "modernc.org/sqlite"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

//go:embed migrations/*.sql
var migrations embed.FS

// pragmas are set on every connection: write-ahead logging so readers do
// not block the writer, a busy timeout instead of failing at once on a
// lock, foreign keys checked, and fsync at checkpoints only, which WAL
// keeps safe against corruption.
var pragmas = []string{
	"journal_mode(WAL)",
	"busy_timeout(5000)",
	"foreign_keys(ON)",
	"synchronous(NORMAL)",
}

// Store is the SQLite state store. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

var _ ports.StateStore = (*Store)(nil)

// Open opens or creates the database at path and brings its schema to
// the latest version. It fails when the database was written by a newer
// Anthill.
func Open(ctx context.Context, path string) (*Store, error) {
	return open(ctx, path, migrations)
}

func open(ctx context.Context, path string, fsys fs.FS) (*Store, error) {
	params := make([]string, len(pragmas))
	for i, p := range pragmas {
		params[i] = "_pragma=" + p
	}
	db, err := sql.Open("sqlite", path+"?"+strings.Join(params, "&"))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One connection: the daemon is the only writer, and SQLite allows
	// one writer at a time, so a single connection never waits on a lock
	// held by itself.
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db, fsys); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// migration is one numbered file in migrations/, such as 0001_init.sql.
type migration struct {
	version int
	name    string
}

// migrate applies, in order, each migration newer than the database's
// user_version, each in its own transaction with the version bump.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	list, err := listMigrations(fsys)
	if err != nil {
		return err
	}
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if latest := len(list); current > latest {
		return fmt.Errorf("schema version %d is newer than this Anthill knows (%d)", current, latest)
	}
	for _, m := range list[current:] {
		script, err := fs.ReadFile(fsys, path.Join("migrations", m.name))
		if err != nil {
			return err
		}
		if err := apply(ctx, db, m, string(script)); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, m migration, script string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, script); err != nil {
		return err
	}
	// PRAGMA takes no parameters; the version is a number we made.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return err
	}
	return tx.Commit()
}

// listMigrations reads migrations/ and checks that the files are
// numbered 1, 2, 3… without gaps.
func listMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var list []migration
	for _, e := range entries {
		number, _, ok := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(number)
		if !ok || err != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("migration file %s is not named NNNN_name.sql", e.Name())
		}
		if version != len(list)+1 {
			return nil, fmt.Errorf("migration file %s should have number %d", e.Name(), len(list)+1)
		}
		list = append(list, migration{version: version, name: e.Name()})
	}
	return list, nil
}

// inTx runs fn in a transaction, committed only when fn succeeds.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback after Commit does nothing, so this undoes the transaction
	// only on the error paths.
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// refKey is a ref's text key, such as gh:acme/shop#12.
func refKey(r domain.Ref) string {
	return string(r.Repo.Provider) + ":" + r.Full()
}

// unixNano stores a time as nanoseconds, and the zero time as NULL.
func unixNano(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixNano(), Valid: true}
}

// fromUnixNano reads a time stored by unixNano, in UTC.
func fromUnixNano(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(0, n.Int64).UTC()
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func fromJSON(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func usage(tokens sql.NullInt64, cost sql.NullFloat64) ports.Usage {
	var u ports.Usage
	if tokens.Valid {
		u.Tokens = &tokens.Int64
	}
	if cost.Valid {
		u.CostUSD = &cost.Float64
	}
	return u
}

// mustAffect turns an update of no row into ports.ErrNotFound.
func mustAffect(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ports.ErrNotFound)
	}
	return nil
}
