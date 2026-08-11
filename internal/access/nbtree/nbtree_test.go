// Package nbtree_test holds the black-box integration tests: they drive the
// public API over a real disk + buffer pool at the production node capacities
// (unlike the in-package tests, which shrink capacities to force splits cheaply).
package nbtree_test

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/access/heap"
	"github.com/ailuruschen-bit/minidb/internal/access/nbtree"
	"github.com/ailuruschen-bit/minidb/internal/storage/buffer"
	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
)

func tid(seed int) heap.TID {
	return heap.TID{Page: disk.PageID(seed), Slot: uint16(seed)}
}

// newTree opens a B+Tree over a real disk manager and buffer pool in a temp dir.
func newTree(t *testing.T, poolSize int) *nbtree.BTree {
	t.Helper()
	path := filepath.Join(t.TempDir(), "idx.db")
	dm, err := disk.Open(path)
	if err != nil {
		t.Fatalf("open disk: %v", err)
	}
	t.Cleanup(func() { _ = dm.Close() })
	tr, err := nbtree.NewBTree(buffer.NewBufferPool(dm, poolSize))
	if err != nil {
		t.Fatalf("new btree: %v", err)
	}
	return tr
}

// Enough keys to split leaves at the real capacity (>584) build a multi-level
// tree; with a pool smaller than the node count this also drives eviction and
// reload. Every key reads back, and a full scan is sorted.
func TestRealPoolRoundTrip(t *testing.T) {
	tr := newTree(t, 16)

	const n = 1000
	for i := 0; i < n; i++ {
		if err := tr.Insert(int64(i), tid(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	for i := 0; i < n; i++ {
		got, found, err := tr.Search(int64(i))
		if err != nil {
			t.Fatal(err)
		}
		if !found || got != tid(i) {
			t.Errorf("key %d: (%+v, found %v), want %+v", i, got, found, tid(i))
		}
	}
	if _, found, _ := tr.Search(n + 1); found {
		t.Error("found a key that was never inserted")
	}

	prev, count := int64(math.MinInt64), 0
	err := tr.Scan(math.MinInt64, math.MaxInt64, func(k int64, _ heap.TID) error {
		if count > 0 && k <= prev {
			t.Fatalf("scan not sorted: %d then %d", prev, k)
		}
		prev, count = k, count+1
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Errorf("scan visited %d keys, want %d", count, n)
	}
}

// The index survives a close/reopen: session 2 validates the meta magic, reads
// the persisted root, and finds every key session 1 inserted.
func TestPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.db")
	const n = 700 // > leaf capacity, so the tree has split into several nodes

	// Session 1: build, flush, sync, close.
	dm1, err := disk.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pool1 := buffer.NewBufferPool(dm1, 16)
	tr1, err := nbtree.NewBTree(pool1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := tr1.Insert(int64(i*2), tid(i)); err != nil { // even keys
			t.Fatalf("insert %d: %v", i*2, err)
		}
	}
	if err := pool1.FlushAll(); err != nil {
		t.Fatal(err)
	}
	if err := pool1.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := dm1.Close(); err != nil {
		t.Fatal(err)
	}

	// Session 2: reopen and read back.
	dm2, err := disk.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dm2.Close() }()
	tr2, err := nbtree.NewBTree(buffer.NewBufferPool(dm2, 16))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for i := 0; i < n; i++ {
		got, found, err := tr2.Search(int64(i * 2))
		if err != nil {
			t.Fatal(err)
		}
		if !found || got != tid(i) {
			t.Errorf("reopened key %d: (%+v, found %v), want %+v", i*2, got, found, tid(i))
		}
	}
	if _, found, _ := tr2.Search(1); found { // an odd key was never inserted
		t.Error("found an odd key that was never inserted")
	}
}
