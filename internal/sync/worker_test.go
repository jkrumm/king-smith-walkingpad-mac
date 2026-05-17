package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// --- fakeStore --------------------------------------------------------------

type fakeStore struct {
	mu                 sync.Mutex
	pending            []store.Session
	marked             map[string]time.Time
	pendingTombstones  []store.Tombstone
	markedTombstones   map[string]time.Time
	localUUIDs         map[string]struct{}
	writtenTombstones  map[string]time.Time
	listErr            error
	markErr            error
	listTombErr        error
	markTombErr        error
	listUUIDsErr       error
	listCalls          atomic.Int32
	listTombstoneCalls atomic.Int32
}

func newFakeStore(sessions ...store.Session) *fakeStore {
	return &fakeStore{
		pending:           sessions,
		marked:            map[string]time.Time{},
		markedTombstones:  map[string]time.Time{},
		localUUIDs:        map[string]struct{}{},
		writtenTombstones: map[string]time.Time{},
	}
}

func (f *fakeStore) seedLocalUUID(uuids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range uuids {
		f.localUUIDs[u] = struct{}{}
	}
}

func (f *fakeStore) AllSessionUUIDs(_ context.Context) (map[string]struct{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listUUIDsErr != nil {
		return nil, f.listUUIDsErr
	}
	out := make(map[string]struct{}, len(f.localUUIDs))
	for u := range f.localUUIDs {
		out[u] = struct{}{}
	}
	return out, nil
}

func (f *fakeStore) WriteTombstone(_ context.Context, uuid string, deletedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writtenTombstones[uuid] = deletedAt
	// Auto-queue so a follow-up flush picks it up.
	f.pendingTombstones = append(f.pendingTombstones, store.Tombstone{UUID: uuid, DeletedAt: deletedAt})
	return nil
}

func (f *fakeStore) writtenTombstoneUUIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.writtenTombstones))
	for u := range f.writtenTombstones {
		out = append(out, u)
	}
	return out
}

func (f *fakeStore) UnsyncedSessions(_ context.Context, limit int) ([]store.Session, error) {
	f.listCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	if limit > len(f.pending) {
		limit = len(f.pending)
	}
	out := append([]store.Session(nil), f.pending[:limit]...)
	return out, nil
}

func (f *fakeStore) MarkSynced(_ context.Context, uuid string, syncedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return f.markErr
	}
	f.marked[uuid] = syncedAt
	// Remove from pending so a follow-up flush sees an empty queue.
	kept := f.pending[:0]
	for _, s := range f.pending {
		if s.UUID != uuid {
			kept = append(kept, s)
		}
	}
	f.pending = kept
	return nil
}

func (f *fakeStore) UnsyncedTombstones(_ context.Context, limit int) ([]store.Tombstone, error) {
	f.listTombstoneCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listTombErr != nil {
		return nil, f.listTombErr
	}
	if limit > len(f.pendingTombstones) {
		limit = len(f.pendingTombstones)
	}
	out := append([]store.Tombstone(nil), f.pendingTombstones[:limit]...)
	return out, nil
}

func (f *fakeStore) MarkTombstoneSynced(_ context.Context, uuid string, syncedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markTombErr != nil {
		return f.markTombErr
	}
	f.markedTombstones[uuid] = syncedAt
	kept := f.pendingTombstones[:0]
	for _, t := range f.pendingTombstones {
		if t.UUID != uuid {
			kept = append(kept, t)
		}
	}
	f.pendingTombstones = kept
	return nil
}

func (f *fakeStore) markedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.marked)
}

func (f *fakeStore) markedTombstoneCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.markedTombstones)
}

func (f *fakeStore) queueTombstone(uuid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingTombstones = append(f.pendingTombstones, store.Tombstone{
		UUID:      uuid,
		DeletedAt: time.Now().UTC(),
	})
}

// --- helpers ----------------------------------------------------------------

