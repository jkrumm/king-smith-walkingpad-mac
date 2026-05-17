// Package session owns the session-grouping state machine that turns a stream
// of BLE status frames into persisted sessions.
//
// The rules live in PRD §7 and the gotchas it references (state-byte semantics
// per CLAUDE.md #8/#9: counters reset on every STOP, not only on STANDBY;
// BeltState.IsRunning() intentionally returns true for both ACTIVE and
// STOPPING so the final decel frame is captured).
//
// The Manager is the sole writer of session/sample rows during normal
// operation. Every public method takes the Manager lock so external callers
// can safely call Ingest, Tick, Resume, and ForceClose from different goroutines.
package session

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// resumeMaxAge bounds Resume(). A daemon restart with an open session older
// than this gets force-closed rather than re-opened — the user has almost
// certainly stopped walking but we crashed before observing the close.
const resumeMaxAge = 6 * time.Hour

// runningGapMaxSeconds caps how much wall-time can elapse between two
// "running" samples before we stop counting the gap toward active duration.
// With 1 Hz polling, samples arrive ~1 s apart; 5 s tolerates a small burst of
// dropped frames without bleeding pause time into the active total.
const runningGapMaxSeconds = 5.0

// Config configures a Manager. All fields are required.
type Config struct {
	GapMinutes          int
	ResumeWithinSeconds int
	WeightKg            float64
	IncludeRawFrames    bool
}

// Manager applies the PRD §7 rules. Exactly one Manager fronts the daemon's
// in-memory session state.
type Manager struct {
	cfg   Config
	store *store.Store
	log   *slog.Logger

	mu  sync.Mutex
	cur *runState
}

// runState holds the in-memory accumulators for the currently open session.
// It is rebuilt by Resume() from persisted samples after a daemon restart.
type runState struct {
	sessionID int64
	uuid      string
	startedAt time.Time

	// Cumulative totals, computed from per-frame deltas.
	totalDistanceM float64
	totalSteps     int64
	maxSpeedKmh    float64
	activeSeconds  float64
	kcalAccum      float64

	// Per-tick state for the delta calculation.
	lastRunningTs   time.Time // last wall time at which the belt was running
	lastDevDistance float64   // device-reported distance at lastRunningTs
	lastDevSteps    uint32    // device-reported steps at lastRunningTs
}

// NewManager wires a Manager. The logger is required; pass slog.Default() in tests.
func NewManager(cfg Config, st *store.Store, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{cfg: cfg, store: st, log: log}
}

// HasOpenSession reports whether the manager currently owns an open session.
// Read-side helper for HTTP handlers and tests.
func (m *Manager) HasOpenSession() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur != nil
}

// CurrentSessionView is a read-only snapshot of the in-flight session, taken
// under the Manager lock. Returns nil when no session is open. The HTTP
// /status handler is the primary consumer.
type CurrentSessionView struct {
	UUID        string
	StartedAt   time.Time
	DurationS   int64
	DistanceM   float64
	Steps       int64
	Kcal        float64
	AvgSpeedKmh float64
	MaxSpeedKmh float64
}

// CurrentSession returns a snapshot of the open session, or nil if none.
func (m *Manager) CurrentSession() *CurrentSessionView {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	avg := 0.0
	if m.cur.activeSeconds > 0 {
		avg = (m.cur.totalDistanceM / 1000.0) / (m.cur.activeSeconds / 3600.0)
	}
	return &CurrentSessionView{
		UUID:        m.cur.uuid,
		StartedAt:   m.cur.startedAt,
		DurationS:   int64(m.cur.activeSeconds),
		DistanceM:   m.cur.totalDistanceM,
		Steps:       m.cur.totalSteps,
		Kcal:        m.cur.kcalAccum,
		AvgSpeedKmh: avg,
		MaxSpeedKmh: m.cur.maxSpeedKmh,
	}
}

