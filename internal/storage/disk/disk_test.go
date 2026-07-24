package disk_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// tempDBPath returns a path inside the test's auto-cleaned temp dir. The file
// does not exist yet.
func tempDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

// writeZeroPages seeds a file of exactly n pages (n*PageSize zero bytes), so a
// test can open a file with a known page count without needing AllocatePage.
func writeZeroPages(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n*int(page.PageSize)), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return info.Size()
}

// filledPage returns a fresh one-page buffer with every byte set to b, so a
// page's contents are predictable and distinguishable from its neighbours.
func filledPage(b byte) []byte {
	buf := make([]byte, page.PageSize)
	for i := range buf {
		buf[i] = b
	}
	return buf
}

// mustOpen opens a manager and registers its cleanup.
func mustOpen(t *testing.T, path string) *disk.DiskManager {
	t.Helper()
	d, err := disk.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// --- Open / Close / NumPages ---

// Opening a path that does not exist creates the file with zero pages.
func TestOpenCreatesMissingFile(t *testing.T) {
	path := tempDBPath(t)
	d := mustOpen(t, path)

	if _, err := os.Stat(path); err != nil {
		t.Errorf("Open did not create the file: %v", err)
	}
	if got := d.NumPages(); got != 0 {
		t.Errorf("NumPages = %d, want 0 for a fresh file", got)
	}
}

// NumPages is derived from the file size at Open.
func TestOpenExistingFileCountsPages(t *testing.T) {
	tests := []struct {
		name  string
		pages int
	}{
		{"empty", 0},
		{"one page", 1},
		{"several pages", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tempDBPath(t)
			writeZeroPages(t, path, tt.pages)
			d := mustOpen(t, path)

			if got := d.NumPages(); got != disk.PageID(tt.pages) {
				t.Errorf("NumPages = %d, want %d", got, tt.pages)
			}
		})
	}
}

// A file whose size is not a whole number of pages is a corrupt/half-written
// file: Open must reject it and return no usable manager.
func TestOpenRejectsCorruptSize(t *testing.T) {
	path := tempDBPath(t)
	// two full pages plus a stray 50 bytes
	if err := os.WriteFile(path, make([]byte, 2*int(page.PageSize)+50), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := disk.Open(path)
	if err == nil {
		t.Error("Open accepted a file whose size is not a multiple of PageSize")
	}
	if d != nil {
		t.Errorf("Open returned a non-nil manager on error: %v", d)
	}
}

// Close releases the handle without error.
func TestClose(t *testing.T) {
	d, err := disk.Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// --- AllocatePage ---

// The first allocation on a fresh file hands out id 0 and grows the file by
// exactly one page.
func TestAllocatePageFirst(t *testing.T) {
	path := tempDBPath(t)
	d := mustOpen(t, path)

	id, err := d.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage: %v", err)
	}
	if id != 0 {
		t.Errorf("first page id = %d, want 0", id)
	}
	if got := d.NumPages(); got != 1 {
		t.Errorf("NumPages = %d, want 1", got)
	}
	if got, want := fileSize(t, path), int64(page.PageSize); got != want {
		t.Errorf("file size = %d, want %d", got, want)
	}
}

// Ids are handed out in order and the file grows one page at a time.
func TestAllocatePageSequentialIDs(t *testing.T) {
	const n = 3
	path := tempDBPath(t)
	d := mustOpen(t, path)

	for i := 0; i < n; i++ {
		id, err := d.AllocatePage()
		if err != nil {
			t.Fatalf("AllocatePage %d: %v", i, err)
		}
		if id != disk.PageID(i) {
			t.Errorf("allocation %d returned id %d, want %d", i, id, i)
		}
	}

	if got := d.NumPages(); got != n {
		t.Errorf("NumPages = %d, want %d", got, n)
	}
	if got, want := fileSize(t, path), n*int64(page.PageSize); got != want {
		t.Errorf("file size = %d, want %d", got, want)
	}
}

// A freshly allocated page reads back as all zeros — callers rely on that to
// initialise a page header.
func TestAllocatePageIsZeroFilled(t *testing.T) {
	path := tempDBPath(t)
	d := mustOpen(t, path)

	id, err := d.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	start := int(id) * int(page.PageSize)
	got := raw[start : start+int(page.PageSize)]
	if !bytes.Equal(got, make([]byte, page.PageSize)) {
		t.Error("a freshly allocated page is not zero-filled")
	}
}

// Allocation continues after the pages a file already had.
func TestAllocatePageContinuesExistingFile(t *testing.T) {
	path := tempDBPath(t)
	writeZeroPages(t, path, 2)
	d := mustOpen(t, path)

	id, err := d.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage: %v", err)
	}
	if id != 2 {
		t.Errorf("id = %d, want 2 (file already had pages 0 and 1)", id)
	}
	if got := d.NumPages(); got != 3 {
		t.Errorf("NumPages = %d, want 3", got)
	}
}