func sampleSession(uuid string) store.Session {
	end := time.Date(2026, 5, 17, 14, 58, 30, 0, time.UTC)
	return store.Session{
		UUID:        uuid,
		StartedAt:   time.Date(2026, 5, 17, 14, 57, 33, 0, time.UTC),
		EndedAt:     sql.NullTime{Time: end, Valid: true},
		DurationS:   57,
		DistanceM:   40,
		Steps:       89,
		AvgSpeedKmh: 2.48,
		MaxSpeedKmh: 3.2,
		Kcal:        3.21,
		PauseCount:  0,
	}
}

func newWorker(t *testing.T, st storeAPI, srv *httptest.Server) *Worker {
	t.Helper()
	w := &Worker{
		cfg: Config{
			BaseURL:    srv.URL,
			Token:      "test-token",
			BatchSize:  25,
			HTTPClient: srv.Client(),
			UserAgent:  "walkingpad-test",
		},
		st: st,
	}
	w.cfg.withDefaults()
	w.log = newDiscardLogger()
	return w
}

// --- tests ------------------------------------------------------------------

func TestWorker_SyncNow_HappyPath_UploadsAndMarks(t *testing.T) {
	st := newFakeStore(sampleSession("uuid-1"), sampleSession("uuid-2"))
	var calls atomic.Int32
	var sawToken atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		sawToken.Store(r.Header.Get("Authorization"))
		if r.Method != http.MethodPost || r.URL.Path != "/walking-pad/sessions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body sessionPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.UUID == "" {
			t.Error("missing uuid in payload")
		}
		if body.StartedAt != "2026-05-17T14:57:33Z" {
			t.Errorf("started_at not RFC3339-UTC: %q", body.StartedAt)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	synced, failed, err := worker.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow err: %v", err)
	}
	if synced != 2 || failed != 0 {
		t.Errorf("counts: synced=%d failed=%d, want 2/0", synced, failed)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("HTTP calls = %d, want 2", got)
	}
	if got := sawToken.Load(); got != "Bearer test-token" {
		t.Errorf("auth header = %q, want Bearer test-token", got)
	}
	if st.markedCount() != 2 {
		t.Errorf("marked %d, want 2", st.markedCount())
	}
}

func TestWorker_SyncNow_EmptyQueue_NoHTTP(t *testing.T) {
	st := newFakeStore()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	synced, failed, err := worker.SyncNow(context.Background())
	if err != nil || synced != 0 || failed != 0 {
		t.Errorf("expected zero counts no error, got synced=%d failed=%d err=%v", synced, failed, err)
	}
	if calls.Load() != 0 {
		t.Errorf("server got %d calls, want 0", calls.Load())
	}
}

