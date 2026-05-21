// Package serve wires every internal subsystem into the running daemon:
// config → logger → store → session.Manager → ble.Link → api.Server.
//
// One root context drives everything. Signal-aware cancellation lives in main;
// serve.Run accepts that context and propagates it. Graceful shutdown order:
//
//  1. ctx is cancelled — link.Run, api.ListenAndServe and the tick loop all
//     return.
//  2. store.Close releases the SQLite handle.
//
// Shutdown deliberately does NOT close any open session. The session row
// stays open and the next startup resumes it (Manager.Resume), so a
// restart while the user is still walking does not split the session in two
// and does not lose the device's running distance counter. Sessions only
// close via the idle-gap rule or the 6 h staleness force-close in Resume.
// See internal/session.Manager for the full lifecycle.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/api"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/config"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/session"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
	syncpkg "github.com/jkrumm/king-smith-walkingpad-mac/internal/sync"
)

// tickInterval is how often the manager re-evaluates the idle-gap close.
// At 10 s the worst-case extra wait before close is small relative to the
// 15-min default gap.
const tickInterval = 10 * time.Second

// dropInterval is how often the runtime sweep drops short standalone
// sessions whose resurrection window has expired. Once a minute is the
// finest cadence that matters — eligibility is bounded by ended_at +
// ResurrectionWindow, so a row lives at most dropInterval past its
// eligibility before being collected.
const dropInterval = 1 * time.Minute

// Run brings the daemon up and blocks until ctx is cancelled or a subsystem
// fails. Returns nil on clean shutdown.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger, version string) error {
	if log == nil {
		log = slog.Default()
	}

	// --- store -----------------------------------------------------------
	dataDir, err := config.DataDir()
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	st, err := store.Open(filepath.Join(dataDir, "db.sqlite"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("store.close", "err", err)
		}
	}()

	// One-shot: recompute duration_s for pre-window-logic sessions and clear
	// their synced_at so Argo gets the corrected row on the next upload tick.
	// Marker in daemon_meta makes this a no-op on subsequent startups.
	backfillCtx, backfillCancel := context.WithTimeout(ctx, 30*time.Second)
	if n, err := st.BackfillDurations(backfillCtx); err != nil {
		log.Warn("backfill.durations_failed", "err", err)
	} else if n > 0 {
		log.Info("backfill.durations_done", "sessions_updated", n)
	}
	backfillCancel()

	// One-shot: stitch adjacent closed sessions that were split by the
	// pre-fix lifecycle (shutdown-close + fresh-open across `make up`).
	// Uses the same resurrection window as the live manager so historical
	// data ends up consistent with the new grouping rule.
	stitchCtx, stitchCancel := context.WithTimeout(ctx, 30*time.Second)
	resurrectWindow := session.ResurrectionWindow(cfg.Session.GapMinutes)
	if n, err := st.StitchAdjacentSessions(stitchCtx, resurrectWindow); err != nil {
		log.Warn("stitch.adjacent_failed", "err", err)
	} else if n > 0 {
		log.Info("stitch.adjacent_done", "sessions_merged", n, "window_min", resurrectWindow.Minutes())
	}
	stitchCancel()

	// Startup sweep: drop short standalone sessions that can no longer be
	// stitched (ended_at + resurrectWindow already past). Same sweep runs
	// periodically below — running it once at startup catches anything that
	// accumulated while the daemon was off.
	minDur := time.Duration(cfg.Session.MinDurationSeconds) * time.Second
	if minDur > 0 {
		dropCtx, dropCancel := context.WithTimeout(ctx, 30*time.Second)
		dropped, err := st.DropShortStandaloneSessions(dropCtx, minDur, resurrectWindow, time.Now().UTC())
		dropCancel()
		if err != nil {
			log.Warn("drop_short.startup_failed", "err", err)
		} else if len(dropped) > 0 {
			log.Info("drop_short.startup_done", "sessions_dropped", len(dropped))
		}
	}

	// --- session manager --------------------------------------------------
	mgr := session.NewManager(session.Config{
		GapMinutes:          cfg.Session.GapMinutes,
		ResumeWithinSeconds: cfg.Session.ResumeWithinSeconds,
		WeightKg:            cfg.Body.WeightKg,
		// Raw frames stay off in info+; debug logging populates them so the
		// hex stream is available for protocol RE without bloating prod rows.
		IncludeRawFrames: cfg.Daemon.LogLevel == "debug",
	}, st, log.With("component", "session"))

	resumeCtx, resumeCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := mgr.Resume(resumeCtx, time.Now().UTC()); err != nil {
		resumeCancel()
		return fmt.Errorf("session resume: %w", err)
	}
	resumeCancel()

	// --- BLE link + adapters ---------------------------------------------
	statusProv := newStatusProvider("WalkingPad")

	link := ble.NewLink(ble.LinkConfig{
		Address:      cfg.Device.Address,
		PollInterval: time.Duration(cfg.Daemon.PollIntervalMs) * time.Millisecond,
		Logger:       log.With("component", "ble"),
		OnStatus: func(s ble.Status) {
			now := time.Now().UTC()
			statusProv.observe(s, now)
			// Background ctx: this runs on the BLE notification thread and
			// must always reach the manager; the ingest path is cheap.
			if err := mgr.Ingest(context.Background(), s, now); err != nil {
				log.Error("session.ingest", "err", err)
			}
		},
		OnError: func(err error) { log.Warn("ble.decode", "err", err) },
		OnConnect: func(addr string) {
			statusProv.markConnected(addr)
			log.Info("ble.bound", "addr", addr)
		},
		OnDisconnect: func(addr string, reason error) {
			statusProv.markDisconnected()
			log.Info("ble.unbound", "addr", addr, "reason", reasonOrNil(reason))
		},
	})

	ctrl := newController(link, statusProv)

	// --- Argo sync worker (optional) -------------------------------------
	// Only constructed when both URL and token are configured. Without it,
	// the API returns 503 on POST /sync/argo via NopSyncer; everything else
	// still works.
	var syncer api.Syncer = api.NopSyncer{}
	var syncWorker *syncpkg.Worker
	var liveWorker *syncpkg.LiveWorker
	if cfg.SyncEnabled() {
		syncWorker = syncpkg.NewWorker(syncpkg.Config{
			BaseURL:   cfg.Argo.URL,
			Token:     cfg.Argo.Token,
			UserAgent: "king-smith-walkingpad-mac/" + version,
		}, st, log)
		syncer = syncWorker
		liveWorker = syncpkg.NewLiveWorker(syncpkg.LiveConfig{
			BaseURL:   cfg.Argo.URL,
			Token:     cfg.Argo.Token,
			UserAgent: "king-smith-walkingpad-mac/" + version,
		}, mgr, log)
	} else {
		log.Info("sync.disabled", "reason", "no argo.token set")
	}

	// --- HTTP API ---------------------------------------------------------
	apiSrv := api.New(api.Config{
		Addr:    "127.0.0.1:" + strconv.Itoa(cfg.Daemon.HTTPPort),
		Token:   cfg.Daemon.HTTPToken,
		Version: version,
	}, api.Deps{
		Store:      st,
		Manager:    mgr,
		Controller: ctrl,
		Status:     statusProv,
		Syncer:     syncer,
		Logger:     log.With("component", "api"),
	})

	// --- run loop ---------------------------------------------------------
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg          sync.WaitGroup
		firstErr    error
		firstErrMu  sync.Mutex
		recordError = func(name string, err error) {
			if err == nil {
				return
			}
			log.Error("subsystem exit", "name", name, "err", err)
			firstErrMu.Lock()
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", name, err)
			}
			firstErrMu.Unlock()
			cancel() // any subsystem failure brings everything down
		}
	)

	spawn := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordError(name, fn())
		}()
	}

	log.Info("serve.starting",
		"version", version,
		"http_port", cfg.Daemon.HTTPPort,
		"poll_interval_ms", cfg.Daemon.PollIntervalMs,
		"data_dir", dataDir,
	)

	spawn("ble", func() error { return link.Run(runCtx) })
	spawn("api", func() error { return apiSrv.ListenAndServe(runCtx) })
	spawn("tick", func() error { return runTickLoop(runCtx, mgr, log) })
	if minDur := time.Duration(cfg.Session.MinDurationSeconds) * time.Second; minDur > 0 {
		spawn("drop", func() error {
			return runDropLoop(runCtx, st, minDur, resurrectWindow, log)
		})
	}

	// Reconcile argo orphans before the sync ticker arms. Synchronous so
	// the resulting tombstones land in the first flush. Soft-fail: if argo
	// is unreachable the daemon still starts and the next reconcile run on
	// the next startup will retry.
	if syncWorker != nil {
		reconcileCtx, reconcileCancel := context.WithTimeout(ctx, 30*time.Second)
		if orphans, err := syncWorker.Reconcile(reconcileCtx); err != nil {
			log.Warn("sync.reconcile_failed", "err", err)
		} else if orphans > 0 {
			log.Info("sync.reconcile_done", "orphans_tombstoned", orphans)
		}
		reconcileCancel()
	}
	if syncWorker != nil {
		spawn("sync", func() error { return syncWorker.Run(runCtx) })
	}
	if liveWorker != nil {
		spawn("live", func() error { return liveWorker.Run(runCtx) })
	}

	wg.Wait()

	// Shutdown intentionally does not close the active session: the row stays
	// open so the next startup's Manager.Resume picks up where we left off.
	// See package docstring for the lifecycle rationale.
	log.Info("serve.stopped")
	return firstErr
}