// Resume restores any in-flight session from the store. Called once during
// daemon startup, before the BLE loop starts ingesting frames.
//
// If the most-recent open session is younger than resumeMaxAge, samples are
// replayed through the accumulators so totals match what was persisted.
// Otherwise the session is force-closed with ended_at set to the last sample
// timestamp (PRD §7 daemon-restart edge case).
func (m *Manager) Resume(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, err := m.store.MostRecentOpenSession(ctx)
	if err != nil {
		return fmt.Errorf("resume lookup: %w", err)
	}
	if sess == nil {
		return nil
	}

	if now.Sub(sess.StartedAt) > resumeMaxAge {
		// Force-close: replay so totals are populated, then close at the last
		// sample ts (or started_at if we never recorded a sample).
		end, ok, err := m.store.LastSampleTime(ctx, sess.ID)
		if err != nil {
			return err
		}
		if !ok {
			end = sess.StartedAt
		}
		if err := m.replayLocked(ctx, sess); err != nil {
			return err
		}
		m.log.Info("session.force_close_on_resume",
			"uuid", sess.UUID, "age_h", now.Sub(sess.StartedAt).Hours())
		return m.closeLocked(ctx, end)
	}

	if err := m.replayLocked(ctx, sess); err != nil {
		return err
	}
	m.log.Info("session.resume",
		"uuid", sess.UUID, "age_min", now.Sub(sess.StartedAt).Minutes(),
		"distance_m", m.cur.totalDistanceM, "active_s", m.cur.activeSeconds)
	return nil
}

// Ingest is called for every decoded status frame. It opens, extends, resumes,
// or closes the current session per PRD §7, and always appends a row to the
// samples table while a session is open.
func (m *Manager) Ingest(ctx context.Context, frame ble.Status, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	isRunning := frame.State.IsRunning()

	// Idle-gap close: if we're sitting on an open session and the belt has been
	// stopped longer than the configured gap, close now. Evaluated on every
	// non-running frame so we close promptly without needing Tick.
	if m.cur != nil && !isRunning && !m.cur.lastRunningTs.IsZero() {
		if now.Sub(m.cur.lastRunningTs) > time.Duration(m.cfg.GapMinutes)*time.Minute {
			endedAt := m.cur.lastRunningTs
			if err := m.closeLocked(ctx, endedAt); err != nil {
				return err
			}
			// The current frame is post-close idle noise — drop it.
			return nil
		}
	}

	switch {
	case m.cur == nil && isRunning:
		if err := m.openLocked(ctx, now); err != nil {
			return err
		}
	case m.cur == nil && !isRunning:
		// Stopped + no session: nothing to do. Don't persist anything.
		return nil
	case m.cur != nil && isRunning:
		// Detect resume after a pause longer than the BLE-drop grace window:
		// that's a real "user took a break" pause, so bump pause_count.
		gap := now.Sub(m.cur.lastRunningTs)
		if !m.cur.lastRunningTs.IsZero() && gap > time.Duration(m.cfg.ResumeWithinSeconds)*time.Second {
			if err := m.store.IncrementPauseCount(ctx, m.cur.sessionID); err != nil {
				return err
			}
			m.log.Info("session.pause_resumed", "uuid", m.cur.uuid, "gap_s", gap.Seconds())
		}
	}

	// Persist the sample.
	smp := store.Sample{
		SessionID:   m.cur.sessionID,
		Ts:          now,
		BeltState:   int(frame.State),
		SpeedKmh:    frame.SpeedKmh,
		DistanceM:   frame.Distance,
		Steps:       int64(frame.Steps),
		Mode:        int(frame.Mode),
		Button:      int(frame.Button),
		RawFrameHex: rawFrameHex(frame, m.cfg.IncludeRawFrames),
	}
	if _, err := m.store.AppendSample(ctx, smp); err != nil {
		return fmt.Errorf("append sample: %w", err)
	}

	// Update in-memory totals.
	if isRunning {
		m.applyRunningTick(frame.SpeedKmh, frame.Distance, frame.Steps, now)
	} else {
		// Non-running frame: drop the device-counter baseline so the next
		// running frame starts a fresh delta window (the belt may have reset
		// its counters between the two — gotcha #8).
		m.cur.lastDevDistance = 0
		m.cur.lastDevSteps = 0
	}
	return nil
}

// Tick lets the daemon advance the close decision even when no frames arrive
// (e.g. the user powered the pad off and BLE went silent). Pass the current
// wall time; returns the same close error as Ingest would.
func (m *Manager) Tick(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || m.cur.lastRunningTs.IsZero() {
		return nil
	}
	if now.Sub(m.cur.lastRunningTs) <= time.Duration(m.cfg.GapMinutes)*time.Minute {
		return nil
	}
	return m.closeLocked(ctx, m.cur.lastRunningTs)
}

// ForceClose closes any open session immediately. Used during clean shutdown.
// The end time is the last running-frame timestamp, or now if we never saw one.
func (m *Manager) ForceClose(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	end := m.cur.lastRunningTs
	if end.IsZero() {
		end = now
	}
	return m.closeLocked(ctx, end)
}

