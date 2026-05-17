package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// reconcileBatchSize is the page size used when listing argo. Argo caps the
// limit query param at 200; we send the maximum to keep round-trips low.
const reconcileBatchSize = 200

// reconcileMaxPages caps the loop so a runaway argo response (or someone
// truly walking 200k sessions) doesn't trap the reconciler. ~200×200 = 40k
// sessions is well past anything realistic.
const reconcileMaxPages = 200

// argoSessionsPage mirrors the shape of GET /walking-pad/sessions: paginated
// envelope with `data` array and `total` count. We only need uuid here, but
// decoding the full row keeps the binding honest if the schema drifts.
type argoSessionsPage struct {
	Data  []argoSessionRef `json:"data"`
	Total int              `json:"total"`
}

type argoSessionRef struct {
	UUID string `json:"uuid"`
}

// Reconcile diffs the upstream argo session list against the local store and
// writes tombstones for any UUID that exists on argo but not locally. The
// existing tombstone-drain pass then DELETEs each orphan on argo, leaving
// the two sides consistent.
//
// This is the safety net for legacy orphans created by code that deleted
// rows locally without writing a tombstone (the pre-`c7ec015` stitch). It is
// also self-healing for any future drift caused by argo restores, manual SQL
// edits on either side, etc.
//
// Soft-fail on network / argo error — the daemon should still start; the
// next reconcile run will retry. Returns the number of orphan tombstones
// written (zero on a clean diff or on a network error).
func (w *Worker) Reconcile(ctx context.Context) (int, error) {
	local, err := w.st.AllSessionUUIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("local uuids: %w", err)
	}

	upstream, err := w.fetchAllArgoUUIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("argo uuids: %w", err)
	}

	now := time.Now().UTC()
	orphans := 0
	for uuid := range upstream {
		if _, has := local[uuid]; has {
			continue
		}
		if err := w.st.WriteTombstone(ctx, uuid, now); err != nil {
			return orphans, fmt.Errorf("tombstone %s: %w", uuid, err)
		}
		orphans++
		w.log.Info("sync.reconcile_orphan", "uuid", uuid)
	}
	return orphans, nil
}

// fetchAllArgoUUIDs paginates through argo's session list and returns the
// full UUID set. Uses asc order so a partial pagination still progresses
// deterministically.
func (w *Worker) fetchAllArgoUUIDs(ctx context.Context) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for page := 1; page <= reconcileMaxPages; page++ {
		batch, total, err := w.fetchArgoPage(ctx, page, reconcileBatchSize)
		if err != nil {
			return nil, err
		}
		for _, ref := range batch {
			if ref.UUID == "" {
				continue
			}
			out[ref.UUID] = struct{}{}
		}
		// We're done when the cumulative count meets argo's reported total or
		// the page came back short.
		if len(out) >= total || len(batch) < reconcileBatchSize {
			return out, nil
		}
	}
	return out, fmt.Errorf("argo pagination exceeded %d pages — total reported %d, fetched %d",
		reconcileMaxPages, -1, len(out))
}

func (w *Worker) fetchArgoPage(ctx context.Context, page, limit int) ([]argoSessionRef, int, error) {
	url := strings.TrimRight(w.cfg.BaseURL, "/") +
		"/walking-pad/sessions?order=asc&page=" + strconv.Itoa(page) +
		"&limit=" + strconv.Itoa(limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	req.Header.Set("User-Agent", w.cfg.UserAgent)

	resp, err := w.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, 0, fmt.Errorf("argo %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	var body argoSessionsPage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, 0, fmt.Errorf("decode argo page: %w", err)
	}
	return body.Data, body.Total, nil
}
