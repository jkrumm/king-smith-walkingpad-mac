package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/store"
)

// TestPipeline_StitchOrphansReconciledAndDeleted simulates the exact
// production scenario that left orphans on argo: a stitch ran in the past
// WITHOUT writing tombstones (so local has no record of the merged-away
// rows), but argo still holds them. A startup Reconcile + one sync tick
// must leave argo and local in lockstep.
func TestPipeline_StitchOrphansReconciledAndDeleted(t *testing.T) {
	st := openRealStore(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 17, 20, 0, 0, 0, time.UTC)

	// Seed local with one survivor — the one the user actually kept.
	survivorUUID := "survivor-uuid"
	if _, err := st.OpenSession(ctx, survivorUUID, now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	// Argo state — survivor + 4 legacy orphans (the bug we hit in prod).
	argo := newFakeArgo(t, []string{
		survivorUUID,
		"orphan-1", "orphan-2", "orphan-3", "orphan-4",
	})

	w := newRealWorker(t, st, argo)

	// Step 1: Reconcile finds the 4 orphans and writes tombstones locally.
	orphans, err := w.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if orphans != 4 {
		t.Fatalf("Reconcile orphans = %d, want 4", orphans)
	}
	pending, _ := st.UnsyncedTombstones(ctx, 10)
	if len(pending) != 4 {
		t.Fatalf("pending tombstones = %d, want 4", len(pending))
	}

	// Step 2: SyncNow drains the tombstones via argo DELETE.
	synced, failed, err := w.SyncNow(ctx)
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if synced != 4 || failed != 0 {
		t.Errorf("drain: synced=%d failed=%d, want 4/0", synced, failed)
	}

	// Argo state should now match local: only the survivor remains.
	remaining := argo.uuids()
	if len(remaining) != 1 || remaining[0] != survivorUUID {
		t.Errorf("argo final state = %v, want [%s]", remaining, survivorUUID)
	}

	// Local tombstones should all be marked synced.
	pending, _ = st.UnsyncedTombstones(ctx, 10)
	if len(pending) != 0 {
		t.Errorf("post-drain pending tombstones = %d, want 0", len(pending))
	}
}

// TestPipeline_DropShortAndTombstoneDeliveredToArgo: a session is uploaded,
// later dropped locally (short + window expired), and the resulting
// tombstone reaches argo. End-to-end through the real Store.
func TestPipeline_DropShortAndTombstoneDeliveredToArgo(t *testing.T) {
	st := openRealStore(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	now := base.Add(2 * time.Hour)

	// Seed a short closed session and pretend it was synced to argo.
	id, err := st.OpenSession(ctx, "short-walk", base)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if _, err := st.AppendSample(ctx, store.Sample{
		SessionID: id, Ts: base, BeltState: 2, SpeedKmh: 4.0,
	}); err != nil {
		t.Fatalf("AppendSample: %v", err)
	}
	if _, err := st.AppendSample(ctx, store.Sample{
		SessionID: id, Ts: base.Add(60 * time.Second), BeltState: 0,
	}); err != nil {
		t.Fatalf("AppendSample: %v", err)
	}
	if err := st.CloseSession(ctx, id, base.Add(60*time.Second),
		store.SessionTotals{DurationS: 60, DistanceM: 70}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if err := st.MarkSynced(ctx, "short-walk", base.Add(70*time.Second)); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	argo := newFakeArgo(t, []string{"short-walk"})
	w := newRealWorker(t, st, argo)

	// Step 1: drop pass deletes locally + tombstone.
	dropped, err := st.DropShortStandaloneSessions(ctx, 5*time.Minute, 30*time.Minute, now)
	if err != nil {
		t.Fatalf("DropShortStandaloneSessions: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "short-walk" {
		t.Fatalf("dropped = %v, want [short-walk]", dropped)
	}

	// Step 2: sync drains the tombstone, argo is empty.
	if _, _, err := w.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if got := argo.uuids(); len(got) != 0 {
		t.Errorf("argo final state = %v, want []", got)
	}
}

// --- helpers ----------------------------------------------------------------

func openRealStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pipeline.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newRealWorker(t *testing.T, st *store.Store, argo *fakeArgo) *Worker {
	t.Helper()
	w := &Worker{
		cfg: Config{
			BaseURL:    argo.srv.URL,
			Token:      "test-token",
			BatchSize:  25,
			HTTPClient: argo.srv.Client(),
			UserAgent:  "walkingpad-pipeline-test",
		},
		st: st,
	}
	w.cfg.withDefaults()
	w.log = newDiscardLogger()
	return w
}

// fakeArgo serves both the paginated GET and the idempotent DELETE so the
// pipeline test can exercise the same surface the daemon uses in production.
type fakeArgo struct {
	mu  sync.Mutex
	srv *httptest.Server
	set map[string]struct{}
}

func newFakeArgo(t *testing.T, seed []string) *fakeArgo {
	t.Helper()
	a := &fakeArgo{set: map[string]struct{}{}}
	for _, u := range seed {
		a.set[u] = struct{}{}
	}
	a.srv = httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeArgo) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/walking-pad/sessions":
		a.handleList(w, r)
	case r.Method == http.MethodDelete && len(r.URL.Path) > len("/walking-pad/sessions/"):
		a.handleDelete(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/walking-pad/sessions":
		a.handleUpsert(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *fakeArgo) handleList(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	all := make([]string, 0, len(a.set))
	for u := range a.set {
		all = append(all, u)
	}
	start := (page - 1) * limit
	end := start + limit
	if start > len(all) {
		start = len(all)
	}
	if end > len(all) {
		end = len(all)
	}
	refs := make([]argoSessionRef, 0, end-start)
	for _, u := range all[start:end] {
		refs = append(refs, argoSessionRef{UUID: u})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(argoSessionsPage{Data: refs, Total: len(all)})
}

func (a *fakeArgo) handleDelete(w http.ResponseWriter, r *http.Request) {
	uuid := r.URL.Path[len("/walking-pad/sessions/"):]
	a.mu.Lock()
	_, existed := a.set[uuid]
	delete(a.set, uuid)
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"uuid": uuid, "deleted": existed})
}

func (a *fakeArgo) handleUpsert(w http.ResponseWriter, r *http.Request) {
	var body sessionPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.set[body.UUID] = struct{}{}
	a.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (a *fakeArgo) uuids() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.set))
	for u := range a.set {
		out = append(out, u)
	}
	return out
}
