package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// RequestRound stores a pending request, unless the PR has one for the
// comment.
func (s *Store) RequestRound(ctx context.Context, r ports.RoundRequest) error {
	pr, err := toJSON(r.PR)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO round_requests (pr_key, pr, comment_id, at)
		VALUES (?, ?, ?, ?) ON CONFLICT (pr_key, comment_id) DO NOTHING`,
		refKey(r.PR), pr, r.CommentID, r.At.UnixNano())
	if err != nil {
		return fmt.Errorf("request a round on %s: %w", r.PR.Full(), err)
	}
	return nil
}

// RoundRequests lists a PR's pending requests.
func (s *Store) RoundRequests(ctx context.Context, pr domain.Ref) ([]ports.RoundRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pr, comment_id, at FROM round_requests
		WHERE pr_key = ? AND round_id IS NULL AND dropped = 0 ORDER BY id`, refKey(pr))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ports.RoundRequest
	for rows.Next() {
		var (
			r   ports.RoundRequest
			ref string
			at  int64
		)
		if err := rows.Scan(&ref, &r.CommentID, &at); err != nil {
			return nil, err
		}
		if err := fromJSON(ref, &r.PR); err != nil {
			return nil, err
		}
		r.At = time.Unix(0, at).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// DropRoundRequest marks a pending request as served elsewhere.
func (s *Store) DropRoundRequest(ctx context.Context, pr domain.Ref, commentID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE round_requests SET dropped = 1
		WHERE pr_key = ? AND comment_id = ? AND round_id IS NULL`, refKey(pr), commentID)
	if err != nil {
		return fmt.Errorf("drop the round request %s on %s: %w", commentID, pr.Full(), err)
	}
	return nil
}

// BeginRound stores an active round and serves the PR's pending requests
// with it when it is requested, in one transaction.
func (s *Store) BeginRound(ctx context.Context, r ports.RoundRecord) (ports.RoundID, error) {
	var id ports.RoundID
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = startRound(ctx, tx, r); err != nil || !r.Requested {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE round_requests SET round_id = ? WHERE pr_key = ? AND round_id IS NULL AND dropped = 0", id, refKey(r.PR))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("begin round on %s: %w", r.PR.Full(), err)
	}
	return id, nil
}

// RecordFixPush stores a head a fix pass pushed, unless it is stored.
func (s *Store) RecordFixPush(ctx context.Context, pr domain.Ref, head string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO fix_pushes (pr_key, head, at)
		VALUES (?, ?, ?) ON CONFLICT (pr_key, head) DO NOTHING`, refKey(pr), head, at.UnixNano())
	if err != nil {
		return fmt.Errorf("record the fix push %s on %s: %w", head, pr.Full(), err)
	}
	return nil
}

// FixPushes lists the heads fix passes pushed to a PR.
func (s *Store) FixPushes(ctx context.Context, pr domain.Ref) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT head FROM fix_pushes WHERE pr_key = ? ORDER BY at, rowid`, refKey(pr))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var head string
		if err := rows.Scan(&head); err != nil {
			return nil, err
		}
		out = append(out, head)
	}
	return out, rows.Err()
}
