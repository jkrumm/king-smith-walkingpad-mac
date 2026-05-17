package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

// mockArgo serves a paginated GET /walking-pad/sessions backed by a static
// UUID list. Page size defaults to whatever the client asks for via ?limit.
type mockArgo struct {
	srv   *httptest.Server
	uuids []string
	calls atomic.Int32
}

func newMockArgo(t *testing.T, uuids ...string) *mockArgo {
	t.Helper()
	m := &mockArgo{uuids: uuids}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockArgo) handle(w http.ResponseWriter, r *http.Request) {
	m.calls.Add(1)
	if r.URL.Path != "/walking-pad/sessions" {
		http.NotFound(w, r)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	start := (page - 1) * limit
	end := start + limit
	if start > len(m.uuids) {
		start = len(m.uuids)
	}
	if end > len(m.uuids) {
		end = len(m.uuids)
	}
	pageUUIDs := make([]argoSessionRef, 0, end-start)
	for _, u := range m.uuids[start:end] {
		pageUUIDs = append(pageUUIDs, argoSessionRef{UUID: u})
	}
	body := argoSessionsPage{Data: pageUUIDs, Total: len(m.uuids)}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func TestReconcile_TombstonesArgoOrphans(t *testing.T) {
	argoUUIDs := []string{"keep-1", "orphan-a", "orphan-b", "keep-2"}
	argo := newMockArgo(t, argoUUIDs...)

	st := newFakeStore()
	st.seedLocalUUID("keep-1", "keep-2", "local-only")

	w := newWorker(t, st, argo.srv)
	orphans, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if orphans != 2 {
		t.Errorf("orphans = %d, want 2", orphans)
	}
	got := st.writtenTombstoneUUIDs()
	want := map[string]bool{"orphan-a": true, "orphan-b": true}
	for _, u := range got {
		if !want[u] {
			t.Errorf("unexpected tombstone for %s", u)
		}
		delete(want, u)
	}
	if len(want) > 0 {
		t.Errorf("missing tombstones for %v", want)
	}
}

func TestReconcile_NoOrphansIsClean(t *testing.T) {
	argo := newMockArgo(t, "a", "b", "c")
	st := newFakeStore()
	st.seedLocalUUID("a", "b", "c")

	w := newWorker(t, st, argo.srv)
	orphans, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if orphans != 0 {
		t.Errorf("orphans = %d, want 0", orphans)
	}
	if len(st.writtenTombstoneUUIDs()) != 0 {
		t.Errorf("no orphans expected, got tombstones: %v", st.writtenTombstoneUUIDs())
	}
}

func TestReconcile_PaginatesAcrossMultiplePages(t *testing.T) {
	// 450 UUIDs across 3 pages of 200 (and one short page of 50).
	all := make([]string, 450)
	for i := range all {
		all[i] = "u-" + strconv.Itoa(i)
	}
	argo := newMockArgo(t, all...)

	st := newFakeStore()
	// Local has none — every UUID upstream is an orphan.
	w := newWorker(t, st, argo.srv)
	orphans, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if orphans != 450 {
		t.Errorf("orphans = %d, want 450", orphans)
	}
	// 3 pages × 1 GET each = 3 calls.
	if argo.calls.Load() != 3 {
		t.Errorf("argo GET calls = %d, want 3 (pagination)", argo.calls.Load())
	}
}

func TestReconcile_ArgoDownReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	st := newFakeStore()
	w := newWorker(t, st, srv)
	_, err := w.Reconcile(context.Background())
	if err == nil {
		t.Error("expected error when argo returns 5xx")
	}
	if len(st.writtenTombstoneUUIDs()) != 0 {
		t.Error("must not tombstone anything when argo is unreachable")
	}
}

func TestReconcile_EmptyArgoIsNoop(t *testing.T) {
	argo := newMockArgo(t /* no uuids */)
	st := newFakeStore()
	st.seedLocalUUID("only-local")

	w := newWorker(t, st, argo.srv)
	orphans, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if orphans != 0 {
		t.Errorf("orphans = %d, want 0", orphans)
	}
}
