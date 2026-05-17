// Package store owns the daemon's SQLite persistence.
//
// One Store instance fronts a single database file. The schema and the
// session/sample model live in PRD §6; the session-grouping state machine that
// feeds this store lives in internal/session.
//
// Times are persisted as ISO-8601 UTC text per the PRD; the Go-side type is
// always time.Time (with sql.NullTime for nullable columns).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Store is the public façade. Concurrent reads/writes are safe — the *sql.DB
// handle handles connection pooling and WAL serialises writers.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at path, applies pending
// migrations, and returns a ready-to-use Store. Pass ":memory:" for tests that
// don't need persistence across handles.
func Open(path string) (*Store, error) {
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// modernc.org/sqlite is goroutine-safe but a single writer outperforms many.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close flushes and releases the underlying handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for advanced callers (sync worker, tests). Prefer
// the typed methods.
func (s *Store) DB() *sql.DB { return s.db }

func buildDSN(path string) string {
	// :memory: short-circuits everything else.
	if path == ":memory:" {
		return path
	}
	// `file:` form lets us cleanly pass pragmas. Path must be absolute or
	// relative-resolvable; we leave that to the caller.
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + filepath.Clean(path) + "?" + q.Encode()
}

// --- Domain types -----------------------------------------------------------

// Session mirrors the `sessions` row. EndedAt and SyncedAt are NULL until the
// session closes / syncs.
type Session struct {
	ID          int64
	UUID        string
	StartedAt   time.Time
	EndedAt     sql.NullTime
	DurationS   int64
	DistanceM   float64
	Steps       int64
	AvgSpeedKmh float64
	MaxSpeedKmh float64
	Kcal        float64
	PauseCount  int64
	SyncedAt    sql.NullTime
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Sample mirrors the `samples` row. RawFrameHex is empty unless debug logging
// is on (PRD §6 retention note).
type Sample struct {
	ID          int64
	SessionID   int64
	Ts          time.Time
	BeltState   int
	SpeedKmh    float64
	DistanceM   float64
	Steps       int64
	Mode        int
	Button      int
	RawFrameHex string
}

// SessionTotals carries the computed final stats handed to CloseSession.
type SessionTotals struct {
	DurationS   int64
	DistanceM   float64
	Steps       int64
	AvgSpeedKmh float64
	MaxSpeedKmh float64
	Kcal        float64
}

// Summary is the aggregate returned for the /summary endpoint.
type Summary struct {
	Sessions  int64
	DurationS int64
	DistanceM float64
	Steps     int64
	Kcal      float64
}

// Period enumerates the windows the /summary endpoint supports.
type Period string

// Period values. Used as the `?period=` query string on /summary.
const (
	PeriodToday Period = "today"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
	PeriodAll   Period = "all"
)

// ErrNotFound is returned when a typed lookup misses.
var ErrNotFound = errors.New("not found")

// timeFormat is the canonical persisted shape — UTC, nanosecond precision,
// RFC3339-compatible and trivially decodable from JS/JSON.
const timeFormat = time.RFC3339Nano

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(s string) (time.Time, error) {
	// SQLite's `datetime('now')` default emits `YYYY-MM-DD HH:MM:SS` — accept
	// it too, otherwise created_at/updated_at decoding fails.
	if t, err := time.Parse(timeFormat, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// --- Sessions ---------------------------------------------------------------

// OpenSession inserts a new open session row (ended_at NULL) and returns its
// rowid. uuid is client-generated so the daemon can reference the same session
// after a restart.
func (s *Store) OpenSession(ctx context.Context, uuid string, startedAt time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (uuid, started_at) VALUES (?, ?)`,
		uuid, formatTime(startedAt),
	)
	if err != nil {
		return 0, fmt.Errorf("insert session: %w", err)
	}
	return res.LastInsertId()
}

// CloseSession writes the final totals and the ended_at timestamp. After this
// call the session is queued for Argo sync (synced_at is NULL by default).
func (s *Store) CloseSession(ctx context.Context, sessionID int64, endedAt time.Time, t SessionTotals) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET
			ended_at = ?,
			duration_s = ?,
			distance_m = ?,
			steps = ?,
			avg_speed_kmh = ?,
			max_speed_kmh = ?,
			kcal = ?,
			updated_at = ?
		WHERE id = ?`,
		formatTime(endedAt), t.DurationS, t.DistanceM, t.Steps,
		t.AvgSpeedKmh, t.MaxSpeedKmh, t.Kcal,
		formatTime(time.Now()), sessionID,
	)
	if err != nil {
		return fmt.Errorf("close session %d: %w", sessionID, err)
	}
	return nil
}

// IncrementPauseCount bumps pause_count on resume (PRD §7).
func (s *Store) IncrementPauseCount(ctx context.Context, sessionID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET pause_count = pause_count + 1, updated_at = ? WHERE id = ?`,
		formatTime(time.Now()), sessionID,
	)
	if err != nil {
		return fmt.Errorf("increment pause: %w", err)
	}
	return nil
}

// MostRecentOpenSession finds the newest session with NULL ended_at. The
// session manager uses this to decide whether to resume on daemon restart.
// Returns (nil, nil) if no open session exists.
func (s *Store) MostRecentOpenSession(ctx context.Context) (*Session, error) {
	row := s.db.QueryRowContext(ctx, sessionSelect+" WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT 1")
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &sess, nil
}

// GetSession returns the session and its samples in insertion order.
func (s *Store) GetSession(ctx context.Context, uuid string) (Session, []Sample, error) {
	row := s.db.QueryRowContext(ctx, sessionSelect+" WHERE uuid = ?", uuid)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, nil, ErrNotFound
		}
		return Session{}, nil, err
	}
	samples, err := s.samplesFor(ctx, sess.ID)
	if err != nil {
		return Session{}, nil, err
	}
	return sess, samples, nil
}

// ListSessions returns up to limit sessions ordered newest-first. If before is
// non-zero, only sessions with started_at < before are returned (cursor pagination).
func (s *Store) ListSessions(ctx context.Context, limit int, before time.Time) ([]Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := sessionSelect + " ORDER BY started_at DESC LIMIT ?"
	args := []any{limit}
	if !before.IsZero() {
		q = sessionSelect + " WHERE started_at < ? ORDER BY started_at DESC LIMIT ?"
		args = []any{formatTime(before), limit}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
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

// UnsyncedSessions returns closed sessions awaiting Argo upload.
func (s *Store) UnsyncedSessions(ctx context.Context, limit int) ([]Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		sessionSelect+" WHERE synced_at IS NULL AND ended_at IS NOT NULL ORDER BY started_at ASC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("unsynced: %w", err)
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

// MarkSynced stamps synced_at on a session by UUID. Idempotent — calling twice
// silently overwrites with the later timestamp.
func (s *Store) MarkSynced(ctx context.Context, uuid string, syncedAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET synced_at = ?, updated_at = ? WHERE uuid = ?`,
		formatTime(syncedAt), formatTime(time.Now()), uuid,
	)
	if err != nil {
		return fmt.Errorf("mark synced: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Samples ----------------------------------------------------------------

// AppendSample writes one telemetry tick. The caller (session manager) decides
// whether to include rawFrameHex — debug builds populate it; prod leaves it empty.
func (s *Store) AppendSample(ctx context.Context, smp Sample) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO samples
			(session_id, ts, belt_state, speed_kmh, distance_m, steps, mode, button, raw_frame_hex)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		smp.SessionID, formatTime(smp.Ts), smp.BeltState, smp.SpeedKmh,
		smp.DistanceM, smp.Steps, smp.Mode, smp.Button,
		nullableString(smp.RawFrameHex),
	)
	if err != nil {
		return 0, fmt.Errorf("insert sample: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) samplesFor(ctx context.Context, sessionID int64) ([]Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, ts, belt_state, speed_kmh, distance_m, steps,
		       mode, button, COALESCE(raw_frame_hex, '')
		FROM samples WHERE session_id = ? ORDER BY ts ASC, id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("samples for %d: %w", sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Sample
	for rows.Next() {
		var smp Sample
		var tsStr string
		if err := rows.Scan(&smp.ID, &smp.SessionID, &tsStr, &smp.BeltState,
			&smp.SpeedKmh, &smp.DistanceM, &smp.Steps, &smp.Mode, &smp.Button,
			&smp.RawFrameHex); err != nil {
			return nil, err
		}
		smp.Ts, err = parseTime(tsStr)
		if err != nil {
			return nil, err
		}
		out = append(out, smp)
	}
	return out, rows.Err()
}

// --- Aggregations -----------------------------------------------------------

// Summary computes totals over the given window. "today/week/month" use local
// calendar boundaries computed against now; "all" omits the lower bound.
func (s *Store) Summary(ctx context.Context, period Period, now time.Time) (Summary, error) {
	since, ok := periodSince(period, now)
	if !ok {
		return Summary{}, fmt.Errorf("unknown period %q", period)
	}

	q := `
		SELECT COUNT(*),
		       COALESCE(SUM(duration_s), 0),
		       COALESCE(SUM(distance_m), 0),
		       COALESCE(SUM(steps), 0),
		       COALESCE(SUM(kcal), 0)
		FROM sessions
		WHERE ended_at IS NOT NULL`
	args := []any{}
	if !since.IsZero() {
		q += ` AND started_at >= ?`
		args = append(args, formatTime(since))
	}

	var sum Summary
	err := s.db.QueryRowContext(ctx, q, args...).Scan(
		&sum.Sessions, &sum.DurationS, &sum.DistanceM, &sum.Steps, &sum.Kcal,
	)
	if err != nil {
		return Summary{}, fmt.Errorf("summary: %w", err)
	}
	return sum, nil
}

// periodSince returns the inclusive lower bound for the given period in the
// reference time's location. PeriodAll returns the zero time (no bound).
func periodSince(p Period, now time.Time) (time.Time, bool) {
	loc := now.Location()
	switch p {
	case PeriodToday:
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc), true
	case PeriodWeek:
		// ISO week: Monday is the boundary.
		y, m, d := now.Date()
		midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
		offset := (int(midnight.Weekday()) + 6) % 7 // Mon=0 … Sun=6
		return midnight.AddDate(0, 0, -offset), true
	case PeriodMonth:
		y, m, _ := now.Date()
		return time.Date(y, m, 1, 0, 0, 0, 0, loc), true
	case PeriodAll:
		return time.Time{}, true
	default:
		return time.Time{}, false
	}
}

// --- Internals --------------------------------------------------------------

// sessionSelect is the canonical column list — kept in one place so the
// scanSession routine stays in lockstep.
const sessionSelect = `
	SELECT id, uuid, started_at, ended_at, duration_s, distance_m, steps,
	       avg_speed_kmh, max_speed_kmh, kcal, pause_count, synced_at,
	       created_at, updated_at
	FROM sessions`

// scanner unifies *sql.Row and *sql.Rows for scanSession.
type scanner interface {
	Scan(dest ...any) error
}

func scanSession(r scanner) (Session, error) {
	var (
		sess       Session
		startedStr string
		endedStr   sql.NullString
		syncedStr  sql.NullString
		createdStr string
		updatedStr string
	)
	if err := r.Scan(
		&sess.ID, &sess.UUID, &startedStr, &endedStr, &sess.DurationS,
		&sess.DistanceM, &sess.Steps, &sess.AvgSpeedKmh, &sess.MaxSpeedKmh,
		&sess.Kcal, &sess.PauseCount, &syncedStr, &createdStr, &updatedStr,
	); err != nil {
		return Session{}, err
	}
	var err error
	if sess.StartedAt, err = parseTime(startedStr); err != nil {
		return Session{}, err
	}
	if endedStr.Valid {
		t, err := parseTime(endedStr.String)
		if err != nil {
			return Session{}, err
		}
		sess.EndedAt = sql.NullTime{Time: t, Valid: true}
	}
	if syncedStr.Valid {
		t, err := parseTime(syncedStr.String)
		if err != nil {
			return Session{}, err
		}
		sess.SyncedAt = sql.NullTime{Time: t, Valid: true}
	}
	if sess.CreatedAt, err = parseTime(createdStr); err != nil {
		return Session{}, err
	}
	if sess.UpdatedAt, err = parseTime(updatedStr); err != nil {
		return Session{}, err
	}
	return sess, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
