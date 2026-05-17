// Package sync uploads closed sessions to the Argo API.
//
// The worker polls the local store on a fixed cadence and POSTs every
// not-yet-synced session to argo's /walking-pad/sessions endpoint. The endpoint
// is idempotent (upsert on uuid), so a session that returns success but whose
// MarkSynced fails locally is safe to retry — argo will simply rewrite the row.
//
// The worker is only constructed when config.SyncEnabled() reports true; the
// serve package falls back to api.NopSyncer otherwise. Sync is strictly
// optional — the daemon, store, and HTTP API all work without it.
package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// Defaults intentionally favour low traffic over snappy uploads: closed
// sessions are rare events and idempotency makes retries free.
const (
	defaultPollInterval = 60 * time.Second
	defaultBatchSize    = 25
	defaultHTTPTimeout  = 10 * time.Second
)

// Config selects which argo to talk to and how aggressively to flush.
type Config struct {
	// BaseURL is the argo API root, e.g. https://argo.jkrumm.com/api. The
	// worker appends /walking-pad/sessions; no trailing slash required.
	BaseURL string
	// Token is the bearer credential. Empty disables construction (see
	// serve.go which only builds the worker when SyncEnabled is true).
	Token string
	// PollInterval is the gap between flush attempts. Default 60 s.
	PollInterval time.Duration
	// BatchSize caps how many sessions one tick will try to upload. Default 25.
	BatchSize int
	// UserAgent identifies the daemon in argo's request logs. Default
	// "king-smith-walkingpad-mac".
	UserAgent string
	// HTTPClient lets tests inject a stubbed transport. Default has a 10 s
	// per-request timeout.
	HTTPClient *http.Client
}

func (c *Config) withDefaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.UserAgent == "" {
		c.UserAgent = "king-smith-walkingpad-mac"
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
}

// storeAPI is the slice of *store.Store the worker needs. Allows tests to
// stub without spinning the whole SQLite layer.
type storeAPI interface {
	UnsyncedSessions(ctx context.Context, limit int) ([]store.Session, error)
	MarkSynced(ctx context.Context, uuid string, syncedAt time.Time) error
}

// Worker owns the upload loop. Instantiate one per process; safe for
// concurrent SyncNow callers (the underlying store is single-writer SQLite).
type Worker struct {
	cfg Config
	st  storeAPI
	log *slog.Logger
}

// NewWorker returns a Worker ready to Run. Required: cfg.BaseURL and cfg.Token
// non-empty; callers should check config.SyncEnabled() first.
func NewWorker(cfg Config, st *store.Store, log *slog.Logger) *Worker {
	cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	return &Worker{cfg: cfg, st: st, log: log.With("component", "sync")}
}

// Run blocks until ctx is cancelled, periodically calling SyncNow. The first
// flush fires immediately so a backlog after restart drains without waiting a
// full PollInterval. Always returns nil — tick errors are logged in place.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("sync.starting",
		"base_url", w.cfg.BaseURL,
		"poll_interval", w.cfg.PollInterval,
		"batch_size", w.cfg.BatchSize,
	)

	// Drain any backlog before arming the ticker.
	w.flushOnce(ctx)

	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			w.flushOnce(ctx)
		}
	}
}

// SyncNow triggers an immediate flush of the pending queue. Used by the
// POST /sync/argo handler. Returns (synced, failed) counts for the batch and
// the first hard error encountered (zero error if everything succeeded or only
// hit recoverable transients — those stay in the queue for next tick).
func (w *Worker) SyncNow(ctx context.Context) (int, int, error) {
	return w.flush(ctx)
}

// flushOnce wraps flush for the loop, swallowing the error after logging so
// the loop never exits on a transient hiccup.
func (w *Worker) flushOnce(ctx context.Context) {
	synced, failed, err := w.flush(ctx)
	if err != nil {
		w.log.Warn("sync.flush_error", "err", err)
	}
	if synced > 0 || failed > 0 {
		w.log.Info("sync.flush", "synced", synced, "failed", failed)
	}
}

func (w *Worker) flush(ctx context.Context) (int, int, error) {
	pending, err := w.st.UnsyncedSessions(ctx, w.cfg.BatchSize)
	if err != nil {
		return 0, 0, fmt.Errorf("list unsynced: %w", err)
	}
	if len(pending) == 0 {
		return 0, 0, nil
	}

	var synced, failed int
	var firstErr error
	for _, sess := range pending {
		if ctx.Err() != nil {
			break
		}
		err := w.upload(ctx, sess)
		if err != nil {
			failed++
			w.log.Warn("sync.session_failed", "uuid", sess.UUID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := w.st.MarkSynced(ctx, sess.UUID, time.Now().UTC()); err != nil {
			// Argo got the row but our local mark failed. Idempotent upsert
			// makes the next attempt safe.
			failed++
			w.log.Warn("sync.mark_failed", "uuid", sess.UUID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		synced++
		w.log.Info("sync.session_uploaded", "uuid", sess.UUID)
	}
	return synced, failed, firstErr
}

// upload POSTs one session to argo. Returns nil on 2xx, an error otherwise.
// 5xx and connection failures are reported the same way — the caller leaves
// the row pending and the next tick retries.
func (w *Worker) upload(ctx context.Context, sess store.Session) error {
	if !sess.EndedAt.Valid {
		// Defensive: the store query already filters to ended sessions. If
		// this fires the migration drifted from the worker.
		return errors.New("session has no ended_at — refusing to upload")
	}

	body, err := json.Marshal(sessionPayload{
		UUID:        sess.UUID,
		StartedAt:   sess.StartedAt.UTC().Format(time.RFC3339),
		EndedAt:     sess.EndedAt.Time.UTC().Format(time.RFC3339),
		DurationS:   sess.DurationS,
		DistanceM:   sess.DistanceM,
		Steps:       sess.Steps,
		AvgSpeedKmh: sess.AvgSpeedKmh,
		MaxSpeedKmh: sess.MaxSpeedKmh,
		Kcal:        sess.Kcal,
		PauseCount:  sess.PauseCount,
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimRight(w.cfg.BaseURL, "/") + "/walking-pad/sessions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	// Read up to a kilobyte for diagnostics.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("argo %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
}

// sessionPayload mirrors the Zod schema in argo's
// apps/api/src/routes/walking-pad.ts (WalkingPadSessionInputSchema).
type sessionPayload struct {
	UUID        string  `json:"uuid"`
	StartedAt   string  `json:"started_at"`
	EndedAt     string  `json:"ended_at"`
	DurationS   int64   `json:"duration_s"`
	DistanceM   float64 `json:"distance_m"`
	Steps       int64   `json:"steps"`
	AvgSpeedKmh float64 `json:"avg_speed_kmh"`
	MaxSpeedKmh float64 `json:"max_speed_kmh"`
	Kcal        float64 `json:"kcal"`
	PauseCount  int64   `json:"pause_count"`
}
