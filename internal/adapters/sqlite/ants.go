package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

const antColumns = "id, item, base_ns, budget, runner, session_id, worktree, branch, pr"

// CreateAnt stores a new ant with its transitions.
func (s *Store) CreateAnt(ctx context.Context, r ports.AntRecord) (ports.AntID, error) {
	var id ports.AntID
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		row, err := antRow(r)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO ants
			(item_key, item, state, base_ns, budget, runner, session_id, worktree, branch, pr)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row...)
		if err != nil {
			return err
		}
		n, err := res.LastInsertId()
		if err != nil {
			return err
		}
		id = ports.AntID(n)
		return insertTransitions(ctx, tx, id, r.History, 0)
	})
	if err != nil {
		return 0, fmt.Errorf("create ant for %s: %w", r.Item.Full(), err)
	}
	return id, nil
}

// SaveAnt updates an ant and appends its new transitions.
func (s *Store) SaveAnt(ctx context.Context, r ports.AntRecord) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var stored int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM transitions WHERE ant_id = ?", r.ID).Scan(&stored); err != nil {
			return err
		}
		if len(r.History) < stored {
			return fmt.Errorf("ant %d: history cannot shrink from %d to %d transitions", r.ID, stored, len(r.History))
		}
		row, err := antRow(r)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE ants SET
			item_key = ?, item = ?, state = ?, base_ns = ?, budget = ?, runner = ?,
			session_id = ?, worktree = ?, branch = ?, pr = ?
			WHERE id = ?`, append(row, r.ID)...)
		if err != nil {
			return err
		}
		if err := mustAffect(res, fmt.Sprintf("ant %d", r.ID)); err != nil {
			return err
		}
		return insertTransitions(ctx, tx, r.ID, r.History[stored:], stored)
	})
}

// antRow returns the values of the ants columns after id, in order.
func antRow(r ports.AntRecord) ([]any, error) {
	item, err := toJSON(r.Item)
	if err != nil {
		return nil, err
	}
	budget, err := toJSON(r.Budget)
	if err != nil {
		return nil, err
	}
	var pr sql.NullString
	if r.PR != nil {
		if pr.String, err = toJSON(r.PR); err != nil {
			return nil, err
		}
		pr.Valid = true
	}
	return []any{refKey(r.Item), item, string(r.State()), int64(r.Base), budget, r.Runner, r.SessionID, r.Worktree, r.Branch, pr}, nil
}

func insertTransitions(ctx context.Context, tx *sql.Tx, ant ports.AntID, history []domain.Transition, first int) error {
	for i, t := range history {
		_, err := tx.ExecContext(ctx, `INSERT INTO transitions (ant_id, seq, from_state, to_state, at, reason)
			VALUES (?, ?, ?, ?, ?, ?)`, ant, first+i, string(t.From), string(t.To), unixNano(t.At), t.Reason)
		if err != nil {
			return err
		}
	}
	return nil
}

// Ant returns an ant by ID.
func (s *Store) Ant(ctx context.Context, id ports.AntID) (ports.AntRecord, error) {
	return s.oneAnt(ctx, "WHERE id = ?", fmt.Sprintf("ant %d", id), id)
}

// CurrentAnt returns the latest ant of an item.
func (s *Store) CurrentAnt(ctx context.Context, item domain.Ref) (ports.AntRecord, error) {
	return s.oneAnt(ctx, "WHERE item_key = ? ORDER BY id DESC LIMIT 1", "ant for item "+item.Full(), refKey(item))
}

// AntByBranch returns the latest ant on a branch.
func (s *Store) AntByBranch(ctx context.Context, branch string) (ports.AntRecord, error) {
	return s.oneAnt(ctx, "WHERE branch = ? ORDER BY id DESC LIMIT 1", "ant for branch "+branch, branch)
}

func (s *Store) oneAnt(ctx context.Context, where, what string, args ...any) (ports.AntRecord, error) {
	ants, err := s.ants(ctx, where, args...)
	if err != nil {
		return ports.AntRecord{}, err
	}
	if len(ants) == 0 {
		return ports.AntRecord{}, fmt.Errorf("%s: %w", what, ports.ErrNotFound)
	}
	return ants[0], nil
}

// Ants lists the current ant of every item in one of states.
func (s *Store) Ants(ctx context.Context, states ...domain.AntState) ([]ports.AntRecord, error) {
	where := "WHERE id IN (SELECT MAX(id) FROM ants GROUP BY item_key)"
	args := make([]any, len(states))
	if len(states) > 0 {
		for i, st := range states {
			args[i] = string(st)
		}
		where += " AND state IN (?" + strings.Repeat(", ?", len(states)-1) + ")"
	}
	return s.ants(ctx, where+" ORDER BY id", args...)
}

func (s *Store) ants(ctx context.Context, where string, args ...any) ([]ports.AntRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+antColumns+" FROM ants "+where, args...)
	if err != nil {
		return nil, err
	}
	var out []ports.AntRecord
	for rows.Next() {
		var (
			r            ports.AntRecord
			item, budget string
			base         int64
			pr           sql.NullString
		)
		if err := rows.Scan(&r.ID, &item, &base, &budget, &r.Runner, &r.SessionID, &r.Worktree, &r.Branch, &pr); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.Base = time.Duration(base)
		if err := fromJSON(item, &r.Item); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := fromJSON(budget, &r.Budget); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if pr.Valid {
			r.PR = new(domain.Ref)
			if err := fromJSON(pr.String, r.PR); err != nil {
				_ = rows.Close()
				return nil, err
			}
		}
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The transitions are read once the ants' rows are closed, since the
	// store has a single connection.
	for i := range out {
		if out[i].History, err = s.history(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) history(ctx context.Context, ant ports.AntID) ([]domain.Transition, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT from_state, to_state, at, reason FROM transitions WHERE ant_id = ? ORDER BY seq", ant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Transition
	for rows.Next() {
		var (
			t        domain.Transition
			from, to string
			at       sql.NullInt64
		)
		if err := rows.Scan(&from, &to, &at, &t.Reason); err != nil {
			return nil, err
		}
		t.From, t.To, t.At = domain.AntState(from), domain.AntState(to), fromUnixNano(at)
		out = append(out, t)
	}
	return out, rows.Err()
}

// StartRun stores an active run.
func (s *Store) StartRun(ctx context.Context, r ports.RunRecord) (ports.RunID, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO runs
		(ant_id, kind, session_id, started_at, ended_at, outcome, reason, tokens, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Ant, string(r.Kind), r.SessionID, r.StartedAt.UnixNano(), unixNano(r.EndedAt),
		string(r.Outcome), r.Reason, nullInt(r.Usage.Tokens), nullFloat(r.Usage.CostUSD))
	if err != nil {
		return 0, fmt.Errorf("start run of ant %d: %w", r.Ant, err)
	}
	id, err := res.LastInsertId()
	return ports.RunID(id), err
}

// EndRun stores the end of a run. An empty session ID keeps the stored
// one.
func (s *Store) EndRun(ctx context.Context, r ports.RunRecord) error {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET
		ended_at = ?, outcome = ?, reason = ?, tokens = ?, cost_usd = ?,
		session_id = CASE WHEN ? = '' THEN session_id ELSE ? END
		WHERE id = ?`,
		unixNano(r.EndedAt), string(r.Outcome), r.Reason, nullInt(r.Usage.Tokens), nullFloat(r.Usage.CostUSD),
		r.SessionID, r.SessionID, r.ID)
	if err != nil {
		return err
	}
	return mustAffect(res, fmt.Sprintf("run %d", r.ID))
}