// Allocation really extends the file, not just the in-memory counter: a fresh
// manager over the same file sees the pages.
func TestAllocatedPagesSurviveReopen(t *testing.T) {
	const n = 3
	path := tempDBPath(t)

	d, err := disk.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := d.AllocatePage(); err != nil {
			t.Fatalf("AllocatePage %d: %v", i, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := mustOpen(t, path)
	if got := reopened.NumPages(); got != n {
		t.Errorf("NumPages after reopen = %d, want %d", got, n)
	}
}

// Concurrent allocations must never hand out the same id: the read-modify-write
// of the page counter is what the mutex protects. Run under -race as well, so a
// missing lock shows up as a data race and not merely as a bad count.
func TestAllocatePageConcurrent(t *testing.T) {
	const goroutines = 50
	path := tempDBPath(t)
	d := mustOpen(t, path)

	var wg sync.WaitGroup
	ids := make(chan disk.PageID, goroutines)
	errs := make(chan error, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			id, err := d.AllocatePage()
			if err != nil {
				errs <- err // never call t.Fatalf off the test goroutine
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		t.Fatalf("AllocatePage: %v", err)
	}

	seen := make(map[disk.PageID]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("page id %d was handed out more than once", id)
		}
		seen[id] = true
	}
	if len(seen) != goroutines {
		t.Errorf("got %d distinct ids, want %d", len(seen), goroutines)
	}
	if got := d.NumPages(); got != goroutines {
		t.Errorf("NumPages = %d, want %d", got, goroutines)
	}
	if got, want := fileSize(t, path), goroutines*int64(page.PageSize); got != want {
		t.Errorf("file size = %d, want %d", got, want)
	}
}

// A failed allocation must leave the counter untouched rather than advertise a
// page that was never written. Writing to a closed file is the cleanest way to
// make the underlying write fail.
func TestAllocatePageFailureLeavesCountUnchanged(t *testing.T) {
	d, err := disk.Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before := d.NumPages()
	if _, err := d.AllocatePage(); err == nil {
		t.Error("AllocatePage on a closed file did not fail")
	}
	if got := d.NumPages(); got != before {
		t.Errorf("NumPages changed after a failed allocation: %d -> %d", before, got)
	}
}

// --- ReadPage / WritePage ---

// A page written and read back through the manager is byte-identical.
func TestWriteReadRoundTrip(t *testing.T) {
	path := tempDBPath(t)
	d := mustOpen(t, path)

	id, err := d.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage: %v", err)
	}
	want := filledPage(0xAB)
	if err := d.WritePage(id, want); err != nil {
		t.Fatalf("WritePage: %v", err)
	}

	got := make([]byte, page.PageSize)
	if err := d.ReadPage(id, got); err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("read page differs from what was written")
	}
}

// Writing one page must not disturb its neighbours.
func TestWritePageIsolation(t *testing.T) {
	const n = 3
	path := tempDBPath(t)
	d := mustOpen(t, path)
	for i := 0; i < n; i++ {
		if _, err := d.AllocatePage(); err != nil {
			t.Fatalf("AllocatePage %d: %v", i, err)
		}
	}

	// each page gets a distinct fill
	for i := 0; i < n; i++ {
		if err := d.WritePage(disk.PageID(i), filledPage(byte(i+1))); err != nil {
			t.Fatalf("WritePage %d: %v", i, err)
		}
	}
	for i := 0; i < n; i++ {
		got := make([]byte, page.PageSize)
		if err := d.ReadPage(disk.PageID(i), got); err != nil {
			t.Fatalf("ReadPage %d: %v", i, err)
		}
		if !bytes.Equal(got, filledPage(byte(i+1))) {
			t.Errorf("page %d was corrupted by a write to another page", i)
		}
	}
}

