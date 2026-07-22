package disk_test

import (
	"os"
	"path/filepath"
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

// Opening a path that does not exist creates the file with zero pages.
func TestOpenCreatesMissingFile(t *testing.T) {
	path := tempDBPath(t)

	d, err := disk.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

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

			d, err := disk.Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer d.Close()

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
