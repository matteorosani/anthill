package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RecordPoll stores a poll's cursor, hashes and change events in one
// transaction (design D7): after a crash, either the events are in the
// outbox and the hashes moved on, or neither, so the next poll detects
// the same changes again.
func (s *Store) RecordPoll(ctx context.Context, p ports.PollResult) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO poll_state (connection, cursor, last_success, last_error)
			VALUES (?, ?, ?, '')
			ON CONFLICT (connection) DO UPDATE SET cursor = excluded.cursor, last_success = excluded.last_success,
				last_error = '', last_error_at = NULL, rate_limited_until = NULL`,
			p.Connection, p.Cursor, unixNano(p.At))
		if err != nil {
			return err
		}
		for _, h := range p.Hashes {
			_, err := tx.ExecContext(ctx, `INSERT INTO hashes (ref_key, field_group, hash, text) VALUES (?, ?, ?, ?)
				ON CONFLICT (ref_key, field_group) DO UPDATE SET hash = excluded.hash, text = excluded.text`,
				refKey(h.Ref), h.Group, h.Value, h.Text)
			if err != nil {
				return err
			}
		}
		for _, e := range p.Events {
			event, err := toJSON(e)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO outbox (ref_key, event, detected_at) VALUES (?, ?, ?)",
				refKey(e.Ref), event, unixNano(p.At))
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordPollFailure stores a failed poll and keeps the cursor.
func (s *Store) RecordPollFailure(ctx context.Context, connection string, at time.Time, message string, rateLimitedUntil time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO poll_state (connection, cursor, last_error, last_error_at, rate_limited_until)
		VALUES (?, '', ?, ?, ?)
		ON CONFLICT (connection) DO UPDATE SET last_error = excluded.last_error,
			last_error_at = excluded.last_error_at, rate_limited_until = excluded.rate_limited_until`,
		connection, message, unixNano(at), unixNano(rateLimitedUntil))
	return err
}

// PollStatus returns a connection's state.
func (s *Store) PollStatus(ctx context.Context, connection string) (ports.PollStatus, error) {
	list, err := s.pollStatuses(ctx, "WHERE connection = ?", connection)
	if err != nil || len(list) == 0 {
		return ports.PollStatus{Connection: connection}, err
	}
	return list[0], nil
}

// PollStatuses lists every connection's state by name.
func (s *Store) PollStatuses(ctx context.Context) ([]ports.PollStatus, error) {
	return s.pollStatuses(ctx, "ORDER BY connection")
}

