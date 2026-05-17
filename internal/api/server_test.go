package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/session"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// --- fakes ------------------------------------------------------------------

type fakeController struct {
	mu        sync.Mutex
	startVals []float64
	stopCalls int
	speedVals []float64
	prefVals  []float64
	err       error
}

func (f *fakeController) Start(_ context.Context, s float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.startVals = append(f.startVals, s)
	return nil
}

func (f *fakeController) Stop(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.stopCalls++
	return nil
}

func (f *fakeController) SetSpeed(_ context.Context, s float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.speedVals = append(f.speedVals, s)
	return nil
}

func (f *fakeController) SetStartSpeed(_ context.Context, s float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.prefVals = append(f.prefVals, s)
	return nil
}

type fakeStatus struct {
	frame     ble.Status
	observed  time.Time
	connected bool
	dev       DeviceInfo
}

func (f *fakeStatus) LastFrame() (ble.Status, time.Time, bool) {
	return f.frame, f.observed, f.connected
}
func (f *fakeStatus) DeviceInfo() DeviceInfo { return f.dev }

type fakeSyncer struct {
	synced, failed int
	err            error
}

func (f *fakeSyncer) SyncNow(context.Context) (int, int, error) {
	return f.synced, f.failed, f.err
}

// --- test rig --------------------------------------------------------------

type rig struct {
	srv    *httptest.Server
	store  *store.Store
	mgr    *session.Manager
	ctrl   *fakeController
	status *fakeStatus
	syncer *fakeSyncer
	token  string
}

