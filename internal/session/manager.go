// Package session owns the session-grouping state machine that turns a stream
// of BLE status frames into persisted sessions.
//
// Lifecycle (single source of truth — every other layer derives from this):
//
//  1. The first running frame opens a session, OR resurrects a recently-closed
//     one if its ended_at is within GapMinutes (so a coffee break inside the
//     gap window keeps the same session UUID).
//  2. The session stays open across stops shorter than GapMinutes.
//  3. The first non-running frame whose timestamp is > GapMinutes past the
//     last running frame closes the session (also enforced by Tick when no
//     frames are arriving).
//  4. Daemon shutdown is NOT a close trigger — we deliberately leave the row
//     open so the next startup resumes it via Resume (no spurious split).
//  5. Resume on startup adopts the most recent open session if it's younger
//     than resumeMaxAge; otherwise it force-closes it at the last sample ts.
//
// The rules originally lived in PRD §7; this docstring is now the source of
// truth (the PRD is updated to match). Gotchas referenced from CLAUDE.md
// #8/#9 still apply: counters reset on every STOP, not only on STANDBY, and
// BeltState.IsRunning() intentionally returns true for both ACTIVE and
// STOPPING so the final decel frame is captured.
//
// The Manager is the sole writer of session/sample rows during normal
// operation. Every public method takes the Manager lock so external callers
// can safely call Ingest, Tick, Resume, and ForceClose from different
// goroutines. ForceClose remains as a primitive for tests and explicit
// close-now use cases; the serve loop no longer calls it on shutdown.
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
// "running" samples before we stop counting the gap toward kcal. With 1 Hz
// polling, samples arrive ~1 s apart; 15 s tolerates the P1's slower frame
// rate (3-5 s/frame is common) and short BLE blips. Duration accounting no
// longer uses this — it accumulates by window, not by per-frame dt.
const runningGapMaxSeconds = 15.0

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
	kcalAccum      float64

	// Active-duration accounting via windows. A window opens when the belt
	// transitions to a running state and closes when it leaves. closedActiveS
	// is the sum of completed windows; activeWindowStart is non-zero while a
	// window is currently open. Total duration = closedActiveS + (now -
	// activeWindowStart) when open. This is robust to slow BLE frame rates
	// and brief pauses — far better than the previous dt-between-frames
	// approach which lost the entire window when frames were spaced > 5 s.
	closedActiveS     float64
	activeWindowStart time.Time

	// Per-tick state for the delta calculation.
	lastRunningTs   time.Time // last wall time at which the belt was running
	lastDevDistance float64   // device-reported distance at lastRunningTs
	lastDevSteps    uint32    // device-reported steps at lastRunningTs

	// Live snapshot mirrors. Tracked in-memory so CurrentSession() can answer
	// without a store round-trip on every 1 s live-push tick.
	lastSpeedKmh float64 // most recent frame.SpeedKmh
	paused       bool    // true when the last frame was non-running but the session is still open
	pauseCount   int64   // in-memory mirror of sessions.pause_count (also persisted via store)
}

// currentActiveSeconds returns the total active duration including any
// currently-open running window evaluated at `now`.
func (r *runState) currentActiveSeconds(now time.Time) float64 {
	total := r.closedActiveS
	if !r.activeWindowStart.IsZero() {
		total += now.Sub(r.activeWindowStart).Seconds()
	}
	return total
}

// closeActiveWindow seals the current window into closedActiveS. Called
// whenever the belt leaves a running state or the session itself ends.
func (r *runState) closeActiveWindow(now time.Time) {
	if r.activeWindowStart.IsZero() {
		return
	}
	r.closedActiveS += now.Sub(r.activeWindowStart).Seconds()
	r.activeWindowStart = time.Time{}
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
	UUID            string
	StartedAt       time.Time
	DurationS       int64
	DistanceM       float64
	Steps           int64
	Kcal            float64
	AvgSpeedKmh     float64
	MaxSpeedKmh     float64
	CurrentSpeedKmh float64 // most recent frame.SpeedKmh; 0 when paused/stopped
	Paused          bool    // last frame was non-running but session is still open
	PauseCount      int64   // in-memory mirror of sessions.pause_count
}

