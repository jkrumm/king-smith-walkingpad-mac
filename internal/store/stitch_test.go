package store

import (
	"context"
	"testing"
	"time"
)

// seedClosed inserts a closed session with the given samples. Totals are
// assigned literally (no recompute) so tests can pre-stage the "pre-fix"
// numbers they want to see fixed up.
func seedClosed(t *testing.T, s *Store, uuid string, started time.Time, samples []Sample, ended time.Time, totals SessionTotals) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.OpenSession(ctx, uuid, started)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	for _, smp := range samples {
		smp.SessionID = id
		if _, err := s.AppendSample(ctx, smp); err != nil {
			t.Fatalf("AppendSample: %v", err)
		}
	}
	if err := s.CloseSession(ctx, id, ended, totals); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	return id
}

func TestStitchAdjacentSessions_MergesPair(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// Session A: 0-30 s walking, ended at +30 s.
	aSamples := []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0, DistanceM: 0},
		{Ts: base.Add(30 * time.Second), BeltState: 2, SpeedKmh: 4.0, DistanceM: 30},
		{Ts: base.Add(31 * time.Second), BeltState: 0},
	}
	aID := seedClosed(t, s, "session-a", base, aSamples, base.Add(30*time.Second),
		SessionTotals{DurationS: 30, DistanceM: 30, Steps: 40, MaxSpeedKmh: 4.0, Kcal: 5.0, AvgSpeedKmh: 3.6})

	// Session B: starts 90 s after A ended (well inside the 30-min window),
	// walks 60 s, ended at +210 s. This is the kind of split a `make up`
	// restart used to create.
	bStart := base.Add(120 * time.Second)
	bSamples := []Sample{
		{Ts: bStart, BeltState: 2, SpeedKmh: 4.5, DistanceM: 0},
		{Ts: bStart.Add(60 * time.Second), BeltState: 2, SpeedKmh: 4.5, DistanceM: 75},
		{Ts: bStart.Add(61 * time.Second), BeltState: 0},
	}
	_ = seedClosed(t, s, "session-b", bStart, bSamples, bStart.Add(60*time.Second),
		SessionTotals{DurationS: 60, DistanceM: 75, Steps: 90, MaxSpeedKmh: 4.5, Kcal: 9.0, AvgSpeedKmh: 4.5})

	// Pretend A was synced — stitch must clear synced_at so Argo gets the merged row.
	if err := s.MarkSynced(ctx, "session-a", base.Add(40*time.Second)); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	merged, err := s.StitchAdjacentSessions(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("StitchAdjacentSessions: %v", err)
	}
	if merged != 1 {
		t.Fatalf("merged = %d, want 1", merged)
	}

	// Only A remains.
	sessions, err := s.ListSessions(ctx, 10, time.Time{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 surviving session, got %d", len(sessions))
	}
	got := sessions[0]
	if got.UUID != "session-a" {
		t.Errorf("survivor UUID = %s, want session-a (earlier wins)", got.UUID)
	}

	// Aggregates: distance 30+75=105, steps 40+90=130, max(4.0, 4.5)=4.5, kcal 14, pause_count +1.
	if got.DistanceM != 105 {
		t.Errorf("distance = %g, want 105", got.DistanceM)
	}
	if got.Steps != 130 {
		t.Errorf("steps = %d, want 130", got.Steps)
	}
	if got.MaxSpeedKmh != 4.5 {
		t.Errorf("max_speed = %g, want 4.5", got.MaxSpeedKmh)
	}
	if got.Kcal != 14.0 {
		t.Errorf("kcal = %g, want 14", got.Kcal)
	}
	if got.PauseCount != 1 {
		t.Errorf("pause_count = %d, want 1 (bridged gap)", got.PauseCount)
	}
	if got.EndedAt.Time != bStart.Add(60*time.Second) {
		t.Errorf("ended_at = %v, want B's ended_at %v", got.EndedAt.Time, bStart.Add(60*time.Second))
	}
	if got.SyncedAt.Valid {
		t.Errorf("synced_at must be cleared, got %v", got.SyncedAt.Time)
	}

	// Duration: window logic across all samples. A's burst = 30 s (open at 0,
	// close at 31 s — but the closing stopped frame at 31 s caps the window),
	// B's burst = 60 s. Total = 90 s.
	// Actually: A's window opens at sample 0 (BeltState 2), closes at sample
	// at 31 s (BeltState 0). That's 31 s. B's window opens at bStart (120 s)
	// closes at bStart+61 s (181 s). That's 61 s. Sum = 92 s.
	if got.DurationS != 92 {
		t.Errorf("duration_s = %d, want 92 (31+61 from window replay)", got.DurationS)
	}
	// avg_speed = (0.105 km) / (92/3600 h) ≈ 4.11
	wantAvg := (105.0 / 1000.0) / (92.0 / 3600.0)
	if diff := got.AvgSpeedKmh - wantAvg; diff > 0.01 || diff < -0.01 {
		t.Errorf("avg_speed = %g, want ~%g", got.AvgSpeedKmh, wantAvg)
	}

	// Samples re-parented onto A.
	_, samples, err := s.GetSession(ctx, "session-a")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(samples) != 6 {
		t.Errorf("samples on survivor = %d, want 6 (3+3)", len(samples))
	}
	for _, smp := range samples {
		if smp.SessionID != aID {
			t.Errorf("orphan sample on session %d, want %d", smp.SessionID, aID)
		}
	}
}

