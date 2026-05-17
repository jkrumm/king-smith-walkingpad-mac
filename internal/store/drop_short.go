package store

import (
	"context"
	"fmt"
	"time"
)

// DropShortStandaloneSessions deletes closed sessions whose duration_s is
// below minDuration AND whose ended_at is older than (now - resurrectionWindow)
// — i.e. sessions that are too short to be meaningful AND no longer
// eligible for forward resurrection (so we know they'll never grow).
//
// Each deleted session leaves a session_tombstones row (same tx) so the
// sync worker can DELETE the upstream Argo row on the next tick.
//
// Returns the UUIDs that were dropped. Safe to call concurrently with the
// session-grouping manager — only closed rows are touched and SQLite's WAL
// serializes the writes.
//
// minDuration == 0 disables the pass (no rows touched, returns nil, nil).
func (s *Store) DropShortStandaloneSessions(ctx context.Context, minDuration, resurrectWindow time.Duration, now time.Time) ([]string, error) {
	if minDuration <= 0 {
		return nil, nil
	}
	cutoff := now.Add(-resurrectWindow)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid
		FROM sessions
		WHERE ended_at IS NOT NULL
		  AND duration_s < ?
		  AND ended_at < ?`,
		int64(minDuration.Seconds()), formatTime(cutoff),
	)
	if err != nil {
		return nil, fmt.Errorf("list droppable sessions: %w", err)
	}
	type candidate struct {
		id   int64
		uuid string
	}
	var pending []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.uuid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pending = append(pending, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return nil, nil
	}

	dropped := make([]string, 0, len(pending))
	for _, c := range pending {
		if ctx.Err() != nil {
			break
		}
		if err := s.dropOneSession(ctx, c.id, c.uuid, now); err != nil {
			return dropped, fmt.Errorf("drop %s: %w", c.uuid, err)
		}
		dropped = append(dropped, c.uuid)
	}
	return dropped, nil
}

// dropOneSession deletes the session row + its samples and writes a tombstone
// in a single tx so the row never exists locally without an upstream-delete
// intent recorded alongside.
func (s *Store) dropOneSession(ctx context.Context, id int64, uuid string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	if _, err := tx.ExecContext(ctx, `DELETE FROM samples WHERE session_id = ?`, id); err != nil {
		rollback()
		return fmt.Errorf("delete samples: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		rollback()
		return fmt.Errorf("delete session: %w", err)
	}
	if err := writeTombstone(ctx, tx, uuid, now); err != nil {
		rollback()
		return err
	}
	return tx.Commit()
}
