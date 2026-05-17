package sync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/session"
)

// --- fakeManager -----------------------------------------------------------

type fakeManager struct {
	mu  sync.Mutex
	cur *session.CurrentSessionView
}

func (f *fakeManager) CurrentSession() *session.CurrentSessionView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}

func (f *fakeManager) set(v *session.CurrentSessionView) {
	f.mu.Lock()
	f.cur = v
	f.mu.Unlock()
}

// --- recorder server -------------------------------------------------------

type recordedPost struct {
	auth        string
	contentType string
	body        liveSnapshotPayload
	rawBody     []byte
}

type recorder struct {
	mu     sync.Mutex
	posts  []recordedPost
	status int
}

func newRecorder() *recorder { return &recorder{status: http.StatusNoContent} }

func (r *recorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/walking-pad/live" {
			t.Errorf("unexpected path %q", req.URL.Path)
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(req.Body)
		var body liveSnapshotPayload
		_ = json.Unmarshal(raw, &body)
		r.mu.Lock()
		r.posts = append(r.posts, recordedPost{
			auth:        req.Header.Get("Authorization"),
			contentType: req.Header.Get("Content-Type"),
			body:        body,
			rawBody:     raw,
		})
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.posts)
}

func (r *recorder) snapshot() []recordedPost {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedPost, len(r.posts))
	copy(out, r.posts)
	return out
}

func newTestLiveWorker(t *testing.T, mgr managerAPI, baseURL string) *LiveWorker {
	t.Helper()
	cfg := LiveConfig{
		BaseURL:      baseURL,
		Token:        "test-token",
		UserAgent:    "live-test/1",
		PushInterval: 20 * time.Millisecond,
		HTTPClient:   &http.Client{Timeout: 1 * time.Second},
	}
	cfg.withDefaults()
	w := &LiveWorker{cfg: cfg, mgr: mgr, log: newDiscardLogger().With("component", "live")}
	return w
}

// --- tick branches ---------------------------------------------------------

func TestLive_Tick_ActiveSessionPostsSnapshot(t *testing.T) {
	rec := newRecorder()
	srv := rec.server(t)

	mgr := &fakeManager{}
	mgr.set(&session.CurrentSessionView{
		UUID:            "abc-123",
		StartedAt:       time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC),
		DurationS:       42,
		DistanceM:       321.5,
		Steps:           400,
		Kcal:            18.0,
		AvgSpeedKmh:     3.5,
		MaxSpeedKmh:     4.0,
		CurrentSpeedKmh: 3.0,
		Paused:          false,
		PauseCount:      0,
	})

	w := newTestLiveWorker(t, mgr, srv.URL)
	w.tick(context.Background())

	posts := rec.snapshot()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	got := posts[0]
	if got.auth != "Bearer test-token" {
		t.Errorf("auth = %q", got.auth)
	}
	if got.contentType != "application/json" {
		t.Errorf("content-type = %q", got.contentType)
	}
	if got.body.UUID != "abc-123" || got.body.State != "active" {
		t.Errorf("body = %+v", got.body)
	}
	if got.body.DurationS != 42 || got.body.CurrentSpeedKmh != 3.0 {
		t.Errorf("totals not propagated: %+v", got.body)
	}
	if w.lastPushedUUID != "abc-123" {
		t.Errorf("lastPushedUUID = %q, want abc-123", w.lastPushedUUID)
	}
}

func TestLive_Tick_PausedSessionMarksState(t *testing.T) {
	rec := newRecorder()
	srv := rec.server(t)
	mgr := &fakeManager{}
	mgr.set(&session.CurrentSessionView{UUID: "p1", Paused: true})

	w := newTestLiveWorker(t, mgr, srv.URL)
	w.tick(context.Background())

	posts := rec.snapshot()
	if len(posts) != 1 || posts[0].body.State != "paused" {
		t.Fatalf("expected paused snapshot, got %+v", posts)
	}
}