// Runs lists an ant's runs.
func (s *Store) Runs(ctx context.Context, ant ports.AntID) ([]ports.RunRecord, error) {
	return s.runs(ctx, "WHERE ant_id = ? ORDER BY id", ant)
}

func (s *Store) runs(ctx context.Context, where string, args ...any) ([]ports.RunRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ant_id, kind, session_id, started_at, ended_at,
		outcome, reason, tokens, cost_usd FROM runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.RunRecord
	for rows.Next() {
		var (
			r             ports.RunRecord
			kind, outcome string
			started       int64
			ended, tokens sql.NullInt64
			cost          sql.NullFloat64
		)
		if err := rows.Scan(&r.ID, &r.Ant, &kind, &r.SessionID, &started, &ended, &outcome, &r.Reason, &tokens, &cost); err != nil {
			return nil, err
		}
		r.Kind, r.Outcome = ports.RunKind(kind), ports.RunOutcome(outcome)
		r.StartedAt, r.EndedAt = time.Unix(0, started).UTC(), fromUnixNano(ended)
		r.Usage = usage(tokens, cost)
		out = append(out, r)
	}
	return out, rows.Err()
}

// StartRound stores an active round.
func (s *Store) StartRound(ctx context.Context, r ports.RoundRecord) (ports.RoundID, error) {
	pr, err := toJSON(r.PR)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO rounds
		(pr_key, pr, head, requested, started_at, ended_at, tokens, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		refKey(r.PR), pr, r.Head, r.Requested, r.StartedAt.UnixNano(), unixNano(r.EndedAt),
		nullInt(r.Usage.Tokens), nullFloat(r.Usage.CostUSD))
	if err != nil {
		return 0, fmt.Errorf("start round on %s: %w", r.PR.Full(), err)
	}
	id, err := res.LastInsertId()
	return ports.RoundID(id), err
}

