package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTombstones_WriteListMarkSynced(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	if err := s.WriteTombstone(ctx, "a", now); err != nil {
		t.Fatalf("WriteTombstone a: %v", err)
	}
	if err := s.WriteTombstone(ctx, "b", now.Add(time.Second)); err != nil {
		t.Fatalf("WriteTombstone b: %v", err)
	}

	pending, err := s.UnsyncedTombstones(ctx, 10)
	if err != nil {
		t.Fatalf("UnsyncedTombstones: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}
	// FIFO by deleted_at.
	if pending[0].UUID != "a" || pending[1].UUID != "b" {
		t.Errorf("order = %s,%s, want a,b", pending[0].UUID, pending[1].UUID)
	}

	if err := s.MarkTombstoneSynced(ctx, "a", now.Add(time.Minute)); err != nil {
		t.Fatalf("MarkTombstoneSynced: %v", err)
	}
	pending, _ = s.UnsyncedTombstones(ctx, 10)
	if len(pending) != 1 || pending[0].UUID != "b" {
		t.Errorf("after mark, pending = %v, want [b]", pending)
	}
}

func TestTombstones_WriteIsIdempotentAndClearsSynced(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	_ = s.WriteTombstone(ctx, "x", now)
	_ = s.MarkTombstoneSynced(ctx, "x", now.Add(time.Minute))

	// Second write to same uuid should re-arm it for delivery.
	if err := s.WriteTombstone(ctx, "x", now.Add(time.Hour)); err != nil {
		t.Fatalf("re-write: %v", err)
	}
	pending, _ := s.UnsyncedTombstones(ctx, 10)
	if len(pending) != 1 || pending[0].UUID != "x" {
		t.Errorf("re-write should re-queue; pending = %v", pending)
	}
}

func TestTombstones_MarkSyncedUnknownReturnsNotFound(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	err := s.MarkTombstoneSynced(ctx, "nope", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTombstones_GCRemovesOnlySyncedOlderThan(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// Three tombstones: one unsynced, one synced-old, one synced-fresh.
	_ = s.WriteTombstone(ctx, "unsynced", now)
	_ = s.WriteTombstone(ctx, "old", now)
	_ = s.MarkTombstoneSynced(ctx, "old", now.Add(-31*24*time.Hour))
	_ = s.WriteTombstone(ctx, "fresh", now)
	_ = s.MarkTombstoneSynced(ctx, "fresh", now.Add(-1*time.Hour))

	cutoff := now.Add(-30 * 24 * time.Hour)
	n, err := s.GCTombstones(ctx, cutoff)
	if err != nil {
		t.Fatalf("GCTombstones: %v", err)
	}
	if n != 1 {
		t.Errorf("gc'd = %d, want 1 (only the old one)", n)
	}
	for _, uuid := range []string{"unsynced", "fresh"} {
		has, _ := s.HasTombstone(ctx, uuid)
		if !has {
			t.Errorf("%s tombstone gone, should still exist", uuid)
		}
	}
	has, _ := s.HasTombstone(ctx, "old")
	if has {
		t.Error("old tombstone should be gone")
	}
}
