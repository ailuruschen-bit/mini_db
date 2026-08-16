package buffer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// fakeDisk is an in-memory diskManager for tests. Pages live in a slice, and
// each operation can be made to fail on demand (via the failXxx hooks) so the
// pool's error-recovery paths can be exercised deterministically — something a
// real file cannot easily be made to do.
type fakeDisk struct {
	pages [][]byte

	failRead  func(id disk.PageID) error
	failWrite func(id disk.PageID) error
	failAlloc func() error
	failSync  func() error
}

func newFakeDisk() *fakeDisk { return &fakeDisk{} }

func (d *fakeDisk) ReadPage(id disk.PageID, dst []byte) error {
	if d.failRead != nil {
		if err := d.failRead(id); err != nil {
			return err
		}
	}
	if int(id) >= len(d.pages) {
		return fmt.Errorf("fakeDisk: read page %d out of range [0,%d)", id, len(d.pages))
	}
	copy(dst, d.pages[id])
	return nil
}

func (d *fakeDisk) WritePage(id disk.PageID, src []byte) error {
	if d.failWrite != nil {
		if err := d.failWrite(id); err != nil {
			return err
		}
	}
	if int(id) >= len(d.pages) {
		return fmt.Errorf("fakeDisk: write page %d out of range [0,%d)", id, len(d.pages))
	}
	d.pages[id] = append([]byte(nil), src...) // store a copy, not the frame's array
	return nil
}

func (d *fakeDisk) AllocatePage() (disk.PageID, error) {
	if d.failAlloc != nil {
		if err := d.failAlloc(); err != nil {
			return 0, err
		}
	}
	id := disk.PageID(len(d.pages))
	d.pages = append(d.pages, make([]byte, page.PageSize))
	return id, nil
}

func (d *fakeDisk) NumPages() disk.PageID { return disk.PageID(len(d.pages)) }

func (d *fakeDisk) Sync() error {
	if d.failSync != nil {
		return d.failSync()
	}
	return nil
}

// newPool builds a pool of the given size with one fakeDisk registered, and
// returns the pool, that disk (for injecting failures), and its handle.
func newPool(size int) (*BufferPool, *fakeDisk, *FileHandle) {
	fd := newFakeDisk()
	bp := NewBufferPool(size)
	return bp, fd, bp.Register(fd)
}

// frameOf returns the frame holding pid in the handle's file, failing if it is
// not resident.
func frameOf(t *testing.T, h *FileHandle, pid disk.PageID) frameID {
	t.Helper()
	key := pageKey{h.file, pid}
	f, ok := h.pool.pageTable[key]
	if !ok {
		t.Fatalf("%s is not resident", key)
	}
	return f
}

// A non-positive pool size is a programming error, so construction panics.
func TestNewBufferPoolPanicsOnBadSize(t *testing.T) {
	for _, size := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewBufferPool(%d) did not panic", size)
				}
			}()
			NewBufferPool(size)
		})
	}
}

// A fresh pool has every frame free and nothing resident or evictable.
func TestNewBufferPoolStartsAllFree(t *testing.T) {
	const size = 4
	bp := NewBufferPool(size)

	if got := len(bp.freeList); got != size {
		t.Errorf("freeList size = %d, want %d", got, size)
	}
	if got := len(bp.pageTable); got != 0 {
		t.Errorf("pageTable size = %d, want 0", got)
	}
	if got := bp.replacer.Size(); got != 0 {
		t.Errorf("replacer size = %d, want 0", got)
	}
}

// The same page id in two different files occupies two different frames: the
// pool keys pages by {file, pid}, so the files never collide.
func TestTwoFilesKeepPagesDistinct(t *testing.T) {
	bp := NewBufferPool(4)
	a := bp.Register(newFakeDisk())
	b := bp.Register(newFakeDisk())

	pa, pidA, err := a.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	pb, pidB, err := b.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if pidA != pidB {
		t.Fatalf("both files should start at pid 0, got %d and %d", pidA, pidB)
	}
	if pa == pb {
		t.Fatal("same pid in two files must occupy different frames")
	}

	pa.Bytes()[0] = 0xAA
	pb.Bytes()[0] = 0xBB
	if pa.Bytes()[0] != 0xAA || pb.Bytes()[0] != 0xBB {
		t.Error("writes to same-pid pages in different files collided")
	}
	if got := len(bp.pageTable); got != 2 {
		t.Errorf("pageTable has %d entries, want 2 (one per file)", got)
	}

	_ = a.UnpinPage(pidA, true)
	_ = b.UnpinPage(pidB, true)
}

// Pin count rises on each fetch and falls on each unpin; a frame is an eviction
// candidate only at zero.
func TestFetchAndPinAccounting(t *testing.T) {
	bp, _, h := newPool(3)

	_, pid, err := h.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	f := frameOf(t, h, pid)

	if got := bp.meta[f].pinCount; got != 1 {
		t.Errorf("pinCount after NewPage = %d, want 1", got)
	}
	if got := bp.replacer.Size(); got != 0 {
		t.Errorf("a pinned page must not be evictable: replacer size = %d, want 0", got)
	}

	if _, err := h.FetchPage(pid); err != nil { // second holder, same frame
		t.Fatal(err)
	}
	if got := bp.meta[f].pinCount; got != 2 {
		t.Errorf("pinCount after second fetch = %d, want 2", got)
	}

	if err := h.UnpinPage(pid, false); err != nil {
		t.Fatal(err)
	}
	if got := bp.replacer.Size(); got != 0 {
		t.Errorf("still pinned once: replacer size = %d, want 0", got)
	}

	if err := h.UnpinPage(pid, false); err != nil {
		t.Fatal(err)
	}
	if got := bp.meta[f].pinCount; got != 0 {
		t.Errorf("pinCount after two unpins = %d, want 0", got)
	}
	if got := bp.replacer.Size(); got != 1 {
		t.Errorf("fully unpinned page must be evictable: replacer size = %d, want 1", got)
	}
}

