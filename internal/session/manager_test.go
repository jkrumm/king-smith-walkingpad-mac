package session

import (
	"context"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// --- helpers ----------------------------------------------------------------

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestManager(t *testing.T, st *store.Store) *Manager {
	t.Helper()
	return NewManager(Config{
		GapMinutes:          1, // tight bounds so tests are fast
		ResumeWithinSeconds: 10,
		WeightKg:            80.0,
	}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// frame builds a synthetic ble.Status for a given state and reading.
func frame(state ble.BeltState, speed, distance float64, steps uint32) ble.Status {
	return ble.Status{
		State:    state,
		SpeedKmh: speed,
		Mode:     ble.ModeManual,
		Distance: distance,
		Steps:    steps,
	}
}

// running is a shorthand for an ACTIVE frame.
func running(speed, distance float64, steps uint32) ble.Status {
	return frame(ble.BeltActive, speed, distance, steps)
}

// stopped is a shorthand for a STOPPED frame (device counters at 0).
func stopped() ble.Status { return frame(ble.BeltStopped, 0, 0, 0) }

// --- core lifecycle ---------------------------------------------------------

func TestManager_OpenExtendClose(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)

	// Tick 0: first running frame opens the session.
	if err := m.Ingest(ctx, running(4.0, 0, 0), base); err != nil {
		t.Fatal(err)
	}
	// 3 more running frames, 1 s apart.
	for i := 1; i <= 3; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		if err := m.Ingest(ctx, running(4.0, float64(i)*1.1, uint32(i)*2), ts); err != nil {
			t.Fatal(err)
		}
	}
	if !m.HasOpenSession() {
		t.Fatal("session should still be open mid-walk")
	}

	// Stopped frame within the gap window: keep the session open, append sample.
	if err := m.Ingest(ctx, stopped(), base.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenSession() {
		t.Fatal("must not close inside gap window")
	}

	// Stopped frame beyond the gap window: closes the session.
	if err := m.Ingest(ctx, stopped(), base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if m.HasOpenSession() {
		t.Fatal("session should be closed after gap exceeded")
	}

	// Verify persisted totals.
	sessions, err := st.ListSessions(ctx, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	got := sessions[0]
	if !got.EndedAt.Valid {
		t.Fatal("session must be closed")
	}
	// 3 deltas across running samples → 3.3 m total, 6 steps.
	if math.Abs(got.DistanceM-3.3) > 1e-9 {
		t.Errorf("distance = %g, want 3.3", got.DistanceM)
	}
	if got.Steps != 6 {
		t.Errorf("steps = %d, want 6", got.Steps)
	}
	if got.MaxSpeedKmh != 4.0 {
		t.Errorf("max_speed = %g, want 4.0", got.MaxSpeedKmh)
	}
	if got.DurationS != 3 {
		t.Errorf("duration_s = %d, want 3", got.DurationS)
	}
}

func TestManager_IgnoresFramesBeforeFirstRun(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		if err := m.Ingest(ctx, stopped(), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if m.HasOpenSession() {
		t.Fatal("must not open a session from stopped frames")
	}
	sessions, _ := st.ListSessions(ctx, 10, time.Time{})
	if len(sessions) != 0 {
		t.Fatalf("want 0 sessions, got %d", len(sessions))
	}
}

// --- counter-reset robustness ----------------------------------------------

func TestManager_CounterResetMidSession(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)

	// Pre-pause: walk 0 → 100 m.
	for i := 0; i <= 5; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		if err := m.Ingest(ctx, running(4.0, float64(i)*20.0, uint32(i)*30), ts); err != nil {
			t.Fatal(err)
		}
	}
	// Brief stop (within gap window): device counters reset to 0.
	if err := m.Ingest(ctx, stopped(), base.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Walk again 0 → 50 m. Resume within ResumeWithinSeconds → no pause bump.
	for i := 0; i <= 5; i++ {
		ts := base.Add(time.Duration(8+i) * time.Second)
		if err := m.Ingest(ctx, running(4.0, float64(i)*10.0, uint32(i)*15), ts); err != nil {
			t.Fatal(err)
		}
	}
	// Close.
	if err := m.Ingest(ctx, stopped(), base.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	sessions, _ := st.ListSessions(ctx, 10, time.Time{})
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	got := sessions[0]
	// Total distance must be 100 + 50 = 150 m — no double-count, no swallowed delta.
	if math.Abs(got.DistanceM-150.0) > 1e-9 {
		t.Errorf("distance = %g, want 150 (no double-count across reset)", got.DistanceM)
	}
	if got.Steps != 150+75 {
		t.Errorf("steps = %d, want 225", got.Steps)
	}
}

// --- pause/resume + pause_count --------------------------------------------

func TestManager_PauseBeyondGraceBumpsCount(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)

	// Walk 3 s.
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		_ = m.Ingest(ctx, running(4.0, float64(i)*1.0, uint32(i)), ts)
	}
	// Idle for 30 s — beyond ResumeWithinSeconds (10) but within GapMinutes (1).
	if err := m.Ingest(ctx, stopped(), base.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Resume walking — should bump pause_count.
	if err := m.Ingest(ctx, running(4.0, 0, 0), base.Add(35*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenSession() {
		t.Fatal("session must still be open across pause")
	}

	var pauseCount int64
	_ = st.DB().QueryRow(`SELECT pause_count FROM sessions LIMIT 1`).Scan(&pauseCount)
	if pauseCount != 1 {
		t.Errorf("pause_count = %d, want 1", pauseCount)
	}
}

func TestManager_PauseWithinGraceDoesNotBump(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)

	_ = m.Ingest(ctx, running(4.0, 0, 0), base)
	_ = m.Ingest(ctx, running(4.0, 5, 6), base.Add(1*time.Second))
	// 5-second gap is within ResumeWithinSeconds=10.
	_ = m.Ingest(ctx, running(4.0, 10, 12), base.Add(6*time.Second))

	var pauseCount int64
	_ = st.DB().QueryRow(`SELECT pause_count FROM sessions LIMIT 1`).Scan(&pauseCount)
	if pauseCount != 0 {
		t.Errorf("pause_count = %d, want 0 (gap within grace)", pauseCount)
	}
}

// --- shutdown / tick --------------------------------------------------------

func TestManager_ForceCloseUsesLastRunningTs(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)
	_ = m.Ingest(ctx, running(4.0, 0, 0), base)
	_ = m.Ingest(ctx, running(4.0, 5, 6), base.Add(1*time.Second))

	shutdownAt := base.Add(5 * time.Minute)
	if err := m.ForceClose(ctx, shutdownAt); err != nil {
		t.Fatal(err)
	}
	if m.HasOpenSession() {
		t.Fatal("ForceClose must close the session")
	}

	sessions, _ := st.ListSessions(ctx, 10, time.Time{})
	if !sessions[0].EndedAt.Valid {
		t.Fatal("ended_at must be set")
	}
	want := base.Add(1 * time.Second)
	if !sessions[0].EndedAt.Time.Equal(want) {
		t.Errorf("ended_at = %v, want lastRunningTs %v", sessions[0].EndedAt.Time, want)
	}
}

func TestManager_TickClosesIdleSession(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)
	_ = m.Ingest(ctx, running(4.0, 0, 0), base)
	_ = m.Ingest(ctx, running(4.0, 5, 6), base.Add(1*time.Second))

	// Tick before the gap → no-op.
	if err := m.Tick(ctx, base.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenSession() {
		t.Fatal("Tick within gap must not close")
	}

	// Tick after the gap → closes.
	if err := m.Tick(ctx, base.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if m.HasOpenSession() {
		t.Fatal("Tick beyond gap must close")
	}
}

// --- resume after restart ---------------------------------------------------

func TestManager_ResumeWithinAgeReplaysTotals(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)

	m1 := newTestManager(t, st)
	_ = m1.Ingest(ctx, running(4.0, 0, 0), base)
	_ = m1.Ingest(ctx, running(4.0, 10, 12), base.Add(1*time.Second))
	_ = m1.Ingest(ctx, running(5.6, 25, 30), base.Add(2*time.Second))
	// Don't close — simulate a crash.

	// Capture pre-restart totals from m1's accumulators by force-closing a
	// clone, then re-running on a fresh manager via Resume.
	captureDistance := m1.cur.totalDistanceM
	captureSteps := m1.cur.totalSteps
	captureMaxSpeed := m1.cur.maxSpeedKmh
	captureKcal := m1.cur.kcalAccum

	// New manager picks up the open session at t=2 minutes (still within 6 h).
	m2 := newTestManager(t, st)
	if err := m2.Resume(ctx, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !m2.HasOpenSession() {
		t.Fatal("Resume must re-open the in-flight session")
	}
	if m2.cur.totalDistanceM != captureDistance {
		t.Errorf("replay distance = %g, want %g", m2.cur.totalDistanceM, captureDistance)
	}
	if m2.cur.totalSteps != captureSteps {
		t.Errorf("replay steps = %d, want %d", m2.cur.totalSteps, captureSteps)
	}
	if m2.cur.maxSpeedKmh != captureMaxSpeed {
		t.Errorf("replay max_speed = %g, want %g", m2.cur.maxSpeedKmh, captureMaxSpeed)
	}
	if math.Abs(m2.cur.kcalAccum-captureKcal) > 1e-9 {
		t.Errorf("replay kcal = %g, want %g", m2.cur.kcalAccum, captureKcal)
	}
}

func TestManager_ResumeBeyondMaxAgeForceCloses(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	base := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)

	m1 := newTestManager(t, st)
	_ = m1.Ingest(ctx, running(4.0, 0, 0), base)
	_ = m1.Ingest(ctx, running(4.0, 10, 12), base.Add(1*time.Second))
	// Crash — session never closed.

	// Resume 7 h later (> resumeMaxAge of 6 h).
	m2 := newTestManager(t, st)
	if err := m2.Resume(ctx, base.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m2.HasOpenSession() {
		t.Fatal("session older than 6 h must be force-closed, not resumed")
	}

	sessions, _ := st.ListSessions(ctx, 10, time.Time{})
	if !sessions[0].EndedAt.Valid {
		t.Fatal("ended_at must be set by force-close")
	}
	// ended_at must be the last sample's ts, not "now".
	want := base.Add(1 * time.Second)
	if !sessions[0].EndedAt.Time.Equal(want) {
		t.Errorf("force-close ended_at = %v, want last sample ts %v", sessions[0].EndedAt.Time, want)
	}
}

// --- misc -------------------------------------------------------------------

func TestManager_StoppingFrameCounted(t *testing.T) {
	// CLAUDE.md gotcha #8: 0x04 STOPPING is the last-chance frame; BeltState
	// considers it running so the session manager must capture it.
	ctx := context.Background()
	st := newTestStore(t)
	m := newTestManager(t, st)

	base := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)
	_ = m.Ingest(ctx, running(4.0, 0, 0), base)
	_ = m.Ingest(ctx, frame(ble.BeltStopping, 3.5, 50, 60), base.Add(1*time.Second))
	_ = m.Ingest(ctx, stopped(), base.Add(2*time.Minute))

	sessions, _ := st.ListSessions(ctx, 10, time.Time{})
	if sessions[0].DistanceM != 50 {
		t.Errorf("STOPPING frame distance not captured: got %g, want 50", sessions[0].DistanceM)
	}
}

func TestManager_RawFramesOptional(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	mOff := NewManager(Config{GapMinutes: 1, ResumeWithinSeconds: 10, WeightKg: 80, IncludeRawFrames: false}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = mOff.Ingest(context.Background(), running(4.0, 0, 0), time.Now().UTC())

	var nullCount, nonNullCount int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM samples WHERE raw_frame_hex IS NULL`).Scan(&nullCount)
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM samples WHERE raw_frame_hex IS NOT NULL`).Scan(&nonNullCount)
	if nullCount != 1 || nonNullCount != 0 {
		t.Errorf("IncludeRawFrames=false should yield NULL hex; null=%d nonnull=%d", nullCount, nonNullCount)
	}

	st2 := newTestStore(t)
	mOn := NewManager(Config{GapMinutes: 1, ResumeWithinSeconds: 10, WeightKg: 80, IncludeRawFrames: true}, st2,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := running(4.0, 0, 0)
	f.Raw[0] = 0xF8 // arbitrary non-zero so encoded hex is non-empty
	_ = mOn.Ingest(ctx, f, time.Now().UTC())

	_ = st2.DB().QueryRow(`SELECT COUNT(*) FROM samples WHERE raw_frame_hex IS NOT NULL`).Scan(&nonNullCount)
	if nonNullCount != 1 {
		t.Errorf("IncludeRawFrames=true should populate hex; non-null=%d", nonNullCount)
	}
}
