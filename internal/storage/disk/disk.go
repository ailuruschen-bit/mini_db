// Package disk is the storage engine's lowest software layer: it moves
// fixed-size pages between a heap file and caller-provided byte buffers,
// addressed by page number. It knows nothing about page contents (slots,
// tuples) — that is the storage package's concern — and nothing about caching,
// which belongs to the buffer pool above it.
package disk

import (
	"fmt"
	"math"
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

// ReadPage reads page id into dst, which must be exactly one page long — ReadAt
// transfers len(dst) bytes, so a short or long buffer would read a partial page
// or spill into the next one.
//
// Only the page-count check is done under the lock; the positioned read itself
// runs lock-free, since ReadAt on distinct pages is safe at the OS level and
// holding the lock across I/O would serialise every access.
func (d *DiskManager) ReadPage(id PageID, dst []byte) error {
	if len(dst) != int(page.PageSize) {
		return fmt.Errorf("disk: read buffer is %d bytes, want %d", len(dst), page.PageSize)
	}

	d.mu.Lock()
	n := d.numPages
	d.mu.Unlock()
	if id >= n {
		return fmt.Errorf("disk: read page %d out of range [0,%d)", id, n)
	}

	// ReadAt returns a non-nil error unless it fills dst completely, so a short
	// read (e.g. a file truncated behind our back) is reported, not silent.
	if _, err := d.file.ReadAt(dst, int64(id)*int64(page.PageSize)); err != nil {
		return fmt.Errorf("disk: read page %d: %w", id, err)
	}
	return nil
}

// WritePage writes src, which must be exactly one page long, over page id. The
// page must already have been allocated; WritePage never grows the file — use
// AllocatePage for that. The lock and I/O split mirrors ReadPage.
//
// The write is not flushed to disk; call Sync when durability is required.
func (d *DiskManager) WritePage(id PageID, src []byte) error {
	if len(src) != int(page.PageSize) {
		return fmt.Errorf("disk: write buffer is %d bytes, want %d", len(src), page.PageSize)
	}

	d.mu.Lock()
	n := d.numPages
	d.mu.Unlock()
	if id >= n {
		return fmt.Errorf("disk: write page %d out of range [0,%d)", id, n)
	}

	if _, err := d.file.WriteAt(src, int64(id)*int64(page.PageSize)); err != nil {
		return fmt.Errorf("disk: write page %d: %w", id, err)
	}
	return nil
}

// Sync flushes all pending writes for the file to the physical storage device
// (fsync). Until it returns, a write reported as successful may still live only
// in the OS page cache and would be lost on a crash. It is deliberately
// separate from WritePage/AllocatePage: fsync is slow, so the caller decides
// when durability is worth the cost (e.g. at a commit or checkpoint).
//
// It touches no shared counter state, so it needs no lock.
func (d *DiskManager) Sync() error {
	return d.file.Sync()
}

// NumPages reports how many pages the file currently holds. Valid page ids are
// [0, NumPages).
func (d *DiskManager) NumPages() PageID {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.numPages
}

// AllocatePage extends the file by one zero-filled page and returns its id.
// It is the only operation that grows the file; WritePage can only overwrite a
// page that has already been allocated.
//
// The whole read-modify-write runs under the lock. Two concurrent calls must
// not hand out the same id, and the page's bytes must reach the file *before*
// numPages advertises it, so that a concurrent reader can always rely on
// "id < numPages implies the page exists and is readable". Hence the write
// comes first and the counter is bumped only once it succeeded — a failed
// write leaves numPages untouched rather than inventing a phantom page.
//
// The page is not flushed to disk; call Sync when durability is required.
func (d *DiskManager) AllocatePage() (PageID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Read the field directly: NumPages would take the same non-reentrant lock.
	if d.numPages == math.MaxUint32 {
		return 0, fmt.Errorf("disk: page limit reached (%d pages)", d.numPages)
	}

	id := d.numPages
	offset := int64(id) * int64(page.PageSize)

	// Zeroed explicitly rather than by extending with Truncate, so a freshly
	// allocated page has predictable contents instead of a sparse hole.
	if _, err := d.file.WriteAt(make([]byte, page.PageSize), offset); err != nil {
		return 0, fmt.Errorf("disk: extend to page %d: %w", id, err)
	}
	d.numPages++

	return id, nil
}