// A new page starts clean: the pool returns a zeroed frame that already matches
// the zero page AllocatePage wrote to disk, so there is nothing to flush until
// the access method formats it and unpins it dirty.
func TestNewPageStartsClean(t *testing.T) {
	bp, _, h := newPool(2)

	_, pid, err := h.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if f := frameOf(t, h, pid); bp.meta[f].dirty {
		t.Error("a new page must start clean (memory matches the zero page on disk)")
	}
}

// A failed write-back of a dirty victim must leave the pool exactly as it was:
// the victim stays resident, dirty, and evictable; no frame is leaked.
func TestEvictionWriteBackFailureRestoresPool(t *testing.T) {
	bp, fd, h := newPool(1) // one frame: any second page forces an eviction

	_, pid0, err := h.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.UnpinPage(pid0, true); err != nil { // sole candidate, dirty
		t.Fatal(err)
	}

	fd.failWrite = func(id disk.PageID) error {
		if id == pid0 {
			return fmt.Errorf("injected write failure")
		}
		return nil
	}
	if _, _, err := h.NewPage(); err == nil {
		t.Fatal("NewPage succeeded despite a failing victim write-back")
	}

	f := frameOf(t, h, pid0)
	if !bp.meta[f].dirty {
		t.Error("victim lost its dirty flag after a failed flush")
	}
	if got := bp.replacer.Size(); got != 1 {
		t.Errorf("victim not restored to the candidate set: replacer size = %d, want 1", got)
	}
	if got := len(bp.freeList); got != 0 {
		t.Errorf("freeList = %d, want 0 (no phantom free frame)", got)
	}
}

// A failed AllocatePage must return the acquired frame to the free list, and
// the pool must keep working once allocation recovers.
func TestNewPageAllocateFailureReturnsFrame(t *testing.T) {
	bp, fd, h := newPool(2)

	fd.failAlloc = func() error { return fmt.Errorf("injected alloc failure") }
	if _, _, err := h.NewPage(); err == nil {
		t.Fatal("NewPage succeeded despite a failing AllocatePage")
	}
	if got := len(bp.freeList); got != 2 {
		t.Errorf("freeList = %d, want 2 (frame not returned after alloc failure)", got)
	}
	if got := len(bp.pageTable); got != 0 {
		t.Errorf("pageTable = %d, want 0", got)
	}

	fd.failAlloc = nil
	if _, _, err := h.NewPage(); err != nil {
		t.Errorf("NewPage after recovery: %v", err)
	}
}

// A miss with every frame pinned has nowhere to load into: FetchPage surfaces
// ErrNoFreeFrame, just like NewPage.
func TestFetchOnFullPoolReturnsErrNoFreeFrame(t *testing.T) {
	_, fd, h := newPool(1) // one frame
	fd.pages = [][]byte{make([]byte, page.PageSize), make([]byte, page.PageSize)}

	if _, err := h.FetchPage(0); err != nil { // loads and pins page 0
		t.Fatal(err)
	}
	if _, err := h.FetchPage(1); !errors.Is(err, ErrNoFreeFrame) {
		t.Errorf("FetchPage on a full pool: err = %v, want ErrNoFreeFrame", err)
	}
}

// A write error while flushing propagates out of both FlushPage and FlushAll;
// the page stays dirty so a later flush can retry it.
func TestFlushWriteFailurePropagates(t *testing.T) {
	bp, fd, h := newPool(2)

	_, pid, err := h.NewPage() // resident
	if err != nil {
		t.Fatal(err)
	}
	// A new page starts clean; dirty it so the flush actually attempts a write.
	bp.meta[frameOf(t, h, pid)].dirty = true
	fd.failWrite = func(id disk.PageID) error {
		if id == pid {
			return fmt.Errorf("injected write failure")
		}
		return nil
	}

	if err := h.FlushPage(pid); err == nil {
		t.Error("FlushPage did not surface the write error")
	}
	if err := bp.FlushAll(); err == nil {
		t.Error("FlushAll did not surface the write error")
	}
	if f := frameOf(t, h, pid); !bp.meta[f].dirty {
		t.Error("page must stay dirty after a failed flush")
	}
}

// Sync fsyncs every registered file, and a failure from any of them propagates.
func TestSyncFailurePropagates(t *testing.T) {
	bp := NewBufferPool(2)
	bp.Register(newFakeDisk()) // fine
	fd := newFakeDisk()
	fd.failSync = func() error { return fmt.Errorf("injected sync failure") }
	bp.Register(fd)

	if err := bp.Sync(); err == nil {
		t.Error("Sync did not surface a file's fsync failure")
	}
}

// A failed page load (phase B) must return the acquired frame to the free list
// so the pool is not permanently short a frame.
func TestFetchReadFailureReturnsFrame(t *testing.T) {
	bp, _, h := newPool(2) // no pages: any read is out of range

	if _, err := h.FetchPage(0); err == nil {
		t.Fatal("FetchPage succeeded on a page that does not exist on disk")
	}
	if got := len(bp.freeList); got != 2 {
		t.Errorf("freeList = %d, want 2 (frame not returned after read failure)", got)
	}
	if got := len(bp.pageTable); got != 0 {
		t.Errorf("pageTable = %d, want 0", got)
	}
}
