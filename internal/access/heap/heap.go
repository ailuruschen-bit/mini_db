// Package heap is a table access method: it stores tuples in a heap file — an
// unordered pile of pages — on top of the buffer pool, and reads them back by
// tuple id or by sequential scan. "Heap" names the unordered organization (as in
// PostgreSQL's heap access method), not memory nor the heap data structure.
//
// Tuple bytes are opaque here: assembling and interpreting a tuple (its header,
// columns, MVCC fields) belongs to the caller. Delete and update wait for the
// MVCC/transaction layer.
package heap

import (
	"errors"
	"fmt"

	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// pager is the slice of the buffer pool the heap depends on, declared here
// consumer-side so the heap couples to just these operations rather than the
// whole *buffer.BufferPool — which keeps the dependency small and lets tests
// substitute a fake that can fail on demand. *buffer.BufferPool satisfies it.
type pager interface {
	FetchPage(pid disk.PageID) (*page.Page, error)
	NewPage() (*page.Page, disk.PageID, error)
	UnpinPage(pid disk.PageID, dirty bool) error
	NumPages() disk.PageID
}

// TID (tuple id) is the physical address of a tuple: which page holds it and
// which slot in that page points at it.
type TID struct {
	Page disk.PageID
	Slot uint16
}

// Heap is a table's storage as an unordered pile of tuples across pages, backed
// by a shared buffer pool.
//
// It has no lock of its own and is not safe for concurrent Insert: Insert
// mutates a page's contents outside the pool lock, which v1 does not isolate
// (the buffer pool has no per-page latch yet).
type Heap struct {
	pool pager
}

// NewHeap wraps a buffer pool as a heap file. The pool is shared and owned
// elsewhere; the heap only borrows it.
func NewHeap(pool pager) *Heap {
	return &Heap{pool: pool}
}

// Insert stores a tuple and returns its id. It appends to the last page when the
// tuple fits there, otherwise grows the file by one page. This v1 strategy does
// not search earlier pages for space (no free-space map yet), so freed space in
// earlier pages is not reused.
func (h *Heap) Insert(data []byte) (TID, error) {
	// Try the last existing page first.
	if n := h.pool.NumPages(); n > 0 {
		pid := n - 1
		raw, err := h.pool.FetchPage(pid)
		if err != nil {
			return TID{}, fmt.Errorf("heap: insert: %w", err)
		}
		slot, err := page.AsSlottedPage(raw).InsertTuple(data)
		if err == nil {
			_ = h.pool.UnpinPage(pid, true)
			return TID{Page: pid, Slot: slot}, nil
		}
		_ = h.pool.UnpinPage(pid, false)
		if !errors.Is(err, page.ErrNoSpace) {
			return TID{}, fmt.Errorf("heap: insert: %w", err)
		}
		// The last page is full; fall through to grow the file.
	}

	// No pages yet, or the last one is full: allocate a fresh page. The pool
	// hands back a raw frame, so the heap formats it into a slotted page — and
	// from that point the page is dirty (its format lives only in memory), so it
	// is unpinned dirty on both paths, even when the tuple does not fit. That
	// persists a valid empty page rather than leaving the zero page on disk,
	// which Scan would later read as an underflowing slot count.
	raw, pid, err := h.pool.NewPage()
	if err != nil {
		return TID{}, fmt.Errorf("heap: insert: %w", err)
	}
	sp := page.AsSlottedPage(raw)
	sp.Init()
	slot, err := sp.InsertTuple(data)
	if err != nil {
		// An empty page could not fit it, so it is larger than a page allows.
		_ = h.pool.UnpinPage(pid, true)
		return TID{}, fmt.Errorf("heap: insert: tuple does not fit an empty page: %w", err)
	}
	_ = h.pool.UnpinPage(pid, true)
	return TID{Page: pid, Slot: slot}, nil
}

// Get returns a copy of the tuple at tid. The bytes are copied out because the
// page may be evicted and its frame reused once the tuple is unpinned.
func (h *Heap) Get(tid TID) ([]byte, error) {
	raw, err := h.pool.FetchPage(tid.Page)
	if err != nil {
		return nil, fmt.Errorf("heap: get %+v: %w", tid, err)
	}
	defer func() { _ = h.pool.UnpinPage(tid.Page, false) }()

	p := page.AsSlottedPage(raw)
	if tid.Slot >= p.SlotCount() {
		return nil, fmt.Errorf("heap: get %+v: slot out of range [0,%d)", tid, p.SlotCount())
	}
	entry := p.SlotEntryAt(tid.Slot)
	src := p.LocateTupleByEntry(&entry).Bytes()
	out := make([]byte, len(src))
	copy(out, src)
	return out, nil
}

// Scan visits every tuple in the heap in physical order, calling fn with each
// tuple's id and bytes. The bytes are a view valid only for the duration of the
// call — copy them to keep them. fn returns an error to stop the scan early;
// that error, and any I/O error, is returned from Scan.
func (h *Heap) Scan(fn func(tid TID, data []byte) error) error {
	for pid := disk.PageID(0); pid < h.pool.NumPages(); pid++ {
		raw, err := h.pool.FetchPage(pid)
		if err != nil {
			return fmt.Errorf("heap: scan page %d: %w", pid, err)
		}
		// scanPage runs between fetch and unpin so the page is always released,
		// even when the callback aborts the scan mid-page.
		err = scanPage(pid, page.AsSlottedPage(raw), fn)
		_ = h.pool.UnpinPage(pid, false)
		if err != nil {
			return err
		}
	}
	return nil
}

// scanPage feeds every tuple on one page to fn, returning the first callback
// error (which aborts the whole scan) or nil.
func scanPage(pid disk.PageID, p *page.SlottedPage, fn func(TID, []byte) error) error {
	for slot, entry := range p.Slots() {
		tup := p.LocateTupleByEntry(&entry)
		if err := fn(TID{Page: pid, Slot: slot}, tup.Bytes()); err != nil {
			return err
		}
	}
	return nil
}
