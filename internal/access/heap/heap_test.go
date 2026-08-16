package heap_test

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/access/heap"
	"github.com/ailuruschen-bit/minidb/internal/storage/buffer"
	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// newHeap builds a heap over a real buffer pool and disk file in a temp dir.
func newHeap(t *testing.T, poolSize int) *heap.Heap {
	t.Helper()
	path := filepath.Join(t.TempDir(), "heap.db")
	dm, err := disk.Open(path)
	if err != nil {
		t.Fatalf("open disk: %v", err)
	}
	t.Cleanup(func() { _ = dm.Close() })
	return heap.NewHeap(buffer.NewBufferPool(poolSize).Register(dm))
}

// payload builds a distinct, verifiable tuple of the given size for seed.
func payload(seed, size int) []byte {
	p := make([]byte, size)
	for j := range p {
		p[j] = byte(seed*31 + j)
	}
	return p
}

// initPage returns a frame already formatted as an empty slotted page — an
// "existing" heap page — for building fakePool states.
func initPage() *page.Page {
	raw := page.NewPage()
	page.AsSlottedPage(raw).Init()
	return raw
}

// fakePool is a pager that fails FetchPage/NewPage on demand, driving the heap's
// I/O error paths a healthy disk cannot reach.
type fakePool struct {
	pages     []*page.Page
	failFetch bool
	failNew   bool
}

func (f *fakePool) NumPages() disk.PageID { return disk.PageID(len(f.pages)) }

func (f *fakePool) FetchPage(pid disk.PageID) (*page.Page, error) {
	if f.failFetch {
		return nil, errors.New("fakePool: fetch failed")
	}
	if int(pid) >= len(f.pages) {
		return nil, fmt.Errorf("fakePool: page %d out of range", pid)
	}
	return f.pages[pid], nil
}

func (f *fakePool) NewPage() (*page.Page, disk.PageID, error) {
	if f.failNew {
		return nil, 0, errors.New("fakePool: newpage failed")
	}
	// Mimic the real pool: hand back a raw, unformatted frame. The heap formats
	// it (Init) before use.
	raw := page.NewPage()
	f.pages = append(f.pages, raw)
	return raw, disk.PageID(len(f.pages) - 1), nil
}

func (f *fakePool) UnpinPage(_ disk.PageID, _ bool) error { return nil }

