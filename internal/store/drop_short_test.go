package store

import (
	"context"
	"testing"
	"time"
)

func TestDropShortStandaloneSessions_DropsShortAndEligibleOnly(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	now := base.Add(2 * time.Hour)
	window := 30 * time.Minute
	minDur := 5 * time.Minute

	// Eligible: short (60 s) AND ended_at < now - window (>30 min old).
	seedClosed(t, s, "drop-me", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(60 * time.Second), BeltState: 0},
	}, base.Add(60*time.Second), SessionTotals{DurationS: 60, DistanceM: 70})

	// Not eligible: short but ended recently (still inside resurrection window).
	recent := now.Add(-10 * time.Minute)
	seedClosed(t, s, "still-resurrectable", recent.Add(-60*time.Second), []Sample{
		{Ts: recent.Add(-60 * time.Second), BeltState: 2, SpeedKmh: 4.0},
		{Ts: recent, BeltState: 0},
	}, recent, SessionTotals{DurationS: 60, DistanceM: 70})

	// Not eligible: long enough (10 min) even though ended_at is old.
	seedClosed(t, s, "long-walk", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(10 * time.Minute), BeltState: 0},
	}, base.Add(10*time.Minute), SessionTotals{DurationS: 600, DistanceM: 800})

	dropped, err := s.DropShortStandaloneSessions(ctx, minDur, window, now)
	if err != nil {
		t.Fatalf("DropShortStandaloneSessions: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "drop-me" {
		t.Errorf("dropped = %v, want [drop-me]", dropped)
	}

	sessions, _ := s.ListSessions(ctx, 10, time.Time{})
	if len(sessions) != 2 {
		t.Errorf("survivors = %d, want 2 (long-walk + still-resurrectable)", len(sessions))
	}

	// Tombstone was written for the dropped session.
	has, err := s.HasTombstone(ctx, "drop-me")
	if err != nil {
		t.Fatalf("HasTombstone: %v", err)
	}
	if !has {
		t.Error("dropped session must leave a tombstone")
	}
}

func TestDropShortStandaloneSessions_DeletesSamplesToo(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	now := base.Add(2 * time.Hour)

	id := seedClosed(t, s, "with-samples", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(10 * time.Second), BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(20 * time.Second), BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(30 * time.Second), BeltState: 0},
	}, base.Add(30*time.Second), SessionTotals{DurationS: 30, DistanceM: 30})

	if _, err := s.DropShortStandaloneSessions(ctx, 5*time.Minute, 30*time.Minute, now); err != nil {
		t.Fatalf("DropShortStandaloneSessions: %v", err)
	}

	var sampleCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM samples WHERE session_id = ?`, id).Scan(&sampleCount); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if sampleCount != 0 {
		t.Errorf("samples not cleaned up: %d remain", sampleCount)
	}
}

func TestDropShortStandaloneSessions_MinDurZeroIsNoop(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	now := base.Add(2 * time.Hour)

	seedClosed(t, s, "tiny", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(10 * time.Second), BeltState: 0},
	}, base.Add(10*time.Second), SessionTotals{DurationS: 10, DistanceM: 10})

	dropped, err := s.DropShortStandaloneSessions(ctx, 0, 30*time.Minute, now)
	if err != nil {
		t.Fatalf("DropShortStandaloneSessions: %v", err)
	}
	if len(dropped) != 0 {
		t.Errorf("minDur=0 must be a no-op, got %d dropped", len(dropped))
	}
}

func TestDropShortStandaloneSessions_NoEligibleNoTombstones(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	now := base.Add(2 * time.Hour)

	// All sessions long enough — nothing dropped.
	seedClosed(t, s, "long", base, []Sample{
		{Ts: base, BeltState: 2, SpeedKmh: 4.0},
		{Ts: base.Add(10 * time.Minute), BeltState: 0},
	}, base.Add(10*time.Minute), SessionTotals{DurationS: 600, DistanceM: 800})

	dropped, err := s.DropShortStandaloneSessions(ctx, 5*time.Minute, 30*time.Minute, now)
	if err != nil {
		t.Fatalf("DropShortStandaloneSessions: %v", err)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped = %d, want 0", len(dropped))
	}
	pending, _ := s.UnsyncedTombstones(ctx, 10)
	if len(pending) != 0 {
		t.Errorf("no-drop run wrote tombstones: %v", pending)
	}
}
