package disk_test

import (
	"bytes"
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
