package store

import (
	"context"
	"testing"
	"time"
)

// seedSession inserts a closed session with the given samples and a
// deliberately wrong duration_s to simulate a row written by the pre-fix
// daemon. Returns the row id.
func seedSession(t *testing.T, s *Store, uuid string, startedAt time.Time, samples []Sample, endedAt time.Time, oldDuration int64, distanceM float64) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.OpenSession(ctx, uuid, startedAt)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	for _, smp := range samples {
		smp.SessionID = id
		if _, err := s.AppendSample(ctx, smp); err != nil {
			t.Fatalf("AppendSample: %v", err)
		}
	}
	if err := s.CloseSession(ctx, id, endedAt, SessionTotals{
		DurationS: oldDuration,
		DistanceM: distanceM,
	}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	return id
}

func readSession(t *testing.T, s *Store, id int64) Session {
	t.Helper()
	var (
		sess Session
		row  = s.db.QueryRow(sessionSelect+" WHERE id = ?", id)
	)
	got, err := scanSession(row)
	if err != nil {
		t.Fatalf("scanSession: %v", err)
	}
	sess = got
	return sess
}

func TestBackfillDurations_RecomputesAndClearsSync(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// Sample sequence: 1 stopped, 5 running (one per second), 1 stopped at +6s.
	// Window-based duration = 6 - 1 = 5s (window opens at t=1, closes at t=6).
	// The "old" duration we seed is 2s (representing dt-undercounting).
	samples := []Sample{
		{Ts: base.Add(0 * time.Second), BeltState: 0},
		{Ts: base.Add(1 * time.Second), BeltState: 2, SpeedKmh: 3.0, DistanceM: 1.0, Steps: 1},
		{Ts: base.Add(2 * time.Second), BeltState: 2, SpeedKmh: 3.0, DistanceM: 2.0, Steps: 2},
		{Ts: base.Add(3 * time.Second), BeltState: 2, SpeedKmh: 3.0, DistanceM: 3.0, Steps: 3},
		{Ts: base.Add(4 * time.Second), BeltState: 2, SpeedKmh: 3.0, DistanceM: 4.0, Steps: 4},
		{Ts: base.Add(5 * time.Second), BeltState: 2, SpeedKmh: 3.0, DistanceM: 5.0, Steps: 5},
		{Ts: base.Add(6 * time.Second), BeltState: 0},
	}
	id := seedSession(t, s, "recompute", base, samples, base.Add(6*time.Second), 2, 5.0)

	// Pretend the session was synced — backfill must clear it so the next
	// upload tick re-pushes the corrected totals.
	if err := s.MarkSynced(ctx, "recompute", base.Add(10*time.Second)); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	n, err := s.BackfillDurations(ctx)
	if err != nil {
		t.Fatalf("BackfillDurations: %v", err)
	}
	if n != 1 {
		t.Fatalf("updated = %d, want 1", n)
	}

	after := readSession(t, s, id)
	if after.DurationS != 5 {
		t.Errorf("duration_s = %d, want 5", after.DurationS)
	}
	// avg = (5 m / 1000) / (5 s / 3600) = 0.005 / 0.001388… ≈ 3.6 km/h
	expAvg := (5.0 / 1000.0) / (5.0 / 3600.0)
	if diff := after.AvgSpeedKmh - expAvg; diff > 0.001 || diff < -0.001 {
		t.Errorf("avg_speed = %g, want ~%g", after.AvgSpeedKmh, expAvg)
	}
	if after.SyncedAt.Valid {
		t.Errorf("synced_at should be cleared, got %v", after.SyncedAt.Time)
	}
}

func TestBackfillDurations_IsIdempotent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	seedSession(t, s, "idempotent", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 3.0},
		{Ts: base.Add(3 * time.Second), BeltState: 0},
	}, base.Add(3*time.Second), 99, 1.0)

	first, err := s.BackfillDurations(ctx)
	if err != nil || first != 1 {
		t.Fatalf("first: n=%d err=%v", first, err)
	}

	second, err := s.BackfillDurations(ctx)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second != 0 {
		t.Errorf("second call updated = %d, want 0 (marker should short-circuit)", second)
	}
}

func TestBackfillDurations_SkipsCorrectRows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// Two samples 3 s apart, then stop. New duration = 3 s. Seed with 3 s
	// already to ensure the row is left untouched (count not bumped).
	seedSession(t, s, "already-correct", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 3.0},
		{Ts: base.Add(3 * time.Second), BeltState: 0},
	}, base.Add(3*time.Second), 3, 1.0)

	n, err := s.BackfillDurations(ctx)
	if err != nil {
		t.Fatalf("BackfillDurations: %v", err)
	}
	if n != 0 {
		t.Errorf("updated = %d, want 0 (duration unchanged)", n)
	}
}

func TestBackfillDurations_WindowClosesAtEndedAt(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// All samples are running; no closing stopped sample. The window must
	// close at ended_at = base+10s. Expected duration = 10.
	seedSession(t, s, "open-window", base, []Sample{
		{Ts: base.Add(0 * time.Second), BeltState: 2, SpeedKmh: 3.0},
		{Ts: base.Add(3 * time.Second), BeltState: 2, SpeedKmh: 3.0},
		{Ts: base.Add(7 * time.Second), BeltState: 2, SpeedKmh: 3.0},
	}, base.Add(10*time.Second), 1, 1.0)

	if _, err := s.BackfillDurations(ctx); err != nil {
		t.Fatalf("BackfillDurations: %v", err)
	}
	got := readSession(t, s, 1)
	if got.DurationS != 10 {
		t.Errorf("duration_s = %d, want 10 (window should close at ended_at)", got.DurationS)
	}
}

func TestBackfillDurations_MultipleWindows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// Run 5s, stop 10s, run 4s, stop. Window sum = 5 + 4 = 9.
	samples := []Sample{
		{Ts: base.Add(0 * time.Second), BeltState: 2, SpeedKmh: 3.0},
		{Ts: base.Add(5 * time.Second), BeltState: 0},
		{Ts: base.Add(15 * time.Second), BeltState: 2, SpeedKmh: 3.0},
		{Ts: base.Add(19 * time.Second), BeltState: 0},
	}
	seedSession(t, s, "multi-window", base, samples, base.Add(19*time.Second), 999, 1.0)

	if _, err := s.BackfillDurations(ctx); err != nil {
		t.Fatalf("BackfillDurations: %v", err)
	}
	got := readSession(t, s, 1)
	if got.DurationS != 9 {
		t.Errorf("duration_s = %d, want 9 (two windows summed)", got.DurationS)
	}
}

func TestBackfillDurations_NoClosedSessionsIsAnNoop(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	n, err := s.BackfillDurations(ctx)
	if err != nil {
		t.Fatalf("BackfillDurations: %v", err)
	}
	if n != 0 {
		t.Errorf("updated = %d, want 0", n)
	}
	// Marker should still be set so we don't re-walk on every restart.
	done, err := s.metaHas(ctx, backfillDurationKey)
	if err != nil || !done {
		t.Errorf("marker missing: done=%v err=%v", done, err)
	}
}
