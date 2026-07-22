// Package disk is the storage engine's lowest software layer: it moves
// fixed-size pages between a heap file and caller-provided byte buffers,
// addressed by page number. It knows nothing about page contents (slots,
// tuples) — that is the storage package's concern — and nothing about caching,
// which belongs to the buffer pool above it.
package disk

import (
	"fmt"
	"os"
	"sync"

	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// PageID identifies a page by its position in the file: page N occupies the
// byte range [N*PageSize, (N+1)*PageSize). It is also the upper bound of the
// page count — valid ids are [0, NumPages).
type PageID uint32

// DiskManager owns one heap file and reads/writes whole pages to it.
//
// A single file must be managed by exactly one DiskManager, shared by every
// goroutine that touches the file: numPages is cached in memory, so two
// managers over the same file would hold divergent counts. The mutex guards
// numPages only — positioned I/O (ReadAt/WriteAt) on distinct pages is safe
// without it, so the read/write path stays lock-free.
type DiskManager struct {
	file     *os.File
	numPages PageID
	mu       sync.Mutex
}

// Open opens the heap file at path, creating it if absent, and derives the page
// count from the file size. The file is opened for positioned I/O, so neither
// O_APPEND (which would defeat WriteAt) nor O_TRUNC (which would erase an
// existing table) is used.
func Open(path string) (*DiskManager, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("disk: open %q: %w", path, err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("disk: stat %q: %w", path, err)
	}

	// The file is an array of fixed-size pages, so its length must be a whole
	// number of pages; anything else means a corrupt or half-written file.
	size := info.Size()
	if size%int64(page.PageSize) != 0 {
		file.Close()
		return nil, fmt.Errorf("disk: file %q is %d bytes, not a multiple of page size %d (corrupt?)", path, size, page.PageSize)
	}

	return &DiskManager{
		file:     file,
		numPages: PageID(size / int64(page.PageSize)),
	}, nil
}

// Close releases the underlying file handle.
func (d *DiskManager) Close() error {
	return d.file.Close()
}

// NumPages reports how many pages the file currently holds. Valid page ids are
// [0, NumPages).
func (d *DiskManager) NumPages() PageID {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.numPages
}