func TestWorker_SyncNow_5xxLeavesPending(t *testing.T) {
	st := newFakeStore(sampleSession("uuid-fail"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream is down")
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	synced, failed, err := worker.SyncNow(context.Background())
	if err == nil {
		t.Error("expected error on 5xx")
	}
	if synced != 0 || failed != 1 {
		t.Errorf("counts: synced=%d failed=%d, want 0/1", synced, failed)
	}
	if st.markedCount() != 0 {
		t.Errorf("must not mark on failure: marked=%d", st.markedCount())
	}

	// A second SyncNow should still see the pending row.
	if _, _, _ = worker.SyncNow(context.Background()); st.markedCount() != 0 {
		t.Error("row should still be pending after retry against failing server")
	}
}

func TestWorker_SyncNow_401PropagatesError(t *testing.T) {
	st := newFakeStore(sampleSession("uuid-bad-token"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "Unauthorized")
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	_, failed, err := worker.SyncNow(context.Background())
	if err == nil {
		t.Error("401 must surface as an error")
	}
	if failed != 1 {
		t.Errorf("failed=%d, want 1", failed)
	}
}

func TestWorker_SyncNow_BatchSizeCaps(t *testing.T) {
	// 30 pending sessions, batch size 10 — one SyncNow must upload exactly 10.
	pending := make([]store.Session, 30)
	for i := range pending {
		pending[i] = sampleSession("uuid-" + string(rune('a'+i)))
	}
	st := newFakeStore(pending...)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	worker.cfg.BatchSize = 10
	synced, _, _ := worker.SyncNow(context.Background())
	if synced != 10 {
		t.Errorf("synced=%d, want 10", synced)
	}
	if calls.Load() != 10 {
		t.Errorf("http calls=%d, want 10", calls.Load())
	}
}

func TestWorker_Run_FirstFlushImmediate_ThenTicks(t *testing.T) {
	st := newFakeStore(sampleSession("immediate"))
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	worker.cfg.PollInterval = time.Hour // disable subsequent ticks

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = worker.Run(ctx)
		close(done)
	}()

	// Wait until the immediate flush has happened.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Errorf("immediate flush didn't fire: calls=%d", calls.Load())
	}
	cancel()
	<-done
}

func TestWorker_Run_RespectsContextCancellation(t *testing.T) {
	st := newFakeStore()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	worker.cfg.PollInterval = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestWorker_Upload_RefusesUnclosedSession(t *testing.T) {
	st := newFakeStore()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("upload of unclosed session must not hit the network")
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	open := sampleSession("never-ended")
	open.EndedAt = sql.NullTime{}
	if err := worker.upload(context.Background(), open); err == nil {
		t.Error("expected error uploading session with no ended_at")
	}
}

func TestWorker_Upload_BaseURLTrailingSlash(t *testing.T) {
	// Defensive: argo URLs commonly come configured with trailing slashes.
	st := newFakeStore(sampleSession("uuid-trail"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/walking-pad/sessions" {
			t.Errorf("path = %q, want /walking-pad/sessions", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	worker.cfg.BaseURL = srv.URL + "/"
	if _, _, err := worker.SyncNow(context.Background()); err != nil {
		t.Errorf("SyncNow err: %v", err)
	}
}

// --- tombstone flush --------------------------------------------------------

func TestWorker_SyncNow_DrainsTombstonesViaDelete(t *testing.T) {
	st := newFakeStore()
	st.queueTombstone("gone-1")
	st.queueTombstone("gone-2")

	var deleteCalls atomic.Int32
	var sawToken atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if want := "/walking-pad/sessions/"; len(r.URL.Path) <= len(want) || r.URL.Path[:len(want)] != want {
			t.Errorf("path = %q, want /walking-pad/sessions/...", r.URL.Path)
		}
		sawToken.Store(r.Header.Get("Authorization"))
		deleteCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	synced, failed, err := worker.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow err: %v", err)
	}
	if synced != 2 || failed != 0 {
		t.Errorf("counts: synced=%d failed=%d, want 2/0", synced, failed)
	}
	if got := deleteCalls.Load(); got != 2 {
		t.Errorf("DELETE calls = %d, want 2", got)
	}
	if got := sawToken.Load(); got != "Bearer test-token" {
		t.Errorf("auth header = %q, want Bearer test-token", got)
	}
	if st.markedTombstoneCount() != 2 {
		t.Errorf("marked tombstones = %d, want 2", st.markedTombstoneCount())
	}
}

func TestWorker_SyncNow_TombstoneDelete5xxLeavesPending(t *testing.T) {
	st := newFakeStore()
	st.queueTombstone("flaky")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	_, failed, err := worker.SyncNow(context.Background())
	if err == nil {
		t.Error("expected error on 5xx for tombstone")
	}
	if failed != 1 {
		t.Errorf("failed=%d, want 1", failed)
	}
	if st.markedTombstoneCount() != 0 {
		t.Error("must not mark tombstone synced on 5xx")
	}
}

func TestWorker_SyncNow_TombstonesAndSessionsInSameFlush(t *testing.T) {
	// One session to upload + one tombstone to delete in the same SyncNow call.
	st := newFakeStore(sampleSession("uploads"))
	st.queueTombstone("deletes")

	var posts, deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	worker := newWorker(t, st, srv)
	synced, failed, err := worker.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow err: %v", err)
	}
	if synced != 2 || failed != 0 {
		t.Errorf("counts: synced=%d failed=%d, want 2/0", synced, failed)
	}
	if posts.Load() != 1 || deletes.Load() != 1 {
		t.Errorf("posts=%d deletes=%d, want 1/1", posts.Load(), deletes.Load())
	}
	if st.markedCount() != 1 || st.markedTombstoneCount() != 1 {
		t.Errorf("marks: sessions=%d tombs=%d, want 1/1", st.markedCount(), st.markedTombstoneCount())
	}
}