// A tuple reads back exactly as inserted.
func TestInsertGetRoundTrip(t *testing.T) {
	h := newHeap(t, 8)

	data := []byte("hello heap")
	tid, err := h.Insert(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.Get(tid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Get = %q, want %q", got, data)
	}
}

// Enough large tuples spill onto new pages; every one still reads back by its id.
func TestInsertSpansPagesAndReadsBack(t *testing.T) {
	h := newHeap(t, 3)

	const (
		n    = 12
		size = 2000 // ~4 tuples per 8KB page
	)
	tids := make([]heap.TID, n)
	for i := range n {
		tid, err := h.Insert(payload(i, size))
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		tids[i] = tid
	}
	if tids[n-1].Page == 0 {
		t.Fatal("expected the inserts to span several pages; all landed on page 0")
	}
	for i := range n {
		got, err := h.Get(tids[i])
		if err != nil {
			t.Fatalf("get %d (%+v): %v", i, tids[i], err)
		}
		if !bytes.Equal(got, payload(i, size)) {
			t.Errorf("tuple %d read back wrong", i)
		}
	}
}

// Scan visits every tuple exactly once, across pages and through eviction (pool
// smaller than the page count).
func TestScanVisitsEveryTuple(t *testing.T) {
	h := newHeap(t, 2)

	const (
		n    = 12
		size = 2000
	)
	for i := range n {
		if _, err := h.Insert(payload(i, size)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	seen := make(map[string]int)
	count := 0
	err := h.Scan(func(_ heap.TID, data []byte) error {
		count++
		seen[string(data)]++ // string(...) copies, so it survives the view
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Errorf("scanned %d tuples, want %d", count, n)
	}
	for i := range n {
		if got := seen[string(payload(i, size))]; got != 1 {
			t.Errorf("payload %d seen %d times, want 1", i, got)
		}
	}
}

// A callback error aborts the scan and propagates out.
func TestScanStopsOnCallbackError(t *testing.T) {
	h := newHeap(t, 8)
	for i := range 10 {
		if _, err := h.Insert([]byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}

	stop := errors.New("stop here")
	seen := 0
	err := h.Scan(func(_ heap.TID, _ []byte) error {
		seen++
		if seen == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Errorf("Scan err = %v, want the sentinel", err)
	}
	if seen != 3 {
		t.Errorf("callback ran %d times, want 3 (scan did not stop)", seen)
	}
}

// A tuple id whose slot does not exist is rejected.
func TestGetSlotOutOfRange(t *testing.T) {
	h := newHeap(t, 8)
	tid, err := h.Insert([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Get(heap.TID{Page: tid.Page, Slot: tid.Slot + 5}); err == nil {
		t.Error("Get with an out-of-range slot did not error")
	}
}

// A tuple id pointing at a page that was never allocated is rejected.
func TestGetMissingPage(t *testing.T) {
	h := newHeap(t, 8)
	if _, err := h.Get(heap.TID{Page: 99, Slot: 0}); err == nil {
		t.Error("Get on a non-existent page did not error")
	}
}

// A tuple that cannot fit even an empty page is rejected, not silently dropped.
func TestInsertTooLarge(t *testing.T) {
	h := newHeap(t, 8)
	if _, err := h.Insert(make([]byte, page.PageSize)); err == nil {
		t.Error("inserting a tuple larger than a page did not error")
	}
}

// Insert surfaces a FetchPage failure on the last page.
func TestInsertFetchError(t *testing.T) {
	fp := &fakePool{pages: []*page.Page{initPage()}, failFetch: true}
	if _, err := heap.NewHeap(fp).Insert([]byte("x")); err == nil {
		t.Error("Insert did not surface the FetchPage failure")
	}
}

// Insert surfaces a NewPage failure when it must grow the file.
func TestInsertNewPageError(t *testing.T) {
	fp := &fakePool{failNew: true} // no pages → Insert goes straight to NewPage
	if _, err := heap.NewHeap(fp).Insert([]byte("x")); err == nil {
		t.Error("Insert did not surface the NewPage failure")
	}
}

// An unexpected (non-ErrNoSpace) page error propagates rather than being taken
// for "page full".
func TestInsertUnexpectedPageError(t *testing.T) {
	corrupt := initPage()
	// Corrupt the page so InsertTuple overflows the slot offset.
	page.AsSlottedPage(corrupt).Header().SetPdLower(40000)
	fp := &fakePool{pages: []*page.Page{corrupt}}
	if _, err := heap.NewHeap(fp).Insert([]byte("x")); err == nil {
		t.Error("Insert did not surface an unexpected page error")
	}
}

// Scan surfaces a FetchPage failure.
func TestScanFetchError(t *testing.T) {
	fp := &fakePool{pages: []*page.Page{initPage()}, failFetch: true}
	err := heap.NewHeap(fp).Scan(func(_ heap.TID, _ []byte) error { return nil })
	if err == nil {
		t.Error("Scan did not surface the FetchPage failure")
	}
}

// Tuple ids are stable across a close/reopen: Get by an old TID still works.
func TestHeapPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heap.db")

	// Session 1: insert, flush, sync, close.
	dm1, err := disk.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pool1 := buffer.NewBufferPool(8)
	h1 := heap.NewHeap(pool1.Register(dm1))
	var tids []heap.TID
	for i := range 5 {
		tid, err := h1.Insert([]byte(fmt.Sprintf("row-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		tids = append(tids, tid)
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

	// Session 2: reopen and read the same ids back.
	dm2, err := disk.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dm2.Close() }()
	h2 := heap.NewHeap(buffer.NewBufferPool(8).Register(dm2))
	for i, tid := range tids {
		got, err := h2.Get(tid)
		if err != nil {
			t.Fatalf("get %+v: %v", tid, err)
		}
		if want := fmt.Sprintf("row-%d", i); string(got) != want {
			t.Errorf("reopened Get %+v = %q, want %q", tid, got, want)
		}
	}
}