// runTickLoop calls Manager.Tick at a fixed cadence so the idle-gap close
// fires even when no frames arrive (e.g. the user shut the pad off, BLE went
// silent). Always returns nil — tick errors are logged at the call site.
func runTickLoop(ctx context.Context, mgr *session.Manager, log *slog.Logger) error {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := mgr.Tick(ctx, time.Now().UTC()); err != nil {
				log.Error("session.tick", "err", err)
			}
		}
	}
}

// runDropLoop periodically drops short standalone sessions whose
// resurrection window has expired. Lives in its own goroutine so a slow
// SQLite query (millions of rows in theory) can't stall the manager tick.
// Always returns nil — errors are logged in place.
func runDropLoop(ctx context.Context, st *store.Store, minDur, resurrectWindow time.Duration, log *slog.Logger) error {
	t := time.NewTicker(dropInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			dropped, err := st.DropShortStandaloneSessions(ctx, minDur, resurrectWindow, time.Now().UTC())
			if err != nil {
				log.Warn("drop_short.tick_failed", "err", err)
				continue
			}
			if len(dropped) > 0 {
				log.Info("drop_short.tick_done", "sessions_dropped", len(dropped))
			}
		}
	}
}

// reasonOrNil flattens an error for structured logging.
func reasonOrNil(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

// PortInUse reports whether the given address is already bound. The CLI uses
// it to give a precise error message before spinning up everything else.
func PortInUse(addr string) (bool, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return true, nil
		}
		return false, err
	}
	_ = ln.Close()
	return false, nil
}
