// Package api owns the loopback HTTP server defined in PRD §8.
//
// The server is loopback-only by design (no TLS). Routes that need to write to
// the belt go through the Controller port; live status reads go through
// StatusProvider; Argo trigger goes through Syncer. The package never imports
// internal/ble's transport code directly — only the wire types — so handler
// tests can run with in-memory fakes.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/session"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// Errors returned across handler / port boundaries.
var (
	// ErrControllerUnavailable signals the BLE writer is not wired up yet.
	// Handlers translate to HTTP 503.
	ErrControllerUnavailable = errors.New("ble controller unavailable")
	// ErrSyncDisabled signals the Argo sync worker has no credentials.
	// Handlers translate to HTTP 503.
	ErrSyncDisabled = errors.New("argo sync disabled")
)

// Config carries the few knobs the server needs. Pulled from internal/config
// by the caller — we don't want a cross-package dep just for two fields.
type Config struct {
	// Addr is the listen address (e.g. "127.0.0.1:7706").
	Addr string
	// Token, if non-empty, is required as a Bearer header on every route
	// except /health.
	Token string
	// Version is reported by /health.
	Version string
}

// Deps bundles the runtime collaborators the handlers need.
type Deps struct {
	Store      *store.Store
	Manager    *session.Manager
	Controller Controller
	Status     StatusProvider
	Syncer     Syncer
	Logger     *slog.Logger
}

// Server holds the http.Server and the resolved handler. Construct with New,
// run with ListenAndServe, or attach to a custom listener via Handler.
type Server struct {
	cfg     Config
	deps    Deps
	handler http.Handler
	http    *http.Server
}

// New wires the mux and middleware. Returns a ready-to-serve Server.
func New(cfg Config, deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &Server{cfg: cfg, deps: deps}
	s.handler = s.routes()
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Handler exposes the resolved handler for tests using httptest.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe binds the configured address and serves until ctx is
// cancelled, at which point it triggers a graceful shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	}
}

// --- routing ----------------------------------------------------------------

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// /health is the only unauthenticated route.
	mux.HandleFunc("GET /health", s.handleHealth)

	// Authenticated routes via subhandler. Pattern-routing requires Go 1.22+;
	// we're on 1.26.
	mux.Handle("GET /status", s.auth(s.handleStatus))
	mux.Handle("POST /start", s.auth(s.handleStart))
	mux.Handle("POST /stop", s.auth(s.handleStop))
	mux.Handle("POST /speed", s.auth(s.handleSpeed))
	mux.Handle("POST /pref/start-speed", s.auth(s.handlePrefStartSpeed))
	mux.Handle("GET /sessions", s.auth(s.handleListSessions))
	mux.Handle("GET /sessions/{uuid}", s.auth(s.handleGetSession))
	mux.Handle("GET /summary", s.auth(s.handleSummary))
	mux.Handle("POST /sync/argo", s.auth(s.handleSyncArgo))

	return logRequest(s.deps.Logger, mux)
}

// auth wraps a handler with the Bearer-token check. When cfg.Token is empty
// the middleware is a no-op and the handler is called directly.
func (s *Server) auth(h http.HandlerFunc) http.Handler {
	if s.cfg.Token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.cfg.Token
		if got != want {
			writeError(w, http.StatusUnauthorized, "missing or bad bearer token")
			return
		}
		h(w, r)
	})
}

// logRequest is a tiny access-log middleware. It uses the daemon logger at
// info level so requests appear in both the JSONL sink and pretty stderr.
func logRequest(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