func newRig(t *testing.T, token string) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.sqlite"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	mgr := session.NewManager(session.Config{GapMinutes: 1, ResumeWithinSeconds: 10, WeightKg: 80}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctrl := &fakeController{}
	statusFake := &fakeStatus{}
	syncerFake := &fakeSyncer{}

	s := New(Config{Addr: "127.0.0.1:0", Token: token, Version: "test"}, Deps{
		Store:      st,
		Manager:    mgr,
		Controller: ctrl,
		Status:     statusFake,
		Syncer:     syncerFake,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(s.Handler())

	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return &rig{srv: srv, store: st, mgr: mgr, ctrl: ctrl, status: statusFake, syncer: syncerFake, token: token}
}

// do issues the request and fully drains the response, returning status code
// and body bytes. The helper owns the response lifecycle so callers (and the
// bodyclose linter) never see a *http.Response.
func (r *rig) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, r.srv.URL+path, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// rawDo bypasses the rig's bearer-token injection — used by the auth-rejection
// tests. Same closed-body discipline as do().
func (r *rig) rawDo(t *testing.T, method, path string) int {
	t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("raw %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// --- /health ---------------------------------------------------------------

func TestHealth_AlwaysOpen(t *testing.T) {
	r := newRig(t, "secret-token")
	// /health must not require auth.
	resp, err := http.Get(r.srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Version != "test" {
		t.Errorf("health body = %+v", body)
	}
}

// --- auth -------------------------------------------------------------------

func TestAuth_RejectsMissingBearer(t *testing.T) {
	r := newRig(t, "secret-token")
	if got := r.rawDo(t, "GET", "/status"); got != 401 {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestAuth_AcceptsCorrectBearer(t *testing.T) {
	r := newRig(t, "secret-token")
	if got, _ := r.do(t, "GET", "/status", nil); got != 200 {
		t.Errorf("status = %d, want 200", got)
	}
}

func TestAuth_DisabledWhenTokenEmpty(t *testing.T) {
	r := newRig(t, "")
	if got := r.rawDo(t, "GET", "/status"); got != 200 {
		t.Errorf("status = %d, want 200 (no auth required)", got)
	}
}

// --- /status ---------------------------------------------------------------

func TestStatus_DisconnectedShape(t *testing.T) {
	r := newRig(t, "")
	code, body := r.do(t, "GET", "/status", nil)
	if code != 200 {
		t.Fatalf("status = %d body=%s", code, body)
	}
	var got statusResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Connected {
		t.Error("connected should be false")
	}
	if got.CurrentSession != nil {
		t.Error("current_session should be nil")
	}
}

func TestStatus_ConnectedWithOpenSession(t *testing.T) {
	r := newRig(t, "")
	r.status.connected = true
	r.status.observed = time.Now().UTC()
	r.status.frame = ble.Status{State: ble.BeltActive, Mode: ble.ModeManual, SpeedKmh: 4.5}
	r.status.dev = DeviceInfo{Name: "WalkingPad", Address: "AA:BB", RSSI: -56}

	// Open a session via the manager.
	now := time.Now().UTC()
	if err := r.mgr.Ingest(context.Background(),
		ble.Status{State: ble.BeltActive, Mode: ble.ModeManual, SpeedKmh: 4.0}, now); err != nil {
		t.Fatal(err)
	}

	_, body := r.do(t, "GET", "/status", nil)
	var got statusResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if !got.Connected || got.BeltState != "active" || got.SpeedKmh != 4.5 {
		t.Errorf("connected fields wrong: %+v", got)
	}
	if got.CurrentSession == nil {
		t.Fatal("current_session should be set")
	}
	if got.Device.Name != "WalkingPad" || got.Device.RSSI != -56 {
		t.Errorf("device wrong: %+v", got.Device)
	}
}

// --- /start, /stop, /speed, /pref/start-speed -----------------------------

func TestStart_WithSpeed(t *testing.T) {
	r := newRig(t, "")
	code, body := r.do(t, "POST", "/start", map[string]any{"speed_kmh": 3.55})
	if code != 200 {
		t.Fatalf("status = %d body=%s", code, body)
	}
	if len(r.ctrl.startVals) != 1 || r.ctrl.startVals[0] != 3.6 {
		t.Errorf("Start vals = %v, want [3.6]", r.ctrl.startVals)
	}
}

func TestStart_NoBody(t *testing.T) {
	r := newRig(t, "")
	if got, _ := r.do(t, "POST", "/start", nil); got != 200 {
		t.Fatalf("status = %d", got)
	}
	if len(r.ctrl.startVals) != 1 || r.ctrl.startVals[0] != 0 {
		t.Errorf("Start vals = %v, want [0] (no-speed start)", r.ctrl.startVals)
	}
}

// /start on an already-running belt must NOT fire the start sequence — that
// halts the belt on real P1 hardware. With a speed it adjusts; without one it
// returns 200 and does nothing.
func TestStart_AlreadyRunning_AdjustsSpeed(t *testing.T) {
	r := newRig(t, "")
	r.status.connected = true
	r.status.frame.State = ble.BeltActive
	if got, _ := r.do(t, "POST", "/start", map[string]any{"speed_kmh": 3.0}); got != 200 {
		t.Fatalf("status = %d", got)
	}
	if len(r.ctrl.startVals) != 0 {
		t.Errorf("Start should NOT be called when belt is running, got %v", r.ctrl.startVals)
	}
	if len(r.ctrl.speedVals) != 1 || r.ctrl.speedVals[0] != 3.0 {
		t.Errorf("SetSpeed vals = %v, want [3.0]", r.ctrl.speedVals)
	}
}

func TestStart_AlreadyRunning_NoSpeed_NoOp(t *testing.T) {
	r := newRig(t, "")
	r.status.connected = true
	r.status.frame.State = ble.BeltActive
	if got, _ := r.do(t, "POST", "/start", nil); got != 200 {
		t.Fatalf("status = %d", got)
	}
	if len(r.ctrl.startVals) != 0 || len(r.ctrl.speedVals) != 0 {
		t.Errorf("expected no controller calls, got start=%v speed=%v",
			r.ctrl.startVals, r.ctrl.speedVals)
	}
}

func TestStop(t *testing.T) {
	r := newRig(t, "")
	if got, _ := r.do(t, "POST", "/stop", nil); got != 200 {
		t.Fatalf("status = %d", got)
	}
	if r.ctrl.stopCalls != 1 {
		t.Errorf("Stop calls = %d, want 1", r.ctrl.stopCalls)
	}
}

func TestSpeed_ValidRoundsToTenth(t *testing.T) {
	r := newRig(t, "")
	if got, _ := r.do(t, "POST", "/speed", map[string]any{"speed_kmh": 4.46}); got != 200 {
		t.Fatalf("status = %d", got)
	}
	if len(r.ctrl.speedVals) != 1 || r.ctrl.speedVals[0] != 4.5 {
		t.Errorf("SetSpeed vals = %v, want [4.5]", r.ctrl.speedVals)
	}
}

func TestSpeed_RejectsOutOfRange(t *testing.T) {
	r := newRig(t, "")
	for _, bad := range []float64{0.4, 6.5, -1} {
		code, body := r.do(t, "POST", "/speed", map[string]any{"speed_kmh": bad})
		if code != 400 {
			t.Errorf("speed=%g: status = %d, want 400; body=%s", bad, code, body)
		}
	}
	if len(r.ctrl.speedVals) != 0 {
		t.Errorf("SetSpeed should not be called on rejected inputs, got %v", r.ctrl.speedVals)
	}
}

func TestSpeed_ControllerUnavailable(t *testing.T) {
	r := newRig(t, "")
	r.ctrl.err = ErrControllerUnavailable
	if got, _ := r.do(t, "POST", "/speed", map[string]any{"speed_kmh": 3.0}); got != 503 {
		t.Errorf("status = %d, want 503", got)
	}
}

func TestPrefStartSpeed(t *testing.T) {
	r := newRig(t, "")
	if got, _ := r.do(t, "POST", "/pref/start-speed", map[string]any{"speed_kmh": 2.0}); got != 200 {
		t.Fatalf("status = %d", got)
	}
	if len(r.ctrl.prefVals) != 1 || r.ctrl.prefVals[0] != 2.0 {
		t.Errorf("SetStartSpeed vals = %v, want [2.0]", r.ctrl.prefVals)
	}
}

// --- /sessions list + get --------------------------------------------------

func TestListSessions_EmptyAndPopulated(t *testing.T) {
	r := newRig(t, "")

	code, body := r.do(t, "GET", "/sessions", nil)
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	var empty sessionsListResponse
	_ = json.Unmarshal(body, &empty)
	if empty.Sessions == nil {
		t.Error("sessions must be [] not null")
	}

	// Insert two sessions.
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	id1, _ := r.store.OpenSession(ctx, "u1", base)
	_ = r.store.CloseSession(ctx, id1, base.Add(10*time.Minute), store.SessionTotals{DurationS: 600})
	id2, _ := r.store.OpenSession(ctx, "u2", base.Add(1*time.Hour))
	_ = r.store.CloseSession(ctx, id2, base.Add(1*time.Hour+5*time.Minute), store.SessionTotals{DurationS: 300})

	_, body = r.do(t, "GET", "/sessions", nil)
	var got sessionsListResponse
	_ = json.Unmarshal(body, &got)
	if len(got.Sessions) != 2 || got.Sessions[0].UUID != "u2" {
		t.Errorf("sessions order/count wrong: %+v", got)
	}
}

func TestListSessions_LimitAndBefore(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_, _ = r.store.OpenSession(ctx, "u"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Hour))
	}
	code, body := r.do(t, "GET", "/sessions?limit=2", nil)
	if code != 200 {
		t.Fatalf("status = %d body=%s", code, body)
	}
	var got sessionsListResponse
	_ = json.Unmarshal(body, &got)
	if len(got.Sessions) != 2 {
		t.Errorf("limit=2 returned %d", len(got.Sessions))
	}

	// Bad limit.
	if c, _ := r.do(t, "GET", "/sessions?limit=nope", nil); c != 400 {
		t.Errorf("bad limit: status = %d, want 400", c)
	}
	// Bad before.
	if c, _ := r.do(t, "GET", "/sessions?before=not-a-time", nil); c != 400 {
		t.Errorf("bad before: status = %d, want 400", c)
	}
}

func TestGetSession_HitAndMiss(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	now := time.Now().UTC()
	id, _ := r.store.OpenSession(ctx, "abc", now)
	_, _ = r.store.AppendSample(ctx, store.Sample{
		SessionID: id, Ts: now.Add(time.Second), BeltState: int(ble.BeltActive),
		SpeedKmh: 4, DistanceM: 5, Steps: 6,
	})

	code, body := r.do(t, "GET", "/sessions/abc", nil)
	if code != 200 {
		t.Fatalf("status = %d body=%s", code, body)
	}
	var got sessionDetailResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Session.UUID != "abc" || len(got.Samples) != 1 || got.Samples[0].SpeedKmh != 4 {
		t.Errorf("detail wrong: %+v", got)
	}

	if c, _ := r.do(t, "GET", "/sessions/missing", nil); c != 404 {
		t.Errorf("missing: status = %d, want 404", c)
	}
}

// --- /summary --------------------------------------------------------------

func TestSummary_DefaultsToToday(t *testing.T) {
	r := newRig(t, "")
	code, body := r.do(t, "GET", "/summary", nil)
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	var got summaryResponse
	_ = json.Unmarshal(body, &got)
	if got.Period != "today" {
		t.Errorf("default period = %q, want today", got.Period)
	}
}

func TestSummary_UnknownPeriodRejected(t *testing.T) {
	r := newRig(t, "")
	code, body := r.do(t, "GET", "/summary?period=year", nil)
	if code != 400 {
		t.Errorf("unknown period: status = %d body=%s", code, body)
	}
}

// --- /sync/argo ------------------------------------------------------------

func TestSyncArgo_DisabledIs503(t *testing.T) {
	r := newRig(t, "")
	r.syncer.err = ErrSyncDisabled
	code, body := r.do(t, "POST", "/sync/argo", nil)
	if code != 503 {
		t.Errorf("status = %d body=%s, want 503", code, body)
	}
}

func TestSyncArgo_Counts(t *testing.T) {
	r := newRig(t, "")
	r.syncer.synced, r.syncer.failed = 7, 2
	code, body := r.do(t, "POST", "/sync/argo", nil)
	if code != 200 {
		t.Fatalf("status = %d body=%s", code, body)
	}
	var got syncResponse
	_ = json.Unmarshal(body, &got)
	if got.Synced != 7 || got.Failed != 2 {
		t.Errorf("counts wrong: %+v", got)
	}
}

// --- JSON hygiene ----------------------------------------------------------

func TestDecodeJSON_RejectsUnknownFields(t *testing.T) {
	r := newRig(t, "")
	// The strings/http imports stay used via the raw client path below.
	body := strings.NewReader(`{"speed_kmh": 3.0, "extra": "nope"}`)
	resp, err := http.Post(r.srv.URL+"/speed", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for unknown fields", resp.StatusCode)
	}
}