func (s *Store) pollStatuses(ctx context.Context, where string, args ...any) ([]ports.PollStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT connection, cursor, last_success, last_error, last_error_at, rate_limited_until
		FROM poll_state `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.PollStatus
	for rows.Next() {
		var (
			st                    ports.PollStatus
			success, errAt, until sql.NullInt64
		)
		if err := rows.Scan(&st.Connection, &st.Cursor, &success, &st.LastError, &errAt, &until); err != nil {
			return nil, err
		}
		st.LastSuccess, st.LastErrorAt, st.RateLimitedUntil = fromUnixNano(success), fromUnixNano(errAt), fromUnixNano(until)
		out = append(out, st)
	}
	return out, rows.Err()
}

// Hashes returns the stored hashes of an item or PR.
func (s *Store) Hashes(ctx context.Context, ref domain.Ref) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT field_group, hash FROM hashes WHERE ref_key = ?", refKey(ref))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var group, hash string
		if err := rows.Scan(&group, &hash); err != nil {
			return nil, err
		}
		out[group] = hash
	}
	return out, rows.Err()
}

// Texts returns the stored texts of an item or PR.
func (s *Store) Texts(ctx context.Context, ref domain.Ref) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT field_group, text FROM hashes WHERE ref_key = ? AND text <> ''", refKey(ref))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var group, text string
		if err := rows.Scan(&group, &text); err != nil {
			return nil, err
		}
		out[group] = text
	}
	return out, rows.Err()
}

// Pending lists the events not yet done, oldest first.
func (s *Store) Pending(ctx context.Context) ([]ports.OutboxEvent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, event, detected_at FROM outbox WHERE done_at IS NULL ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.OutboxEvent
	for rows.Next() {
		var (
			e        ports.OutboxEvent
			event    string
			detected sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &event, &detected); err != nil {
			return nil, err
		}
		if err := fromJSON(event, &e.Event); err != nil {
			return nil, fmt.Errorf("outbox event %d: %w", e.ID, err)
		}
		e.DetectedAt = fromUnixNano(detected)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Done marks an event handled. Marking it again keeps the first time.
func (s *Store) Done(ctx context.Context, id ports.OutboxID, at time.Time) error {
	res, err := s.db.ExecContext(ctx, "UPDATE outbox SET done_at = COALESCE(done_at, ?) WHERE id = ?", unixNano(at), id)
	if err != nil {
		return err
	}
	return mustAffect(res, fmt.Sprintf("outbox event %d", id))
}

// StoreChange stores a change unless its Source is already stored.
func (s *Store) StoreChange(ctx context.Context, c ports.StoredChange) error {
	event, err := toJSON(c.Event)
	if err != nil {
		return err
	}
	source := sql.NullInt64{Int64: int64(c.Source), Valid: c.Source != 0}
	_, err = s.db.ExecContext(ctx, `INSERT INTO stored_changes (ant_id, source, event, stored_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (source) DO NOTHING`, c.Ant, source, event, unixNano(c.StoredAt))
	return err
}

// StoredChanges lists an ant's changes not yet consumed.
func (s *Store) StoredChanges(ctx context.Context, ant ports.AntID) ([]ports.StoredChange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ant_id, source, event, stored_at FROM stored_changes
		WHERE ant_id = ? AND consumed_at IS NULL ORDER BY id`, ant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.StoredChange
	for rows.Next() {
		var (
			c              ports.StoredChange
			source, stored sql.NullInt64
			event          string
		)
		if err := rows.Scan(&c.ID, &c.Ant, &source, &event, &stored); err != nil {
			return nil, err
		}
		if err := fromJSON(event, &c.Event); err != nil {
			return nil, err
		}
		c.Source, c.StoredAt = ports.OutboxID(source.Int64), fromUnixNano(stored)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConsumeChanges marks changes as given to a run, all or none.
func (s *Store) ConsumeChanges(ctx context.Context, ids []int64, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, "UPDATE stored_changes SET consumed_at = ? WHERE id = ?", unixNano(at), id)
			if err != nil {
				return err
			}
			if err := mustAffect(res, fmt.Sprintf("stored change %d", id)); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveQuestion stores or updates a question.
func (s *Store) SaveQuestion(ctx context.Context, q ports.QuestionRecord) error {
	on, err := toJSON(q.On)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO questions (comment_id, ant_id, on_ref, asked_at, answered_at, answer_id, stale_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (comment_id) DO UPDATE SET ant_id = excluded.ant_id, on_ref = excluded.on_ref,
			asked_at = excluded.asked_at, answered_at = excluded.answered_at, answer_id = excluded.answer_id,
			stale_at = excluded.stale_at`,
		q.CommentID, q.Ant, on, unixNano(q.AskedAt), unixNano(q.AnsweredAt), q.AnswerID, unixNano(q.StaleAt))
	return err
}

// PendingQuestions lists the questions without an answer.
func (s *Store) PendingQuestions(ctx context.Context) ([]ports.QuestionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT comment_id, ant_id, on_ref, asked_at, answered_at, answer_id, stale_at
		FROM questions WHERE answered_at IS NULL ORDER BY asked_at, comment_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.QuestionRecord
	for rows.Next() {
		var (
			q                      ports.QuestionRecord
			on                     string
			asked, answered, stale sql.NullInt64
		)
		if err := rows.Scan(&q.CommentID, &q.Ant, &on, &asked, &answered, &q.AnswerID, &stale); err != nil {
			return nil, err
		}
		if err := fromJSON(on, &q.On); err != nil {
			return nil, err
		}
		q.AskedAt, q.AnsweredAt, q.StaleAt = fromUnixNano(asked), fromUnixNano(answered), fromUnixNano(stale)
		out = append(out, q)
	}
	return out, rows.Err()
}

// SaveExtension stores a new request when r.ID is 0, or updates r.ID.
func (s *Store) SaveExtension(ctx context.Context, r ports.ExtensionRecord) (ports.ExtensionID, error) {
	q := r.Request
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO extension_requests
			(ant_id, minutes, reason, at, status, granted, message, decided_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Ant, q.Minutes, q.Reason, unixNano(q.At), string(q.Status), q.Granted, q.Message, unixNano(r.DecidedAt))
		if err != nil {
			return 0, err
		}
		id, err := res.LastInsertId()
		return ports.ExtensionID(id), err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE extension_requests SET ant_id = ?, minutes = ?, reason = ?, at = ?,
		status = ?, granted = ?, message = ?, decided_at = ? WHERE id = ?`,
		r.Ant, q.Minutes, q.Reason, unixNano(q.At), string(q.Status), q.Granted, q.Message, unixNano(r.DecidedAt), r.ID)
	if err != nil {
		return 0, err
	}
	return r.ID, mustAffect(res, fmt.Sprintf("extension request %d", r.ID))
}

// PendingExtensions lists the requests waiting for the queen.
func (s *Store) PendingExtensions(ctx context.Context) ([]ports.ExtensionRecord, error) {
	return s.extensions(ctx, "WHERE status = ? ORDER BY id", string(domain.ExtensionPending))
}

// Extensions lists an ant's requests.
func (s *Store) Extensions(ctx context.Context, ant ports.AntID) ([]ports.ExtensionRecord, error) {
	return s.extensions(ctx, "WHERE ant_id = ? ORDER BY id", ant)
}

func (s *Store) extensions(ctx context.Context, where string, args ...any) ([]ports.ExtensionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ant_id, minutes, reason, at, status, granted, message, decided_at
		FROM extension_requests `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.ExtensionRecord
	for rows.Next() {
		var (
			r           ports.ExtensionRecord
			status      string
			at, decided sql.NullInt64
		)
		q := &r.Request
		if err := rows.Scan(&r.ID, &r.Ant, &q.Minutes, &q.Reason, &at, &status, &q.Granted, &q.Message, &decided); err != nil {
			return nil, err
		}
		q.Status, q.At, r.DecidedAt = domain.ExtensionStatus(status), fromUnixNano(at), fromUnixNano(decided)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Hide stores or replaces an item's hidden entry.
func (s *Store) Hide(ctx context.Context, h ports.HiddenProposal) error {
	item, err := toJSON(h.Item)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO hidden_proposals (item_key, item, hidden_at, until, item_hash)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (item_key) DO UPDATE SET item = excluded.item, hidden_at = excluded.hidden_at,
			until = excluded.until, item_hash = excluded.item_hash`,
		refKey(h.Item), item, unixNano(h.HiddenAt), unixNano(h.Until), h.ItemHash)
	return err
}

// Unhide removes an item's hidden entry.
func (s *Store) Unhide(ctx context.Context, item domain.Ref) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM hidden_proposals WHERE item_key = ?", refKey(item))
	return err
}

// Hidden lists the hidden entries, oldest first.
func (s *Store) Hidden(ctx context.Context) ([]ports.HiddenProposal, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT item, hidden_at, until, item_hash FROM hidden_proposals ORDER BY hidden_at, item_key")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.HiddenProposal
	for rows.Next() {
		var (
			h             ports.HiddenProposal
			item          string
			hidden, until sql.NullInt64
		)
		if err := rows.Scan(&item, &hidden, &until, &h.ItemHash); err != nil {
			return nil, err
		}
		if err := fromJSON(item, &h.Item); err != nil {
			return nil, err
		}
		h.HiddenAt, h.Until = fromUnixNano(hidden), fromUnixNano(until)
		out = append(out, h)
	}
	return out, rows.Err()
}

// SaveClaim stores or replaces a held claim.
func (s *Store) SaveClaim(ctx context.Context, c ports.HeldClaim) error {
	ref, err := toJSON(c.Ref)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO held_claims
			(ref_key, kind, ref, instance, comment_id, label, assigned, parked, lease_ns, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (ref_key, kind) DO UPDATE SET ref = excluded.ref, instance = excluded.instance,
			comment_id = excluded.comment_id, label = excluded.label, assigned = excluded.assigned,
			parked = excluded.parked, lease_ns = excluded.lease_ns, updated_at = excluded.updated_at`,
		refKey(c.Ref), string(c.Kind), ref, c.Instance, c.CommentID, c.Label, c.Assigned, c.Parked, int64(c.Lease), unixNano(c.UpdatedAt))
	return err
}

// DeleteClaim removes a held claim.
func (s *Store) DeleteClaim(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM held_claims WHERE ref_key = ? AND kind = ?", refKey(ref), string(kind))
	return err
}

// HeldClaims lists the held claims by ref and kind.
func (s *Store) HeldClaims(ctx context.Context) ([]ports.HeldClaim, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ref, kind, instance, comment_id, label, assigned, parked, lease_ns, updated_at
		FROM held_claims ORDER BY ref_key, kind`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.HeldClaim
	for rows.Next() {
		var (
			c         ports.HeldClaim
			ref, kind string
			lease     int64
			updated   sql.NullInt64
		)
		if err := rows.Scan(&ref, &kind, &c.Instance, &c.CommentID, &c.Label, &c.Assigned, &c.Parked, &lease, &updated); err != nil {
			return nil, err
		}
		if err := fromJSON(ref, &c.Ref); err != nil {
			return nil, err
		}
		c.Kind, c.Lease, c.UpdatedAt = domain.ClaimKind(kind), time.Duration(lease), fromUnixNano(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}
