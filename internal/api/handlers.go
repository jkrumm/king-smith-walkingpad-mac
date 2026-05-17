package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// --- /health ----------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{OK: true, Version: s.cfg.Version})
}

// --- /status ----------------------------------------------------------------

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	frame, observedAt, connected := s.deps.Status.LastFrame()
	dev := s.deps.Status.DeviceInfo()
	cur := s.deps.Manager.CurrentSession()

	resp := statusResponse{
		Connected: connected,
		Device:    deviceJSON(dev),
	}
	if connected {
		resp.BeltState = frame.State.String()
		resp.Mode = frame.Mode.String()
		resp.SpeedKmh = frame.SpeedKmh
		if !observedAt.IsZero() {
			resp.ObservedAt = observedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	if cur != nil {
		resp.CurrentSession = &currentSessionJSON{
			UUID:        cur.UUID,
			StartedAt:   cur.StartedAt.UTC().Format(time.RFC3339Nano),
			DurationS:   cur.DurationS,
			DistanceM:   cur.DistanceM,
			Steps:       cur.Steps,
			Kcal:        cur.Kcal,
			AvgSpeedKmh: cur.AvgSpeedKmh,
			MaxSpeedKmh: cur.MaxSpeedKmh,
			// Samples ring is wired up in step 6 alongside the BLE loop.
			Samples: []sampleJSON{},
		}
	}

	today, err := s.deps.Store.Summary(r.Context(), store.PeriodToday, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("today summary: %v", err))
		return
	}
	resp.Today = summaryJSON{
		DurationS: today.DurationS,
		DistanceM: today.DistanceM,
		Steps:     today.Steps,
		Kcal:      today.Kcal,
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- write endpoints --------------------------------------------------------

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeOptionalSpeed(w, r)
	if !ok {
		return
	}
	// speed = 0 is the explicit "just start, don't reset speed" path.
	speed := 0.0
	if body != nil {
		v, ok := validatedSpeed(w, *body)
		if !ok {
			return
		}
		speed = v
	}
	if err := s.deps.Controller.Start(r.Context(), speed); err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Controller.Stop(r.Context()); err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func (s *Server) handleSpeed(w http.ResponseWriter, r *http.Request) {
	var body speedRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	speed, ok := validatedSpeed(w, body.SpeedKmh)
	if !ok {
		return
	}
	if err := s.deps.Controller.SetSpeed(r.Context(), speed); err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func (s *Server) handlePrefStartSpeed(w http.ResponseWriter, r *http.Request) {
	var body speedRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	speed, ok := validatedSpeed(w, body.SpeedKmh)
	if !ok {
		return
	}
	if err := s.deps.Controller.SetStartSpeed(r.Context(), speed); err != nil {
		writeControllerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

// --- session reads ---------------------------------------------------------

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	var before time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be RFC3339")
			return
		}
		before = t
	}

	sessions, err := s.deps.Store.ListSessions(r.Context(), limit, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := sessionsListResponse{Sessions: make([]sessionJSON, 0, len(sessions))}
	for _, ss := range sessions {
		out.Sessions = append(out.Sessions, sessionToJSON(ss))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	if uuid == "" {
		writeError(w, http.StatusBadRequest, "uuid required")
		return
	}
	sess, samples, err := s.deps.Store.GetSession(r.Context(), uuid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := sessionDetailResponse{Session: sessionToJSON(sess), Samples: make([]sampleJSON, 0, len(samples))}
	for _, smp := range samples {
		out.Samples = append(out.Samples, sampleJSON{
			Ts:        smp.Ts.UTC().Format(time.RFC3339Nano),
			BeltState: ble.BeltState(smp.BeltState).String(), //nolint:gosec // wire byte 0..9
			SpeedKmh:  smp.SpeedKmh,
			DistanceM: smp.DistanceM,
			Steps:     smp.Steps,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// --- /summary --------------------------------------------------------------

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	p := store.Period(r.URL.Query().Get("period"))
	if p == "" {
		p = store.PeriodToday
	}
	sum, err := s.deps.Store.Summary(r.Context(), p, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summaryResponse{
		Period:    string(p),
		Sessions:  sum.Sessions,
		DurationS: sum.DurationS,
		DistanceM: sum.DistanceM,
		Steps:     sum.Steps,
		Kcal:      sum.Kcal,
	})
}

// --- /sync/argo ------------------------------------------------------------

func (s *Server) handleSyncArgo(w http.ResponseWriter, r *http.Request) {
	synced, failed, err := s.deps.Syncer.SyncNow(r.Context())
	if err != nil {
		if errors.Is(err, ErrSyncDisabled) {
			writeError(w, http.StatusServiceUnavailable, "argo sync disabled (no token)")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, syncResponse{Synced: synced, Failed: failed})
}

// --- shared --------------------------------------------------------------

// validatedSpeed enforces PRD §8 §POST /speed: 0.5–6.0 inclusive, rounded to
// 0.1. Returns (rounded, true) on success; on failure writes the 400 response
// and returns (_, false).
func validatedSpeed(w http.ResponseWriter, raw float64) (float64, bool) {
	if raw < 0.5 || raw > ble.MaxSpeedKmh {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("speed_kmh must be in [0.5, %.1f]", ble.MaxSpeedKmh))
		return 0, false
	}
	return math.Round(raw*10) / 10, true
}

// decodeOptionalSpeed reads a /start body. The body is optional; a missing or
// empty body returns (nil, true).
func decodeOptionalSpeed(w http.ResponseWriter, r *http.Request) (*float64, bool) {
	if r.ContentLength == 0 {
		return nil, true
	}
	var body speedRequest
	if err := decodeJSON(w, r, &body); err != nil {
		return nil, false
	}
	return &body.SpeedKmh, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeControllerError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrControllerUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "ble controller not connected")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func sessionToJSON(s store.Session) sessionJSON {
	out := sessionJSON{
		UUID:        s.UUID,
		StartedAt:   s.StartedAt.UTC().Format(time.RFC3339Nano),
		DurationS:   s.DurationS,
		DistanceM:   s.DistanceM,
		Steps:       s.Steps,
		AvgSpeedKmh: s.AvgSpeedKmh,
		MaxSpeedKmh: s.MaxSpeedKmh,
		Kcal:        s.Kcal,
		PauseCount:  s.PauseCount,
	}
	if s.EndedAt.Valid {
		t := s.EndedAt.Time.UTC().Format(time.RFC3339Nano)
		out.EndedAt = &t
	}
	if s.SyncedAt.Valid {
		t := s.SyncedAt.Time.UTC().Format(time.RFC3339Nano)
		out.SyncedAt = &t
	}
	return out
}