// CurrentSession returns a snapshot of the open session, or nil if none.
func (m *Manager) CurrentSession() *CurrentSessionView {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return nil
	}
	active := m.cur.currentActiveSeconds(time.Now())
	avg := 0.0
	if active > 0 {
		avg = (m.cur.totalDistanceM / 1000.0) / (active / 3600.0)
	}
	curSpeed := m.cur.lastSpeedKmh
	if m.cur.paused {
		curSpeed = 0
	}
	return &CurrentSessionView{
		UUID:            m.cur.uuid,
		StartedAt:       m.cur.startedAt,
		DurationS:       int64(active),
		DistanceM:       m.cur.totalDistanceM,
		Steps:           m.cur.totalSteps,
		Kcal:            m.cur.kcalAccum,
		AvgSpeedKmh:     avg,
		MaxSpeedKmh:     m.cur.maxSpeedKmh,
		CurrentSpeedKmh: curSpeed,
		Paused:          m.cur.paused,
		PauseCount:      m.cur.pauseCount,
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
		// Force-close: rebuild totals so the row has a sane close snapshot,
		// then close at the last sample ts (or started_at if we never
		// recorded a sample).
		end, ok, err := m.store.LastSampleTime(ctx, sess.ID)
		if err != nil {
			return err
		}
		if !ok {
			end = sess.StartedAt
		}
		if err := m.rebuildFromHistoryLocked(ctx, sess); err != nil {
			return err
		}
		m.log.Info("session.force_close_on_resume",
			"uuid", sess.UUID, "age_h", now.Sub(sess.StartedAt).Hours())
		return m.closeLocked(ctx, end)
	}

	if err := m.rebuildFromHistoryLocked(ctx, sess); err != nil {
		return err
	}
	m.log.Info("session.resume",
		"uuid", sess.UUID, "age_min", now.Sub(sess.StartedAt).Minutes(),
		"distance_m", m.cur.totalDistanceM, "active_s", m.cur.currentActiveSeconds(now))
	return nil
}

