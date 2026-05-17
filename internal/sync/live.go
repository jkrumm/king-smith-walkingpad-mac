// Live-session push worker.
//
// While a session is open in the Manager, this worker POSTs a snapshot of the
// in-flight totals to argo's POST /walking-pad/live every ~1 s. The endpoint
// stores a single in-memory snapshot with a 15 s TTL; the dashboard polls
// argo's GET /walking-pad/live every ~2 s to render the live session card.
//
// Push semantics are fire-and-forget: a failed tick is logged at debug and
// dropped, because the next tick supersedes it within a second anyway. Closed
// sessions take a different path (worker.go → POST /walking-pad/sessions), so
// we never duplicate effort or contend with the upload worker.

package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/session"
)

const (
	defaultLivePushInterval = 1 * time.Second
	defaultLiveHTTPTimeout  = 3 * time.Second
)

// LiveConfig configures the live-snapshot push loop. Most fields mirror
// sync.Config; intervals are deliberately tighter.
type LiveConfig struct {
	BaseURL      string
	Token        string
	UserAgent    string
	PushInterval time.Duration
	HTTPClient   *http.Client
}

func (c *LiveConfig) withDefaults() {
	if c.PushInterval <= 0 {
		c.PushInterval = defaultLivePushInterval
	}
	if c.UserAgent == "" {
		c.UserAgent = "king-smith-walkingpad-mac"
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: defaultLiveHTTPTimeout}
	}
}

// managerAPI is the slice of *session.Manager the live worker needs. Allows
// tests to stub without spinning the BLE + store layers.
type managerAPI interface {
	CurrentSession() *session.CurrentSessionView
}

// LiveWorker pushes per-tick live snapshots to argo's /walking-pad/live.
// Construct via NewLiveWorker; safe to spawn one per process. Stops cleanly
// on ctx cancellation.
type LiveWorker struct {
	cfg LiveConfig
	mgr managerAPI
	log *slog.Logger

	// lastPushedUUID is the session uuid we most recently pushed for. When the
	// in-memory session disappears (Manager.CurrentSession() → nil) we POST one
	// `state: ended` tombstone keyed on this uuid so argo can clear immediately
	// without waiting for TTL.
	lastPushedUUID string
}

// NewLiveWorker wires a LiveWorker. Requires non-empty cfg.BaseURL and
// cfg.Token; serve.go gates construction on config.SyncEnabled() exactly like
// the upload worker.
func NewLiveWorker(cfg LiveConfig, mgr *session.Manager, log *slog.Logger) *LiveWorker {
	cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	return &LiveWorker{cfg: cfg, mgr: mgr, log: log.With("component", "live")}
}

// Run blocks until ctx is cancelled, ticking every PushInterval. Always
// returns nil — push errors are logged at debug and dropped.
func (w *LiveWorker) Run(ctx context.Context) error {
	w.log.Info("live.starting",
		"base_url", w.cfg.BaseURL,
		"push_interval", w.cfg.PushInterval,
	)

	t := time.NewTicker(w.cfg.PushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// One last tombstone if we were mid-session: lets the dashboard
			// clear the live card immediately on shutdown.
			if w.lastPushedUUID != "" {
				cleanCtx, cancel := context.WithTimeout(context.Background(), w.cfg.HTTPClient.Timeout)
				w.pushEnded(cleanCtx, w.lastPushedUUID)
				cancel()
			}
			return nil
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// tick is the per-second push. Three branches:
//   - session is open → push the current snapshot
//   - session just ended (we had one last tick, now don't) → push tombstone
//   - no session, no prior session → no-op (don't burn HTTP traffic on idle)
func (w *LiveWorker) tick(ctx context.Context) {
	cur := w.mgr.CurrentSession()
	switch {
	case cur != nil:
		w.pushActive(ctx, cur)
		w.lastPushedUUID = cur.UUID
	case cur == nil && w.lastPushedUUID != "":
		w.pushEnded(ctx, w.lastPushedUUID)
		w.lastPushedUUID = ""
	default:
		// Idle: nothing to push.
	}
}

// pushActive sends a normal snapshot for the open session.
func (w *LiveWorker) pushActive(ctx context.Context, cur *session.CurrentSessionView) {
	state := "active"
	if cur.Paused {
		state = "paused"
	}
	body := liveSnapshotPayload{
		UUID:            cur.UUID,
		StartedAt:       cur.StartedAt.UTC().Format(time.RFC3339),
		State:           state,
		DurationS:       cur.DurationS,
		DistanceM:       cur.DistanceM,
		Steps:           cur.Steps,
		CurrentSpeedKmh: cur.CurrentSpeedKmh,
		AvgSpeedKmh:     cur.AvgSpeedKmh,
		MaxSpeedKmh:     cur.MaxSpeedKmh,
		Kcal:            cur.Kcal,
		PauseCount:      cur.PauseCount,
		SampleAt:        time.Now().UTC().Format(time.RFC3339),
	}
	if err := w.post(ctx, body); err != nil {
		w.log.Debug("live.push_failed", "uuid", cur.UUID, "err", err)
	}
}

// pushEnded sends a tombstone so argo clears immediately.
func (w *LiveWorker) pushEnded(ctx context.Context, uuid string) {
	body := liveSnapshotPayload{
		UUID:      uuid,
		StartedAt: time.Now().UTC().Format(time.RFC3339), // placeholder; argo ignores on `ended`
		State:     "ended",
		SampleAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := w.post(ctx, body); err != nil {
		w.log.Debug("live.tombstone_failed", "uuid", uuid, "err", err)
	}
}

func (w *LiveWorker) post(ctx context.Context, body liveSnapshotPayload) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	url := strings.TrimRight(w.cfg.BaseURL, "/") + "/walking-pad/live"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", w.cfg.UserAgent)

	resp, err := w.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("argo %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
}

// liveSnapshotPayload mirrors the Zod LiveSnapshotSchema in argo's
// apps/api/src/routes/walking-pad.ts. Keep in lockstep with that file.
type liveSnapshotPayload struct {
	UUID            string  `json:"uuid"`
	StartedAt       string  `json:"started_at"`
	State           string  `json:"state"`
	DurationS       int64   `json:"duration_s"`
	DistanceM       float64 `json:"distance_m"`
	Steps           int64   `json:"steps"`
	CurrentSpeedKmh float64 `json:"current_speed_kmh"`
	AvgSpeedKmh     float64 `json:"avg_speed_kmh"`
	MaxSpeedKmh     float64 `json:"max_speed_kmh"`
	Kcal            float64 `json:"kcal"`
	PauseCount      int64   `json:"pause_count"`
	SampleAt        string  `json:"sample_at"`
}