func TestLive_Tick_SessionDisappearsPostsTombstone(t *testing.T) {
	rec := newRecorder()
	srv := rec.server(t)
	mgr := &fakeManager{}

	w := newTestLiveWorker(t, mgr, srv.URL)
	w.lastPushedUUID = "old-uuid"
	w.tick(context.Background())

	posts := rec.snapshot()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1 (tombstone)", len(posts))
	}
	if posts[0].body.UUID != "old-uuid" || posts[0].body.State != "ended" {
		t.Errorf("tombstone shape wrong: %+v", posts[0].body)
	}
	if w.lastPushedUUID != "" {
		t.Errorf("lastPushedUUID should reset, got %q", w.lastPushedUUID)
	}
}

func TestLive_Tick_IdleNoSessionDoesNothing(t *testing.T) {
	rec := newRecorder()
	srv := rec.server(t)
	mgr := &fakeManager{}

	w := newTestLiveWorker(t, mgr, srv.URL)
	w.tick(context.Background())

	if rec.count() != 0 {
		t.Errorf("idle worker should not POST, got %d", rec.count())
	}
}

// --- Run loop --------------------------------------------------------------

func TestLive_Run_StopsOnContextCancel(t *testing.T) {
	rec := newRecorder()
	srv := rec.server(t)
	mgr := &fakeManager{}
	mgr.set(&session.CurrentSessionView{UUID: "u1", StartedAt: time.Now().UTC()})

	w := newTestLiveWorker(t, mgr, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Let it push a few times.
	time.Sleep(80 * time.Millisecond)
	beforeCancel := rec.count()
	if beforeCancel < 1 {
		t.Fatalf("expected at least 1 push before cancel, got %d", beforeCancel)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of cancel")
	}

	// Final tombstone fires from the shutdown branch when a session was
	// active mid-run.
	posts := rec.snapshot()
	var tombstone *recordedPost
	for i := range posts {
		if posts[i].body.State == "ended" {
			tombstone = &posts[i]
			break
		}
	}
	if tombstone == nil {
		t.Error("expected an `ended` tombstone after ctx cancel")
	} else if tombstone.body.UUID != "u1" {
		t.Errorf("tombstone uuid = %q, want u1", tombstone.body.UUID)
	}
}

func TestLive_Run_NoTombstoneWhenIdleAtShutdown(t *testing.T) {
	rec := newRecorder()
	srv := rec.server(t)
	mgr := &fakeManager{}

	w := newTestLiveWorker(t, mgr, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if rec.count() != 0 {
		t.Errorf("idle worker should not POST during run, got %d", rec.count())
	}
}

// --- failure modes ---------------------------------------------------------

func TestLive_PushFailureDoesNotPanic(t *testing.T) {
	rec := newRecorder()
	rec.status = http.StatusInternalServerError
	srv := rec.server(t)
	mgr := &fakeManager{}
	mgr.set(&session.CurrentSessionView{UUID: "u1"})

	w := newTestLiveWorker(t, mgr, srv.URL)
	w.tick(context.Background())

	// Even on 5xx the worker should have recorded the attempt without panicking.
	if rec.count() != 1 {
		t.Errorf("post count = %d, want 1 (attempt made)", rec.count())
	}
	// lastPushedUUID is set even when the push fails — that's intentional: the
	// next idle tick still needs to know we *intended* this session, so the
	// tombstone fires when it goes away.
	if w.lastPushedUUID != "u1" {
		t.Errorf("lastPushedUUID = %q, want u1", w.lastPushedUUID)
	}
}

// pushCounter sanity-checks that we don't double-count via the test recorder
// itself — used as a guard for the assertions above.
func TestLive_RecorderCountsCorrectly(t *testing.T) {
	var n atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/walking-pad/live", func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mgr := &fakeManager{}
	mgr.set(&session.CurrentSessionView{UUID: "u1"})
	w := newTestLiveWorker(t, mgr, srv.URL)
	for i := 0; i < 3; i++ {
		w.tick(context.Background())
	}
	if got := n.Load(); got != 3 {
		t.Errorf("server saw %d posts, want 3", got)
	}
}