// rebuildFromHistoryLocked picks the right reconstruction strategy for an
// open session that pre-exists this Manager instance:
//   - Stored totals non-zero → seedFromStoredLocked: preserve the truth
//     and seed baselines from the last sample.
//   - Stored totals zero → replayLocked: the session was never closed (real
//     crash mid-walk), so re-derive totals from the sample stream.
func (m *Manager) rebuildFromHistoryLocked(ctx context.Context, sess *store.Session) error {
	if sess.DurationS > 0 {
		return m.seedFromStoredLocked(ctx, sess)
	}
	return m.replayLocked(ctx, sess)
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
		if err := m.ensureSessionLocked(ctx, now); err != nil {
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
			m.cur.pauseCount++
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
		m.cur.lastSpeedKmh = frame.SpeedKmh
		m.cur.paused = false
	} else {
		// Non-running frame: close the active window (the seconds we've been
		// running just became part of closedActiveS) and drop the device-
		// counter baseline so the next running frame starts a fresh delta
		// window (the belt may reset its counters across STANDBY — gotcha #8).
		m.cur.closeActiveWindow(now)
		m.cur.lastDevDistance = 0
		m.cur.lastDevSteps = 0
		m.cur.lastSpeedKmh = 0
		m.cur.paused = true
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

// ensureSessionLocked is the single decision point for "we just saw a running
// frame and have no current session — what should we do?". It picks exactly
// one of three paths:
//
//  1. Resurrect: most recent row is closed and its ended_at is within
//     resurrectionWindow — same physical walk, just bridged across a long
//     pause or a daemon restart. Re-opens the row in place, replays samples
//     to rebuild totals, bumps pause_count.
//  2. Continue: most recent row is still open and younger than resumeMaxAge.
//     Normally Resume already adopted it at startup; this is the safety net
//     for code paths that bypass Resume.
//  3. Open: fresh UUID, fresh row, zero accumulators.
//
// Centralising the choice here means the rest of the manager only ever sees
// "a valid m.cur" and never needs to second-guess the lifecycle.
func (m *Manager) ensureSessionLocked(ctx context.Context, now time.Time) error {
	recent, err := m.store.MostRecentSession(ctx)
	if err != nil {
		return fmt.Errorf("ensure session: lookup recent: %w", err)
	}
	if recent != nil {
		if recent.EndedAt.Valid {
			if now.Sub(recent.EndedAt.Time) < m.resurrectionWindow() {
				return m.resurrectLocked(ctx, recent, now)
			}
		} else if now.Sub(recent.StartedAt) <= resumeMaxAge {
			return m.replayLocked(ctx, recent)
		}
	}
	return m.openLocked(ctx, now)
}

// resurrectionWindow is the max age of a closed session's ended_at that still
// counts as "same walk" when the next running frame arrives. Delegates to the
// package-level helper so the historical-cleanup stitch in the store package
// can apply the exact same rule.
func (m *Manager) resurrectionWindow() time.Duration {
	return ResurrectionWindow(m.cfg.GapMinutes)
}

// ResurrectionWindow is the single source of truth for the "same walk?" time
// tolerance. The close itself fires after GapMinutes of idle (so at close
// moment, now - ended_at already equals GapMinutes); we grant another
// GapMinutes on top so the user can come back within roughly a gap-window
// after the close was decided. Effective idle tolerance across a
// close+resurrect is therefore 2× GapMinutes. Used at runtime by Manager
// and at startup by store.StitchAdjacentSessions for historical merge.
func ResurrectionWindow(gapMinutes int) time.Duration {
	return 2 * time.Duration(gapMinutes) * time.Minute
}

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

// resurrectLocked clears ended_at/synced_at on the row, rebuilds the
// in-memory accumulators so the next live tick continues from the right
// baseline, and bumps pause_count to record the bridged gap.
//
// Rebuild strategy: prefer stored totals (the truth at the previous close)
// over re-running applyRunningTick over every sample, which would silently
// drift away from what argo and the user actually saw. The branch lives in
// rebuildFromHistoryLocked.
func (m *Manager) resurrectLocked(ctx context.Context, sess *store.Session, now time.Time) error {
	if err := m.store.ReopenSession(ctx, sess.ID); err != nil {
		return fmt.Errorf("reopen session %s: %w", sess.UUID, err)
	}
	if err := m.rebuildFromHistoryLocked(ctx, sess); err != nil {
		return err
	}
	// ReopenSession bumped the row's pause_count by 1; mirror in memory on
	// top of whatever the seed/replay loaded.
	m.cur.pauseCount++
	m.log.Info("session.resurrect",
		"uuid", sess.UUID,
		"closed_for_s", now.Sub(sess.EndedAt.Time).Seconds(),
		"distance_m", m.cur.totalDistanceM,
		"active_s", m.cur.currentActiveSeconds(now))
	return nil
}

func (m *Manager) closeLocked(ctx context.Context, endedAt time.Time) error {
	// Seal any still-open running window at the close time so its seconds
	// are counted in the final duration.
	m.cur.closeActiveWindow(endedAt)
	active := m.cur.closedActiveS
	totals := store.SessionTotals{
		DurationS:   int64(active),
		DistanceM:   m.cur.totalDistanceM,
		Steps:       m.cur.totalSteps,
		MaxSpeedKmh: m.cur.maxSpeedKmh,
		Kcal:        m.cur.kcalAccum,
	}
	if active > 0 {
		totals.AvgSpeedKmh = (m.cur.totalDistanceM / 1000.0) / (active / 3600.0)
		// Belt-and-suspenders for any case the derived-speed pass in
		// applyRunningTick missed (e.g. a session restored from samples that
		// pre-date this fix, or a multi-tick BLE outage where individual
		// derivations were skipped). avg > max is physically impossible —
		// raise max to avg and log so the upstream daemon issue stays visible.
		if totals.AvgSpeedKmh > totals.MaxSpeedKmh {
			m.log.Warn("session.max_speed.clamped_to_avg",
				"uuid", m.cur.uuid,
				"raw_max_kmh", totals.MaxSpeedKmh,
				"avg_kmh", totals.AvgSpeedKmh,
			)
			totals.MaxSpeedKmh = totals.AvgSpeedKmh
		}
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
	// case by treating the current reading as a fresh baseline. We capture
	// the delta as a local so the max-speed pass below can derive an implicit
	// speed from it.
	counterReset := devDistance < m.cur.lastDevDistance
	var distanceDelta float64
	if counterReset {
		distanceDelta = devDistance
	} else {
		distanceDelta = devDistance - m.cur.lastDevDistance
	}
	m.cur.totalDistanceM += distanceDelta
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

	// Max speed: combine the per-frame setpoint reading with the implicit
	// speed derived from this tick's distance delta and wall-time gap. BLE
	// frames arrive at 3-5 s intervals, so a brief in-frame speed bump is
	// invisible to the setpoint stream but captured by the device's own
	// distance integrator. Without the derived component, max_speed_kmh can
	// read lower than avg_speed_kmh (which is computed at close time from
	// total_distance / active_time) — a physically impossible artifact.
	// Cap at the device's hardware ceiling so counter glitches or replays
	// of synthetic data can't poison the peak.
	peak := speedKmh
	if !m.cur.lastRunningTs.IsZero() && !counterReset && distanceDelta > 0 {
		dt := ts.Sub(m.cur.lastRunningTs).Seconds()
		if dt > 0 && dt <= runningGapMaxSeconds {
			derivedKmh := (distanceDelta / dt) * 3.6
			if derivedKmh > peak && derivedKmh <= ble.MaxSpeedKmh {
				peak = derivedKmh
			}
		}
	}
	if peak > m.cur.maxSpeedKmh {
		m.cur.maxSpeedKmh = peak
	}

	// Open the active window on the first running tick of this burst.
	// Subsequent ticks in the same window are no-ops here — the elapsed
	// wall time accumulates implicitly between activeWindowStart and the
	// next close (either a non-running frame or the session end).
	if m.cur.activeWindowStart.IsZero() {
		m.cur.activeWindowStart = ts
	}

	// Calories: still per-frame, with a generous cap so slow BLE polling
	// (the belt sometimes emits at 3-5 s/frame) doesn't drop kcal silently.
	if !m.cur.lastRunningTs.IsZero() {
		dt := ts.Sub(m.cur.lastRunningTs).Seconds()
		if dt > 0 && dt <= runningGapMaxSeconds {
			m.cur.kcalAccum += Kcal(speedKmh, m.cfg.WeightKg, dt)
		}
	}
	m.cur.lastRunningTs = ts
}

// seedFromStoredLocked builds m.cur from the session row's stored totals
// (the truth at the previous close) and seeds the device-counter baseline
// from the most-recent running sample so the next live frame computes a
// small delta on top.
//
// This is the right path whenever stored totals exist. Re-running
// applyRunningTick over every sample (replayLocked) is deterministic in
// isolation but does NOT reproduce the original live totals: the live run
// carried per-tick state across thousands of frames (counter trajectories
// across brief stops, kcal dt-gating, window-close timing) that can't be
// reconstructed from samples alone. Trusting the stored value preserves
// whatever the user actually saw at close and avoids silent drift on
// resurrect.
//
// Replay remains the right path for sessions that were truly never closed
// (stored totals = 0) — typically a crash mid-walk.
func (m *Manager) seedFromStoredLocked(ctx context.Context, sess *store.Session) error {
	m.cur = &runState{
		sessionID:      sess.ID,
		uuid:           sess.UUID,
		startedAt:      sess.StartedAt,
		totalDistanceM: sess.DistanceM,
		totalSteps:     sess.Steps,
		maxSpeedKmh:    sess.MaxSpeedKmh,
		kcalAccum:      sess.Kcal,
		closedActiveS:  float64(sess.DurationS),
		pauseCount:     sess.PauseCount,
	}

	lastRun, hasRunning, err := m.store.LastRunningSample(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("seed last running sample: %w", err)
	}
	if hasRunning {
		m.cur.lastRunningTs = lastRun.Ts
		m.cur.lastDevDistance = lastRun.DistanceM
		// #nosec G115 -- steps round-trips through the device's native uint32 counter
		m.cur.lastDevSteps = uint32(lastRun.Steps)
		m.cur.lastSpeedKmh = lastRun.SpeedKmh
	}

	last, hasAny, err := m.store.LastSample(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("seed last sample: %w", err)
	}
	if hasAny {
		// #nosec G115 -- belt_state is a single wire byte (values 0..9)
		belt := ble.BeltState(last.BeltState)
		if !belt.IsRunning() {
			// Session is currently paused. Clear display speed but keep the
			// dev-counter baselines from the last running sample — the
			// clamp in applyRunningTick handles the STANDBY-reset case if
			// the device counter actually wraps to zero.
			m.cur.lastSpeedKmh = 0
			m.cur.paused = true
		}
	}
	return nil
}

// replayLocked rebuilds the in-memory accumulators from persisted samples.
// Used by Resume; runs through the same applyRunningTick path so totals match
// what Ingest would have produced live.
func (m *Manager) replayLocked(ctx context.Context, sess *store.Session) error {
	_, samples, err := m.store.GetSession(ctx, sess.UUID)
	if err != nil {
		return fmt.Errorf("replay get session: %w", err)
	}

	m.cur = &runState{
		sessionID:  sess.ID,
		uuid:       sess.UUID,
		startedAt:  sess.StartedAt,
		pauseCount: sess.PauseCount,
	}

	for _, smp := range samples {
		// #nosec G115 -- belt_state is a single wire byte (values 0..9, see ble.BeltState)
		belt := ble.BeltState(smp.BeltState)
		if belt.IsRunning() {
			// #nosec G115 -- steps round-trips through the device's native uint32 counter
			m.applyRunningTick(smp.SpeedKmh, smp.DistanceM, uint32(smp.Steps), smp.Ts)
		} else {
			// Mirror Ingest: close the active window on each non-running
			// sample so replay produces the same closedActiveS as live.
			m.cur.closeActiveWindow(smp.Ts)
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
