package store

import (
	"context"
	"fmt"
	"time"
)

// stitchAdjacentKey is the daemon_meta marker. Bump the suffix to force a
// re-run after a future logic change.
const stitchAdjacentKey = "stitch_adjacent_v1"

// StitchAdjacentSessions is a one-shot historical cleanup that merges
// adjacent closed sessions whose gap (B.started_at - A.ended_at) falls
// inside the given window. It applies the same "same walk?" rule the live
// manager uses for resurrection (session.ResurrectionWindow), so the
// historical data ends up consistent with how new sessions are now grouped.
//
// For each merge (A is the earlier survivor, B is absorbed):
//   - B's samples are re-parented onto A.
//   - A's totals are recomputed via the window-based replay (same code path
//     as BackfillDurations) so the merged duration_s and avg_speed_kmh are
//     correct rather than a sum of two stale halves.
//   - A.distance_m / steps / kcal become the simple sums (these aggregates
//     don't care about gap math).
//   - A.max_speed_kmh = max(A, B).
//   - A.ended_at = B.ended_at, A.pause_count += B.pause_count + 1
//     (the bridged gap itself counts as one pause).
//   - A.synced_at is cleared so Argo gets the merged row on the next tick.
//   - B is deleted from the local DB.
//
// Idempotent: a row in daemon_meta records that the cleanup ran. To re-run
// after a future logic change, bump stitchAdjacentKey.
//
// Argo upstream: each merged-away UUID gets a session_tombstones row in
// the same tx as the local DELETE. The sync worker drains tombstones via
// DELETE /walking-pad/sessions/:uuid so Argo ends up consistent with the
// local truth.
//
// Returns the number of sessions merged-away (i.e. deleted).
func (s *Store) StitchAdjacentSessions(ctx context.Context, window time.Duration) (int, error) {
	if done, err := s.metaHas(ctx, stitchAdjacentKey); err != nil {
		return 0, err
	} else if done {
		return 0, nil
	}

	closed, err := s.loadClosedSessionsAsc(ctx)
	if err != nil {
		return 0, err
	}
	if len(closed) < 2 {
		// Nothing to stitch but still mark the run so we don't re-walk.
		return 0, s.metaSet(ctx, stitchAdjacentKey, formatTime(time.Now().UTC()))
	}

	merged := 0
	head := closed[0]
	for i := 1; i < len(closed); i++ {
		next := closed[i]
		gap := next.StartedAt.Sub(head.EndedAt.Time)
		if gap >= window {
			head = next
			continue
		}
		if err := s.mergeInto(ctx, head, next); err != nil {
			return merged, fmt.Errorf("merge %s into %s: %w", next.UUID, head.UUID, err)
		}
		// Reload head so subsequent merges see the updated ended_at, totals,
		// and pause_count.
		refreshed, err := s.sessionByID(ctx, head.ID)
		if err != nil {
			return merged, fmt.Errorf("reload head %s: %w", head.UUID, err)
		}
		head = refreshed
		merged++
	}

	if err := s.metaSet(ctx, stitchAdjacentKey, formatTime(time.Now().UTC())); err != nil {
		return merged, err
	}
	return merged, nil
}

// mergeInto absorbs `b` into `a`: re-parents samples, recomputes a's totals
// from the merged sample set, deletes b. Caller must have already verified
// the temporal gap.
func (s *Store) mergeInto(ctx context.Context, a, b Session) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	if _, err := tx.ExecContext(ctx,
		`UPDATE samples SET session_id = ? WHERE session_id = ?`, a.ID, b.ID); err != nil {
		rollback()
		return fmt.Errorf("reparent samples: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, b.ID); err != nil {
		rollback()
		return fmt.Errorf("delete b: %w", err)
	}
	// Queue B for upstream delete on Argo. Only meaningful when B was already
	// synced (otherwise Argo never saw it), but writing unconditionally keeps
	// the contract simple: every local delete leaves a tombstone, the worker
	// gets a 200/deleted=false back when the upstream row never existed.
	if err := writeTombstone(ctx, tx, b.UUID, time.Now().UTC()); err != nil {
		rollback()
		return err
	}

	// Aggregate totals on a. Distance/steps/kcal are simple sums; max_speed
	// is the per-pair max; pause_count gets B's contribution plus one for the
	// bridged gap itself.
	newDistance := a.DistanceM + b.DistanceM
	newSteps := a.Steps + b.Steps
	newKcal := a.Kcal + b.Kcal
	newMax := a.MaxSpeedKmh
	if b.MaxSpeedKmh > newMax {
		newMax = b.MaxSpeedKmh
	}
	newEnded := b.EndedAt.Time
	newPauseCount := a.PauseCount + b.PauseCount + 1

	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET
			ended_at = ?,
			distance_m = ?,
			steps = ?,
			max_speed_kmh = ?,
			kcal = ?,
			pause_count = ?,
			synced_at = NULL,
			updated_at = ?
		WHERE id = ?`,
		formatTime(newEnded), newDistance, newSteps, newMax, newKcal,
		newPauseCount, formatTime(time.Now().UTC()), a.ID,
	); err != nil {
		rollback()
		return fmt.Errorf("update a aggregates: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Recompute duration_s + avg_speed_kmh from the merged sample set using
	// the same window logic as BackfillDurations. Runs outside the tx — it
	// reads samples (already on a) and updates a's two fields. Safe because
	// no concurrent writer touches a closed session.
	dur, err := s.recomputeOne(ctx, a.ID, newEnded)
	if err != nil {
		return fmt.Errorf("recompute duration for %s: %w", a.UUID, err)
	}
	avg := 0.0
	if dur > 0 {
		avg = (newDistance / 1000.0) / (float64(dur) / 3600.0)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET duration_s = ?, avg_speed_kmh = ?, updated_at = ?
		WHERE id = ?`,
		dur, avg, formatTime(time.Now().UTC()), a.ID,
	); err != nil {
		return fmt.Errorf("update duration: %w", err)
	}
	return nil
}

// loadClosedSessionsAsc returns every closed session ordered by started_at
// ascending — the order in which the stitch pass needs them.
func (s *Store) loadClosedSessionsAsc(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		sessionSelect+" WHERE ended_at IS NOT NULL ORDER BY started_at ASC")
	if err != nil {
		return nil, fmt.Errorf("list closed sessions asc: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) sessionByID(ctx context.Context, id int64) (Session, error) {
	row := s.db.QueryRowContext(ctx, sessionSelect+" WHERE id = ?", id)
	return scanSession(row)
}