// --- internals --------------------------------------------------------------

func (m *Manager) openLocked(ctx context.Context, now time.Time) error {
	uuid := newUUIDv4()
	id, err := m.store.OpenSession(ctx, uuid, now)
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	m.cur = &runState{sessionID: id, uuid: uuid, startedAt: now}
	m.log.Info("session.open", "uuid", uuid)
	return nil
}

func (m *Manager) closeLocked(ctx context.Context, endedAt time.Time) error {
	totals := store.SessionTotals{
		DurationS:   int64(m.cur.activeSeconds),
		DistanceM:   m.cur.totalDistanceM,
		Steps:       m.cur.totalSteps,
		MaxSpeedKmh: m.cur.maxSpeedKmh,
		Kcal:        m.cur.kcalAccum,
	}
	if m.cur.activeSeconds > 0 {
		totals.AvgSpeedKmh = (m.cur.totalDistanceM / 1000.0) / (m.cur.activeSeconds / 3600.0)
	}
	if err := m.store.CloseSession(ctx, m.cur.sessionID, endedAt, totals); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	m.log.Info("session.close",
		"uuid", m.cur.uuid,
		"duration_s", totals.DurationS,
		"distance_m", totals.DistanceM,
		"steps", totals.Steps,
		"avg_speed_kmh", totals.AvgSpeedKmh,
		"max_speed_kmh", totals.MaxSpeedKmh,
		"kcal", totals.Kcal,
	)
	m.cur = nil
	return nil
}

// applyRunningTick is the single accumulator used by both Ingest (live frames)
// and replayLocked (persisted samples). All inputs are device-reported values
// for a single tick; ts is the wall time at which they were observed.
func (m *Manager) applyRunningTick(speedKmh, devDistance float64, devSteps uint32, ts time.Time) {
	// Distance delta. Device may have reset its counter between two running
	// samples (e.g. across a STANDBY pause) — clamp the delta to zero in that
	// case by treating the current reading as a fresh baseline.
	if devDistance >= m.cur.lastDevDistance {
		m.cur.totalDistanceM += devDistance - m.cur.lastDevDistance
	} else {
		m.cur.totalDistanceM += devDistance
	}
	m.cur.lastDevDistance = devDistance

	// Steps delta — same shape.
	curSteps := int64(devSteps)
	prevSteps := int64(m.cur.lastDevSteps)
	if curSteps >= prevSteps {
		m.cur.totalSteps += curSteps - prevSteps
	} else {
		m.cur.totalSteps += curSteps
	}
	m.cur.lastDevSteps = devSteps

	if speedKmh > m.cur.maxSpeedKmh {
		m.cur.maxSpeedKmh = speedKmh
	}

	// Time + calories: only count the gap when the previous tick was also
	// running and the two are within the polling jitter window. This naturally
	// skips pause gaps without needing a separate "is this a resume?" branch.
	if !m.cur.lastRunningTs.IsZero() {
		dt := ts.Sub(m.cur.lastRunningTs).Seconds()
		if dt > 0 && dt <= runningGapMaxSeconds {
			m.cur.activeSeconds += dt
			m.cur.kcalAccum += Kcal(speedKmh, m.cfg.WeightKg, dt)
		}
	}
	m.cur.lastRunningTs = ts
}

// replayLocked rebuilds the in-memory accumulators from persisted samples.
// Used by Resume; runs through the same applyRunningTick path so totals match
// what Ingest would have produced live.
func (m *Manager) replayLocked(ctx context.Context, sess *store.Session) error {
	_, samples, err := m.store.GetSession(ctx, sess.UUID)
	if err != nil {
		return fmt.Errorf("replay get session: %w", err)
	}

	m.cur = &runState{sessionID: sess.ID, uuid: sess.UUID, startedAt: sess.StartedAt}

	for _, smp := range samples {
		// #nosec G115 -- belt_state is a single wire byte (values 0..9, see ble.BeltState)
		belt := ble.BeltState(smp.BeltState)
		if belt.IsRunning() {
			// #nosec G115 -- steps round-trips through the device's native uint32 counter
			m.applyRunningTick(smp.SpeedKmh, smp.DistanceM, uint32(smp.Steps), smp.Ts)
		} else {
			m.cur.lastDevDistance = 0
			m.cur.lastDevSteps = 0
		}
	}
	return nil
}

func rawFrameHex(f ble.Status, include bool) string {
	if !include {
		return ""
	}
	return hex.EncodeToString(f.Raw[:])
}
