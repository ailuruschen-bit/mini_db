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

func (d *fakeDisk) Sync() error { return nil }

// frameOf returns the frame currently holding pid, failing if it is not resident.
func frameOf(t *testing.T, bp *BufferPool, pid disk.PageID) frameID {
	t.Helper()
	f, ok := bp.pageTable[pid]
	if !ok {
		t.Fatalf("page %d is not resident", pid)
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
			NewBufferPool(newFakeDisk(), size)
		})
	}
}

// A fresh pool has every frame free and nothing resident or evictable.
func TestNewBufferPoolStartsAllFree(t *testing.T) {
	const size = 4
	bp := NewBufferPool(newFakeDisk(), size)

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

// Pin count rises on each fetch and falls on each unpin; a frame is an eviction
// candidate only at zero.
func TestFetchAndPinAccounting(t *testing.T) {
	bp := NewBufferPool(newFakeDisk(), 3)

	_, pid, err := bp.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	f := frameOf(t, bp, pid)

	if got := bp.meta[f].pinCount; got != 1 {
		t.Errorf("pinCount after NewPage = %d, want 1", got)
	}
	if got := bp.replacer.Size(); got != 0 {
		t.Errorf("a pinned page must not be evictable: replacer size = %d, want 0", got)
	}

	if _, err := bp.FetchPage(pid); err != nil { // second holder, same frame
		t.Fatal(err)
	}
	if got := bp.meta[f].pinCount; got != 2 {
		t.Errorf("pinCount after second fetch = %d, want 2", got)
	}

	if err := bp.UnpinPage(pid, false); err != nil {
		t.Fatal(err)
	}
	if got := bp.replacer.Size(); got != 0 {
		t.Errorf("still pinned once: replacer size = %d, want 0", got)
	}

	if err := bp.UnpinPage(pid, false); err != nil {
		t.Fatal(err)
	}
	if got := bp.meta[f].pinCount; got != 0 {
		t.Errorf("pinCount after two unpins = %d, want 0", got)
	}
	if got := bp.replacer.Size(); got != 1 {
		t.Errorf("fully unpinned page must be evictable: replacer size = %d, want 1", got)
	}
}

// A freshly formatted new page is dirty: its header lives only in memory until
// flushed, so a clean eviction must not silently drop it.
func TestNewPageIsDirty(t *testing.T) {
	bp := NewBufferPool(newFakeDisk(), 2)

	_, pid, err := bp.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if f := frameOf(t, bp, pid); !bp.meta[f].dirty {
		t.Error("a new page must start dirty")
	}
}

// A failed write-back of a dirty victim must leave the pool exactly as it was:
// the victim stays resident, dirty, and evictable; no frame is leaked.
func TestEvictionWriteBackFailureRestoresPool(t *testing.T) {
	fd := newFakeDisk()
	bp := NewBufferPool(fd, 1) // one frame: any second page forces an eviction

	_, pid0, err := bp.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if err := bp.UnpinPage(pid0, true); err != nil { // sole candidate, dirty
		t.Fatal(err)
	}

	fd.failWrite = func(id disk.PageID) error {
		if id == pid0 {
			return fmt.Errorf("injected write failure")
		}
		return nil
	}
	if _, _, err := bp.NewPage(); err == nil {
		t.Fatal("NewPage succeeded despite a failing victim write-back")
	}

	f := frameOf(t, bp, pid0)
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
	fd := newFakeDisk()
	bp := NewBufferPool(fd, 2)

	fd.failAlloc = func() error { return fmt.Errorf("injected alloc failure") }
	if _, _, err := bp.NewPage(); err == nil {
		t.Fatal("NewPage succeeded despite a failing AllocatePage")
	}
	if got := len(bp.freeList); got != 2 {
		t.Errorf("freeList = %d, want 2 (frame not returned after alloc failure)", got)
	}
	if got := len(bp.pageTable); got != 0 {
		t.Errorf("pageTable = %d, want 0", got)
	}

	fd.failAlloc = nil
	if _, _, err := bp.NewPage(); err != nil {
		t.Errorf("NewPage after recovery: %v", err)
	}
}

// A miss with every frame pinned has nowhere to load into: FetchPage surfaces
// ErrNoFreeFrame, just like NewPage.
func TestFetchOnFullPoolReturnsErrNoFreeFrame(t *testing.T) {
	fd := newFakeDisk()
	fd.pages = [][]byte{make([]byte, page.PageSize), make([]byte, page.PageSize)}
	bp := NewBufferPool(fd, 1) // one frame

	if _, err := bp.FetchPage(0); err != nil { // loads and pins page 0
		t.Fatal(err)
	}
	if _, err := bp.FetchPage(1); !errors.Is(err, ErrNoFreeFrame) {
		t.Errorf("FetchPage on a full pool: err = %v, want ErrNoFreeFrame", err)
	}
}

// A write error while flushing propagates out of both FlushPage and FlushAll;
// the page stays dirty so a later flush can retry it.
func TestFlushWriteFailurePropagates(t *testing.T) {
	fd := newFakeDisk()
	bp := NewBufferPool(fd, 2)

	_, pid, err := bp.NewPage() // dirty and resident
	if err != nil {
		t.Fatal(err)
	}
	fd.failWrite = func(id disk.PageID) error {
		if id == pid {
			return fmt.Errorf("injected write failure")
		}
		return nil
	}

	if err := bp.FlushPage(pid); err == nil {
		t.Error("FlushPage did not surface the write error")
	}
	if err := bp.FlushAll(); err == nil {
		t.Error("FlushAll did not surface the write error")
	}
	if f := frameOf(t, bp, pid); !bp.meta[f].dirty {
		t.Error("page must stay dirty after a failed flush")
	}
}

// A failed page load (phase B) must return the acquired frame to the free list
// so the pool is not permanently short a frame.
func TestFetchReadFailureReturnsFrame(t *testing.T) {
	fd := newFakeDisk() // no pages: any read is out of range
	bp := NewBufferPool(fd, 2)

	if _, err := bp.FetchPage(0); err == nil {
		t.Fatal("FetchPage succeeded on a page that does not exist on disk")
	}
	if got := len(bp.freeList); got != 2 {
		t.Errorf("freeList = %d, want 2 (frame not returned after read failure)", got)
	}
	if got := len(bp.pageTable); got != 0 {
		t.Errorf("pageTable = %d, want 0", got)
	}
}
