package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Tombstone is a pending delete that the sync worker must propagate to Argo.
// Rows are created by stitch (merged-away survivor row) and by the drop pass
// (sub-threshold standalone session). Once Argo has DELETEd the upstream row,
// the worker stamps synced_at. GCTombstones eventually removes rows whose
// synced_at is older than the retention window.
type Tombstone struct {
	UUID      string
	DeletedAt time.Time
	SyncedAt  sql.NullTime
}

// WriteTombstone records that the given uuid should be deleted on Argo.
// Safe to call inside an existing transaction via WriteTombstoneTx.
// Idempotent: re-writing an existing uuid refreshes deleted_at and clears
// synced_at (the latter forces a re-attempt — useful if the upstream row
// reappeared somehow).
func (s *Store) WriteTombstone(ctx context.Context, uuid string, deletedAt time.Time) error {
	return writeTombstone(ctx, s.db, uuid, deletedAt)
}

// writeTombstone is the shared body used by both Store.WriteTombstone (db
// handle) and the in-tx call from stitch/drop paths.
func writeTombstone(ctx context.Context, exec sqlExec, uuid string, deletedAt time.Time) error {
	_, err := exec.ExecContext(ctx, `
		INSERT INTO session_tombstones (uuid, deleted_at)
		VALUES (?, ?)
		ON CONFLICT(uuid) DO UPDATE SET
			deleted_at = excluded.deleted_at,
			synced_at = NULL`,
		uuid, formatTime(deletedAt),
	)
	if err != nil {
		return fmt.Errorf("write tombstone %s: %w", uuid, err)
	}
	return nil
}

// sqlExec is the narrow surface needed by writeTombstone — both *sql.DB and
// *sql.Tx satisfy it.
type sqlExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// UnsyncedTombstones returns the next batch of tombstones whose Argo delete
// hasn't succeeded yet. Ordered oldest-first so retries are FIFO.
func (s *Store) UnsyncedTombstones(ctx context.Context, limit int) ([]Tombstone, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT uuid, deleted_at, synced_at
		FROM session_tombstones
		WHERE synced_at IS NULL
		ORDER BY deleted_at ASC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list unsynced tombstones: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Tombstone
	for rows.Next() {
		var (
			t         Tombstone
			deletedAt string
			syncedAt  sql.NullString
		)
		if err := rows.Scan(&t.UUID, &deletedAt, &syncedAt); err != nil {
			return nil, err
		}
		t.DeletedAt, err = parseTime(deletedAt)
		if err != nil {
			return nil, err
		}
		if syncedAt.Valid {
			parsed, err := parseTime(syncedAt.String)
			if err != nil {
				return nil, err
			}
			t.SyncedAt = sql.NullTime{Time: parsed, Valid: true}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkTombstoneSynced stamps synced_at on the tombstone for uuid. Returns
// ErrNotFound if the uuid has no tombstone (which means the worker raced
// with GC — safe to ignore at the call site).
func (s *Store) MarkTombstoneSynced(ctx context.Context, uuid string, syncedAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE session_tombstones SET synced_at = ? WHERE uuid = ?`,
		formatTime(syncedAt), uuid,
	)
	if err != nil {
		return fmt.Errorf("mark tombstone synced %s: %w", uuid, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GCTombstones deletes synced tombstones whose synced_at is older than
// olderThan. Run as a periodic housekeeping pass; returns the count removed.
// Unsynced tombstones are never collected — they must drain to Argo first.
func (s *Store) GCTombstones(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM session_tombstones
		WHERE synced_at IS NOT NULL AND synced_at < ?`,
		formatTime(olderThan),
	)
	if err != nil {
		return 0, fmt.Errorf("gc tombstones: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// HasTombstone reports whether the given uuid has a tombstone row (synced or
// not). Used by tests; not part of the runtime hot path.
func (s *Store) HasTombstone(ctx context.Context, uuid string) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT uuid FROM session_tombstones WHERE uuid = ?`, uuid,
	).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