// Written pages must survive a close/reopen: the data really reached the file.
func TestWrittenPageSurvivesReopen(t *testing.T) {
	path := tempDBPath(t)

	d, err := disk.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id, err := d.AllocatePage()
	if err != nil {
		t.Fatalf("AllocatePage: %v", err)
	}
	if err := d.WritePage(id, filledPage(0xCD)); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := mustOpen(t, path)
	got := make([]byte, page.PageSize)
	if err := reopened.ReadPage(id, got); err != nil {
		t.Fatalf("ReadPage after reopen: %v", err)
	}
	if !bytes.Equal(got, filledPage(0xCD)) {
		t.Error("page contents did not survive reopen")
	}
}

// Reading or writing a page that was never allocated is rejected, and a
// rejected write must not grow the file.
func TestReadWriteOutOfRange(t *testing.T) {
	path := tempDBPath(t)
	d := mustOpen(t, path)
	if _, err := d.AllocatePage(); err != nil { // only page 0 exists
		t.Fatalf("AllocatePage: %v", err)
	}

	buf := make([]byte, page.PageSize)
	if err := d.ReadPage(5, buf); err == nil {
		t.Error("ReadPage on an unallocated page did not fail")
	}
	if err := d.WritePage(5, buf); err == nil {
		t.Error("WritePage on an unallocated page did not fail")
	}
	if got, want := fileSize(t, path), int64(page.PageSize); got != want {
		t.Errorf("a rejected write grew the file: size %d, want %d", got, want)
	}
}

// The buffer must be exactly one page long: ReadAt/WriteAt transfer len(buf)
// bytes, so a wrong length would read/write a partial or overlapping page.
func TestReadWriteRejectWrongBufferLength(t *testing.T) {
	path := tempDBPath(t)
	d := mustOpen(t, path)
	if _, err := d.AllocatePage(); err != nil {
		t.Fatalf("AllocatePage: %v", err)
	}

	for _, n := range []int{0, 100, int(page.PageSize) - 1, int(page.PageSize) + 1} {
		if err := d.ReadPage(0, make([]byte, n)); err == nil {
			t.Errorf("ReadPage accepted a %d-byte buffer", n)
		}
		if err := d.WritePage(0, make([]byte, n)); err == nil {
			t.Errorf("WritePage accepted a %d-byte buffer", n)
		}
	}
}

// Integrated stress capstone: many workers interleave Allocate/Write/Read while
// others poll NumPages. Each worker only touches pages it allocated itself, so
// no two goroutines share a page (the disk layer does not coordinate same-page
// access — that is the buffer pool's job), which keeps page contents
// deterministic and assertable. Run under -race so a missing lock surfaces as a
// data race, not just a bad final count.
func TestConcurrentMixedOperations(t *testing.T) {
	const (
		workers        = 20
		pagesPerWorker = 25
		pollers        = 4
	)
	path := tempDBPath(t)
	d := mustOpen(t, path)

	var wg sync.WaitGroup
	errs := make(chan error, workers*pagesPerWorker)
	ids := make(chan disk.PageID, workers*pagesPerWorker)

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for p := 0; p < pagesPerWorker; p++ {
				id, err := d.AllocatePage()
				if err != nil {
					errs <- err
					return
				}
				ids <- id

				// content derived from the (globally unique) id
				want := filledPage(byte(id))
				if err := d.WritePage(id, want); err != nil {
					errs <- err
					return
				}
				got := make([]byte, page.PageSize)
				if err := d.ReadPage(id, got); err != nil {
					errs <- err
					return
				}
				if !bytes.Equal(got, want) {
					errs <- fmt.Errorf("page %d read back wrong contents", id)
					return
				}
			}
		}()
	}

	// pollers just hammer the shared counter to add read contention
	stop := make(chan struct{})
	var pollWG sync.WaitGroup
	pollWG.Add(pollers)
	for i := 0; i < pollers; i++ {
		go func() {
			defer pollWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = d.NumPages()
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	pollWG.Wait()
	close(errs)
	close(ids)

	for err := range errs {
		t.Fatalf("worker: %v", err)
	}

	// every allocated id is globally unique
	const total = workers * pagesPerWorker
	seen := make(map[disk.PageID]bool, total)
	for id := range ids {
		if seen[id] {
			t.Errorf("id %d handed out more than once", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Errorf("got %d distinct ids, want %d", len(seen), total)
	}
	if got := d.NumPages(); got != total {
		t.Errorf("NumPages = %d, want %d", got, total)
	}
	if got, want := fileSize(t, path), int64(total)*int64(page.PageSize); got != want {
		t.Errorf("file size = %d, want %d", got, want)
	}
}
