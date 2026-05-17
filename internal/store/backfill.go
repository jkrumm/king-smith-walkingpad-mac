package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// backfillDurationKey is the daemon_meta marker. Bumping the suffix forces
// a re-run on the next startup (after a logic change).
const backfillDurationKey = "duration_backfill_v1"

// BackfillDurations recomputes duration_s and avg_speed_kmh for every closed
// session using the window-based replay (open window on a running sample,
// close at the next non-running sample or ended_at). Sessions written before
// commit 8d2ab19 used a dt-between-frames accumulator that silently lost the
// seconds straddling brief stops and slow BLE polling — a 27-min walk could
// show up as 10 min.
//
// Idempotent: a row in daemon_meta records that the backfill ran. To re-run
// after a future logic change, bump backfillDurationKey.
//
// Sessions whose duration changes get synced_at cleared so the next Argo
// upload tick re-pushes the corrected totals (the endpoint is an idempotent
// upsert, so this is safe even if the original row hasn't drifted in Argo).
//
// Returns the number of session rows whose duration_s changed.
func (s *Store) BackfillDurations(ctx context.Context) (int, error) {
	if done, err := s.metaHas(ctx, backfillDurationKey); err != nil {
		return 0, err
	} else if done {
		return 0, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ended_at, distance_m, duration_s
		FROM sessions
		WHERE ended_at IS NOT NULL
		ORDER BY id`,
	)
	if err != nil {
		return 0, fmt.Errorf("list closed sessions: %w", err)
	}
	type closedSession struct {
		id          int64
		endedAt     time.Time
		distanceM   float64
		oldDuration int64
	}
	var sessions []closedSession
	for rows.Next() {
		var (
			id          int64
			endedStr    string
			distanceM   float64
			oldDuration int64
		)
		if err := rows.Scan(&id, &endedStr, &distanceM, &oldDuration); err != nil {
			_ = rows.Close()
			return 0, err
		}
		endedAt, err := parseTime(endedStr)
		if err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("parse ended_at for session %d: %w", id, err)
		}
		sessions = append(sessions, closedSession{id, endedAt, distanceM, oldDuration})
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	updated := 0
	for _, cs := range sessions {
		newDuration, err := s.recomputeOne(ctx, cs.id, cs.endedAt)
		if err != nil {
			return updated, fmt.Errorf("recompute session %d: %w", cs.id, err)
		}
		if newDuration == cs.oldDuration {
			continue
		}
		avg := 0.0
		if newDuration > 0 {
			avg = (cs.distanceM / 1000.0) / (float64(newDuration) / 3600.0)
		}
		_, err = s.db.ExecContext(ctx, `
			UPDATE sessions
			SET duration_s = ?,
			    avg_speed_kmh = ?,
			    synced_at = NULL,
			    updated_at = ?
			WHERE id = ?`,
			newDuration, avg, formatTime(time.Now().UTC()), cs.id,
		)
		if err != nil {
			return updated, fmt.Errorf("update session %d: %w", cs.id, err)
		}
		updated++
	}

	if err := s.metaSet(ctx, backfillDurationKey, formatTime(time.Now().UTC())); err != nil {
		return updated, err
	}
	return updated, nil
}

// recomputeOne replays one session's samples through the window logic.
// Belt-state byte 2 is ACTIVE and 4 is STOPPING — both count as running per
// ble.BeltState.IsRunning(). Kept inline here to avoid an import cycle.
func (s *Store) recomputeOne(ctx context.Context, sessionID int64, endedAt time.Time) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, belt_state
		FROM samples
		WHERE session_id = ?
		ORDER BY ts ASC, id ASC`,
		sessionID,
	)
	if err != nil {
		return 0, fmt.Errorf("samples: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		closedActiveS float64
		windowStart   time.Time
	)
	for rows.Next() {
		var (
			tsStr     string
			beltState int
		)
		if err := rows.Scan(&tsStr, &beltState); err != nil {
			return 0, err
		}
		ts, err := parseTime(tsStr)
		if err != nil {
			return 0, fmt.Errorf("parse sample ts: %w", err)
		}
		isRunning := beltState == 2 || beltState == 4 // BeltActive | BeltStopping
		if isRunning {
			if windowStart.IsZero() {
				windowStart = ts
			}
			continue
		}
		if !windowStart.IsZero() {
			closedActiveS += ts.Sub(windowStart).Seconds()
			windowStart = time.Time{}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Window still open at the last sample → close at the session's ended_at.
	if !windowStart.IsZero() {
		closedActiveS += endedAt.Sub(windowStart).Seconds()
	}
	if closedActiveS < 0 {
		closedActiveS = 0
	}
	return int64(closedActiveS), nil
}

// metaHas reports whether daemon_meta carries the given key.
func (s *Store) metaHas(ctx context.Context, key string) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM daemon_meta WHERE key = ?`, key).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("meta lookup %s: %w", key, err)
	}
	return true, nil
}

// metaSet writes (or overwrites) a daemon_meta entry.
func (s *Store) metaSet(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO daemon_meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("meta set %s: %w", key, err)
	}
	return nil
}
