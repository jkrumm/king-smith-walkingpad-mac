package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_MigratesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mig.sqlite")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	// schema_migrations must record version 1.
	var v int
	if err := s1.DB().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if v != 1 {
		t.Errorf("schema version = %d, want 1", v)
	}
	_ = s1.Close()

	// Re-opening must not re-apply.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	var count int
	if err := s2.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations rows = %d, want 1 (idempotent)", count)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	started := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)
	id, err := s.OpenSession(ctx, "uuid-1", started)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if id <= 0 {
		t.Fatalf("bad rowid %d", id)
	}

	// Append three samples one second apart.
	for i := 0; i < 3; i++ {
		_, err := s.AppendSample(ctx, Sample{
			SessionID: id,
			Ts:        started.Add(time.Duration(i) * time.Second),
			BeltState: 2,
			SpeedKmh:  3.5,
			DistanceM: float64(i) * 1.0,
			Steps:     int64(i) * 2,
			Mode:      1,
			Button:    0,
		})
		if err != nil {
			t.Fatalf("AppendSample[%d]: %v", i, err)
		}
	}

	// Open-session lookup before close should hit.
	open, err := s.MostRecentOpenSession(ctx)
	if err != nil {
		t.Fatalf("MostRecentOpenSession: %v", err)
	}
	if open == nil || open.UUID != "uuid-1" {
		t.Fatalf("expected open uuid-1, got %+v", open)
	}

	// Close with totals.
	ended := started.Add(3 * time.Second)
	totals := SessionTotals{
		DurationS:   3,
		DistanceM:   2.0,
		Steps:       4,
		AvgSpeedKmh: 3.0,
		MaxSpeedKmh: 3.5,
		Kcal:        12.5,
	}
	if err := s.CloseSession(ctx, id, ended, totals); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	// After close: no open sessions.
	open, err = s.MostRecentOpenSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if open != nil {
		t.Errorf("expected no open session, got %+v", open)
	}

	// Round-trip: fetch session + samples by uuid.
	sess, samples, err := s.GetSession(ctx, "uuid-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !sess.EndedAt.Valid {
		t.Error("ended_at must be set after close")
	}
	if sess.DistanceM != 2.0 || sess.Kcal != 12.5 || sess.MaxSpeedKmh != 3.5 {
		t.Errorf("totals mismatch: %+v", sess)
	}
	if !sess.StartedAt.Equal(started) {
		t.Errorf("started_at lost precision: %v vs %v", sess.StartedAt, started)
	}
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(samples))
	}
	for i, smp := range samples {
		want := started.Add(time.Duration(i) * time.Second)
		if !smp.Ts.Equal(want) {
			t.Errorf("samples[%d].Ts = %v, want %v", i, smp.Ts, want)
		}
	}
}