// EndRound stores the end of a round.
func (s *Store) EndRound(ctx context.Context, r ports.RoundRecord) error {
	res, err := s.db.ExecContext(ctx, "UPDATE rounds SET ended_at = ?, tokens = ?, cost_usd = ? WHERE id = ?",
		unixNano(r.EndedAt), nullInt(r.Usage.Tokens), nullFloat(r.Usage.CostUSD), r.ID)
	if err != nil {
		return err
	}
	return mustAffect(res, fmt.Sprintf("round %d", r.ID))
}

// Rounds lists a PR's rounds.
func (s *Store) Rounds(ctx context.Context, pr domain.Ref) ([]ports.RoundRecord, error) {
	return s.rounds(ctx, "WHERE pr_key = ? ORDER BY id", refKey(pr))
}

func (s *Store) rounds(ctx context.Context, where string, args ...any) ([]ports.RoundRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, pr, head, requested, started_at, ended_at, tokens, cost_usd
		FROM rounds `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.RoundRecord
	for rows.Next() {
		var (
			r             ports.RoundRecord
			pr            string
			started       int64
			ended, tokens sql.NullInt64
			cost          sql.NullFloat64
		)
		if err := rows.Scan(&r.ID, &pr, &r.Head, &r.Requested, &started, &ended, &tokens, &cost); err != nil {
			return nil, err
		}
		if err := fromJSON(pr, &r.PR); err != nil {
			return nil, err
		}
		r.StartedAt, r.EndedAt = time.Unix(0, started).UTC(), fromUnixNano(ended)
		r.Usage = usage(tokens, cost)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Usage lists the runs and rounds started in [from, to).
func (s *Store) Usage(ctx context.Context, from, to time.Time) ([]ports.RunRecord, []ports.RoundRecord, error) {
	const where = "WHERE started_at >= ? AND started_at < ? ORDER BY id"
	runs, err := s.runs(ctx, where, from.UnixNano(), to.UnixNano())
	if err != nil {
		return nil, nil, err
	}
	rounds, err := s.rounds(ctx, where, from.UnixNano(), to.UnixNano())
	return runs, rounds, err
}