func TestStitchAdjacentSessions_LeavesDistantPairsAlone(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	seedClosed(t, s, "morning", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(60 * time.Second), BeltState: 0},
	}, base.Add(60*time.Second), SessionTotals{DurationS: 60, DistanceM: 70})

	// 2 hours later — well outside any reasonable resurrection window.
	later := base.Add(2 * time.Hour)
	seedClosed(t, s, "evening", later, []Sample{
		{Ts: later, BeltState: 2, SpeedKmh: 4.0},
		{Ts: later.Add(60 * time.Second), BeltState: 0},
	}, later.Add(60*time.Second), SessionTotals{DurationS: 60, DistanceM: 70})

	merged, err := s.StitchAdjacentSessions(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("StitchAdjacentSessions: %v", err)
	}
	if merged != 0 {
		t.Errorf("merged = %d, want 0 (gap > window)", merged)
	}
	sessions, _ := s.ListSessions(ctx, 10, time.Time{})
	if len(sessions) != 2 {
		t.Errorf("want 2 sessions, got %d", len(sessions))
	}
}

func TestStitchAdjacentSessions_MergesChainOfThree(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// Three sessions, each separated by 5 minutes. With a 30-min window all
	// three collapse into one.
	for i, label := range []string{"a", "b", "c"} {
		start := base.Add(time.Duration(i) * 10 * time.Minute)
		seedClosed(t, s, "chain-"+label, start, []Sample{
			{Ts: start, BeltState: 2, SpeedKmh: 4.0},
			{Ts: start.Add(60 * time.Second), BeltState: 0},
		}, start.Add(60*time.Second),
			SessionTotals{DurationS: 60, DistanceM: 70, Steps: 80, MaxSpeedKmh: 4.0, Kcal: 5.0})
	}

	merged, err := s.StitchAdjacentSessions(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("StitchAdjacentSessions: %v", err)
	}
	if merged != 2 {
		t.Errorf("merged = %d, want 2 (b and c absorbed into a)", merged)
	}
	sessions, _ := s.ListSessions(ctx, 10, time.Time{})
	if len(sessions) != 1 {
		t.Fatalf("want 1 surviving session, got %d", len(sessions))
	}
	got := sessions[0]
	if got.UUID != "chain-a" {
		t.Errorf("survivor = %s, want chain-a", got.UUID)
	}
	if got.PauseCount != 2 {
		t.Errorf("pause_count = %d, want 2 (two bridged gaps)", got.PauseCount)
	}
	if got.DistanceM != 210 {
		t.Errorf("distance = %g, want 210 (3 × 70)", got.DistanceM)
	}
}

func TestStitchAdjacentSessions_IsIdempotent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	seedClosed(t, s, "x", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(60 * time.Second), BeltState: 0},
	}, base.Add(60*time.Second), SessionTotals{DurationS: 60, DistanceM: 70})
	seedClosed(t, s, "y", base.Add(5*time.Minute), []Sample{
		{Ts: base.Add(5 * time.Minute), BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(5*time.Minute + 60*time.Second), BeltState: 0},
	}, base.Add(5*time.Minute+60*time.Second), SessionTotals{DurationS: 60, DistanceM: 70})

	first, _ := s.StitchAdjacentSessions(ctx, 30*time.Minute)
	if first != 1 {
		t.Fatalf("first run merged %d, want 1", first)
	}
	second, err := s.StitchAdjacentSessions(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second != 0 {
		t.Errorf("second run merged %d, want 0 (marker should short-circuit)", second)
	}
}

func TestStitchAdjacentSessions_NoSessionsIsAnNoop(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	n, err := s.StitchAdjacentSessions(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("StitchAdjacentSessions: %v", err)
	}
	if n != 0 {
		t.Errorf("merged = %d, want 0", n)
	}
	done, err := s.metaHas(ctx, stitchAdjacentKey)
	if err != nil || !done {
		t.Errorf("marker missing: done=%v err=%v", done, err)
	}
}