func TestGetSession_NotFound(t *testing.T) {
	s := openTest(t)
	_, _, err := s.GetSession(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestIncrementPauseCount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id, _ := s.OpenSession(ctx, "u", time.Now().UTC())
	for i := 0; i < 3; i++ {
		if err := s.IncrementPauseCount(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := s.DB().QueryRow(`SELECT pause_count FROM sessions WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("pause_count = %d, want 3", count)
	}
}

func TestSyncQueue(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	// Two closed sessions, one already synced; one open (should not appear).
	now := time.Now().UTC()
	id1, _ := s.OpenSession(ctx, "u1", now.Add(-10*time.Minute))
	_ = s.CloseSession(ctx, id1, now.Add(-9*time.Minute), SessionTotals{DurationS: 60})

	id2, _ := s.OpenSession(ctx, "u2", now.Add(-5*time.Minute))
	_ = s.CloseSession(ctx, id2, now.Add(-4*time.Minute), SessionTotals{DurationS: 60})
	_ = s.MarkSynced(ctx, "u2", now.Add(-3*time.Minute))

	_, _ = s.OpenSession(ctx, "u3-open", now)

	queue, err := s.UnsyncedSessions(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].UUID != "u1" {
		t.Errorf("queue = %+v, want [u1]", uuids(queue))
	}

	// MarkSynced on missing uuid -> ErrNotFound.
	if err := s.MarkSynced(ctx, "nope", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkSynced missing: want ErrNotFound, got %v", err)
	}
}

func TestListSessions_OrderAndCursor(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		_, err := s.OpenSession(ctx, "u"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Newest first.
	got, err := s.ListSessions(ctx, 3, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].UUID != "u4" || got[1].UUID != "u3" || got[2].UUID != "u2" {
		t.Errorf("order wrong: %v", uuids(got))
	}

	// Cursor: before u3 → u2, u1, u0.
	cursor, _, _ := s.GetSession(ctx, "u3")
	got, err = s.ListSessions(ctx, 10, cursor.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].UUID != "u2" {
		t.Errorf("cursor result: %v", uuids(got))
	}
}

func TestSummary_PeriodWindows(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	// Fixed "now" inside the test so we control which window each session falls into.
	loc, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 5, 17, 14, 0, 0, 0, loc) // Sunday afternoon

	insert := func(uuid string, startedAt time.Time, totals SessionTotals) {
		id, err := s.OpenSession(ctx, uuid, startedAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CloseSession(ctx, id, startedAt.Add(30*time.Minute), totals); err != nil {
			t.Fatal(err)
		}
	}

	// Today (Sunday 2026-05-17):
	insert("today-a", now.Add(-2*time.Hour), SessionTotals{DurationS: 1800, DistanceM: 1500, Steps: 2000, Kcal: 100})
	insert("today-b", now.Add(-30*time.Minute), SessionTotals{DurationS: 600, DistanceM: 500, Steps: 700, Kcal: 30})

	// This week but before today (Friday): ISO week starts Mon 2026-05-11.
	insert("friday", time.Date(2026, 5, 15, 9, 0, 0, 0, loc),
		SessionTotals{DurationS: 1200, DistanceM: 1000, Steps: 1500, Kcal: 60})

	// Earlier this month, before this week (Tuesday 2026-05-05).
	insert("earlier-may", time.Date(2026, 5, 5, 9, 0, 0, 0, loc),
		SessionTotals{DurationS: 1000, DistanceM: 800, Steps: 1200, Kcal: 50})

	// Before this month (April).
	insert("april", time.Date(2026, 4, 20, 9, 0, 0, 0, loc),
		SessionTotals{DurationS: 999, DistanceM: 999, Steps: 999, Kcal: 99})

	// Open (not closed) — must not count anywhere.
	_, _ = s.OpenSession(ctx, "still-open", now)

	tests := []struct {
		period   Period
		wantN    int64
		wantKcal float64
	}{
		{PeriodToday, 2, 130},
		{PeriodWeek, 3, 190},  // today-a + today-b + friday
		{PeriodMonth, 4, 240}, // adds earlier-may
		{PeriodAll, 5, 339},   // adds april
	}
	for _, tt := range tests {
		t.Run(string(tt.period), func(t *testing.T) {
			got, err := s.Summary(ctx, tt.period, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.Sessions != tt.wantN {
				t.Errorf("sessions = %d, want %d", got.Sessions, tt.wantN)
			}
			if got.Kcal != tt.wantKcal {
				t.Errorf("kcal = %v, want %v", got.Kcal, tt.wantKcal)
			}
		})
	}
}

func TestSummary_UnknownPeriodRejected(t *testing.T) {
	if _, err := openTest(t).Summary(context.Background(), Period("year"), time.Now()); err == nil {
		t.Fatal("expected error for unknown period")
	}
}

func TestAppendSample_RawHexOptional(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id, _ := s.OpenSession(ctx, "u", time.Now().UTC())

	if _, err := s.AppendSample(ctx, Sample{SessionID: id, Ts: time.Now().UTC(), BeltState: 2, SpeedKmh: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendSample(ctx, Sample{SessionID: id, Ts: time.Now().UTC(), BeltState: 2, SpeedKmh: 3, RawFrameHex: "f8a2…"}); err != nil {
		t.Fatal(err)
	}

	var nullCount, nonNullCount int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM samples WHERE raw_frame_hex IS NULL`).Scan(&nullCount)
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM samples WHERE raw_frame_hex IS NOT NULL`).Scan(&nonNullCount)
	if nullCount != 1 || nonNullCount != 1 {
		t.Errorf("raw_frame_hex storage: null=%d non-null=%d want 1/1", nullCount, nonNullCount)
	}
}

// helpers
func uuids(ss []Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.UUID
	}
	return out
}
