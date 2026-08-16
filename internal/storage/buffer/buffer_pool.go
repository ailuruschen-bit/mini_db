package buffer

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// ErrNoFreeFrame is returned by page operations when every frame is pinned, so
// no page can be evicted to make room. It signals that callers are holding too
// many pages pinned at once, not a corrupt state — releasing pins (UnpinPage)
// resolves it.
var ErrNoFreeFrame = errors.New("buffer: all frames pinned, no page can be evicted")

// diskManager is the slice of the disk layer the buffer pool depends on, per
// file. It is declared here, consumer-side, so the pool couples to just these
// methods rather than the concrete *disk.DiskManager — which keeps the
// dependency small and lets tests inject a fake that can fail on demand. One
// diskManager backs one file; the pool holds several. *disk.DiskManager
// satisfies it.
type diskManager interface {
	ReadPage(id disk.PageID, dst []byte) error
	WritePage(id disk.PageID, src []byte) error
	AllocatePage() (disk.PageID, error)
	NumPages() disk.PageID
	Sync() error
}

// FileID identifies a file registered with the pool. It is assigned in
// registration order and lives only for the session: the catalog persists file
// names, not FileIDs, so a reopened database re-registers its files and gets
// fresh ids.
type FileID uint32

// pageKey identifies a page across every file the pool caches: which file, and
// which page within it. It is the pool's lookup key now that one pool backs many
// files — replacing the bare page id a single-file pool used.
type pageKey struct {
	file FileID
	pid  disk.PageID
}

func (k pageKey) String() string { return fmt.Sprintf("file %d page %d", k.file, k.pid) }

// frameMeta is the bookkeeping the pool keeps for one frame, held parallel to
// frames: meta[i] describes frames[i]. It stays separate from the page's byte
// layout on purpose — pin count, dirtiness and the resident page's key are the
// pool's concern.
//
// The fields are meaningful only while the frame is resident (in pageTable). A
// frame sitting in freeList carries stale meta that no one reads.
type frameMeta struct {
	// key is the reverse of pageTable: the {file, page} this frame holds. It
	// lets eviction find, in O(1), which entry to drop from pageTable and, if
	// dirty, which file and page to write the bytes back to.
	key pageKey

	// pinCount is how many callers currently hold the page. A frame is
	// evictable only at zero; above zero it must stay put.
	pinCount int

	// dirty records whether the page was modified since it was loaded. It is
	// sticky: once set it stays set until the page is flushed, so a clean
	// eviction can skip the write-back.
	dirty bool
}

// BufferPool caches a fixed number of disk pages in memory, shared across every
// file registered with it. Callers reach a file through a FileHandle (from
// Register) and ask for pages by id; the pool serves a page from memory when
// resident, otherwise loads it from that file into a frame, evicting a
// least-recently-used unpinned page first if no frame is free. Eviction spans
// all files: the one shared LRU is a single memory budget for the whole engine.
//
// The frame slots (frames/meta) and the three bookkeeping structures
// (pageTable, freeList, replacer) all address frames by frameID, which is just
// the index into frames — never stored as a field.
//
// A frame is in exactly one of three states, an invariant the page routines
// rely on:
//
//	in freeList                       -> empty, holds no page
//	in pageTable with pinCount > 0    -> resident and in use, not evictable
//	in pageTable with pinCount == 0   -> resident and idle, a victim candidate
//	                                     (tracked by replacer)
//
// freeList and replacer are therefore disjoint: a frame is either free or holds
// an idle page, never both.
//
// Concurrency (v1): one mutex serialises every access to the metadata above.
// There are no per-page latches yet — a page's bytes are protected only for as
// long as the whole pool is locked, so callers must not assume concurrent
// readers and writers of the same page are isolated. That finer-grained latch
// is deliberately deferred.
type BufferPool struct {
	files []diskManager // indexed by FileID (registration order)

	frames []*page.Page // frames[i] is the memory slot for frame i
	meta   []frameMeta  // meta[i] describes frames[i]

	pageTable map[pageKey]frameID // resident {file,page} -> its frame
	freeList  []frameID           // frames holding no page, free to fill
	replacer  *LRUReplacer        // idle resident frames, ordered for eviction

	mu sync.Mutex
}

// NewBufferPool builds a pool of poolSize frames holding no files yet; register
// each file with Register. Every frame's backing page is allocated up front and
// reused for the pool's lifetime: loading a page overwrites a frame's bytes in
// place (ReadPage into frame.Bytes()), so the run-time page path never
// allocates. All frames start free.
//
// poolSize is a start-up configuration value, not run-time input, so an invalid
// size is a programming error: NewBufferPool panics rather than returning an
// error the caller would have to handle on a path that can never legitimately
// occur.
func NewBufferPool(poolSize int) *BufferPool {
	if poolSize <= 0 {
		panic(fmt.Sprintf("buffer: pool size must be positive, got %d", poolSize))
	}

	frames := make([]*page.Page, poolSize)
	freeList := make([]frameID, poolSize)
	for i := range frames {
		frames[i] = page.NewPage()
		freeList[i] = frameID(i)
	}

	return &BufferPool{
		frames:    frames,
		meta:      make([]frameMeta, poolSize),
		pageTable: make(map[pageKey]frameID),
		freeList:  freeList,
		replacer:  NewLRUReplacer(),
	}
}

// Register adds a file (one diskManager) to the pool and returns a FileHandle
// bound to it. The handle presents a single-file page API, so an access method
// (heap, B+Tree) consumes it through its unchanged, file-agnostic pager
// interface — the multi-file key lives only inside the pool and the handle.
//
// Registration may happen at any time (e.g. a table created mid-session), so it
// takes the pool lock.
func (bp *BufferPool) Register(dm diskManager) *FileHandle {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	id := FileID(len(bp.files))
	bp.files = append(bp.files, dm)
	return &FileHandle{pool: bp, file: id}
}

// FileHandle is a per-file view over a shared BufferPool: each call forwards to
// the pool with the handle's file id baked in. It satisfies the consumer-side
// pager interfaces of the heap and the B+Tree, which are unaware the pool is
// multi-file. A handle is obtained from Register and is valid for the pool's
// lifetime.
type FileHandle struct {
	pool *BufferPool
	file FileID
}

// FetchPage returns the page with the given id in this handle's file, pinned so
// it will not be evicted until the caller releases it with UnpinPage. A resident
// page is served from memory; otherwise it is loaded from disk into a free
// frame, evicting a least-recently-used unpinned page first when no frame is
// free. It returns ErrNoFreeFrame if every frame is pinned.
//
// The returned pointer is valid only until the matching UnpinPage: once unpinned
// the frame may be reused for another page, so callers must not retain it past
// that point. It is a raw frame; the caller overlays its own view
// (page.AsSlottedPage, a B+Tree node, ...) to interpret the bytes.
func (h *FileHandle) FetchPage(pid disk.PageID) (*page.Page, error) {
	return h.pool.fetch(h.file, pid)
}

// NewPage allocates a brand-new page in this handle's file, loads it into a
// frame pinned to the caller, and returns the raw frame with its id. Like
// FetchPage it is pinned until UnpinPage and returns ErrNoFreeFrame when no
// frame can be freed. The frame is zeroed but NOT formatted into any page kind —
// formatting (and marking it dirty on unpin) is the access method's job.
func (h *FileHandle) NewPage() (*page.Page, disk.PageID, error) {
	return h.pool.newPage(h.file)
}

// UnpinPage releases one pin on a page in this handle's file, reporting via
// dirty whether the caller modified it. dirty is merged, never cleared. See
// (*BufferPool).unpin.
func (h *FileHandle) UnpinPage(pid disk.PageID, dirty bool) error {
	return h.pool.unpin(h.file, pid, dirty)
}

// NumPages reports how many pages this handle's file holds; valid ids are
// [0, NumPages).
func (h *FileHandle) NumPages() disk.PageID {
	return h.pool.numPages(h.file)
}

// FlushPage writes a dirty page of this handle's file back to disk and marks it
// clean; a clean page is a no-op. It neither unpins nor evicts. See
// (*BufferPool).flushPage.
func (h *FileHandle) FlushPage(pid disk.PageID) error {
	return h.pool.flushPage(h.file, pid)
}

// fetch is the file-keyed core of FileHandle.FetchPage.
func (bp *BufferPool) fetch(file FileID, pid disk.PageID) (*page.Page, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	key := pageKey{file, pid}

	// Hit: already resident. Pin it and take it out of the eviction candidates
	// (Pin is a no-op if it was already pinned, hence not a candidate).
	if f, ok := bp.pageTable[key]; ok {
		bp.meta[f].pinCount++
		bp.replacer.Pin(f)
		return bp.frames[f], nil
	}

	// Miss. Phase A: acquire a blank frame (from the free list, or by evicting).
	f, err := bp.acquireFrame()
	if err != nil {
		return nil, err
	}

	// Phase B: install the page into the blank frame. If the read fails the
	// frame holds no valid page, so return it to the free list. (If it came
	// from an eviction the old page is already flushed and dropped — it stays
	// dropped, which is safe: its bytes are durable on disk.)
	if err := bp.files[file].ReadPage(pid, bp.frames[f].Bytes()); err != nil {
		bp.freeList = append(bp.freeList, f)
		return nil, fmt.Errorf("buffer: load %s: %w", key, err)
	}

	bp.bindFrame(f, key)
	return bp.frames[f], nil
}

// newPage is the file-keyed core of FileHandle.NewPage.
//
// A frame is acquired before the disk page is allocated, on purpose: acquiring
// can fail (pool full) and undoes cleanly, whereas AllocatePage grows the file
// and cannot be undone in v1. Doing the reversible step first means a full pool
// never leaks an unreferenced page on disk.
func (bp *BufferPool) newPage(file FileID) (*page.Page, disk.PageID, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	f, err := bp.acquireFrame()
	if err != nil {
		return nil, 0, err
	}

	pid, err := bp.files[file].AllocatePage()
	if err != nil {
		bp.freeList = append(bp.freeList, f) // give the frame back untouched
		return nil, 0, fmt.Errorf("buffer: allocate page: %w", err)
	}

	// Clear any bytes left by the frame's previous occupant so the caller sees a
	// blank frame, matching the zero page AllocatePage wrote to disk. Memory and
	// disk now agree, so the frame is bound clean; the access method's formatting
	// is what makes it dirty (via UnpinPage).
	clear(bp.frames[f].Bytes())

	bp.bindFrame(f, pageKey{file, pid})
	return bp.frames[f], pid, nil
}

// unpin is the file-keyed core of FileHandle.UnpinPage.
//
// When the last pin is released the frame becomes an eviction candidate again.
// dirty is merged, never cleared: a page stays dirty until it is flushed, so a
// read-only unpin (dirty=false) cannot mask an earlier writer's change; only
// FlushPage clears it. Unpinning a page that is not resident, or one already at
// zero pins, is a caller contract violation and returns an error without
// touching any state.
func (bp *BufferPool) unpin(file FileID, pid disk.PageID, dirty bool) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	key := pageKey{file, pid}
	f, ok := bp.pageTable[key]
	if !ok {
		return fmt.Errorf("buffer: unpin %s that is not resident", key)
	}
	if bp.meta[f].pinCount == 0 {
		return fmt.Errorf("buffer: unpin %s that is not pinned", key)
	}

	bp.meta[f].dirty = bp.meta[f].dirty || dirty
	bp.meta[f].pinCount--
	if bp.meta[f].pinCount == 0 {
		bp.replacer.Unpin(f)
	}
	return nil
}

// numPages is the file-keyed core of FileHandle.NumPages. It grabs the file's
// diskManager under the lock (Register may be appending to files) then reads its
// count lock-free, since the disk manager guards the count itself.
func (bp *BufferPool) numPages(file FileID) disk.PageID {
	bp.mu.Lock()
	dm := bp.files[file]
	bp.mu.Unlock()
	return dm.NumPages()
}

// flushPage is the file-keyed core of FileHandle.FlushPage.
//
// This is the only place (with FlushAll) the dirty flag is cleared, closing the
// lifecycle UnpinPage opens. The write reaches only the OS page cache; call Sync
// for a durability barrier. v1 has no per-page latch, so the byte copy is not
// isolated from a caller concurrently mutating the page it holds pinned — flush
// a page only when no writer is actively changing it.
func (bp *BufferPool) flushPage(file FileID, pid disk.PageID) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	key := pageKey{file, pid}
	f, ok := bp.pageTable[key]
	if !ok {
		return fmt.Errorf("buffer: flush %s that is not resident", key)
	}
	return bp.flushLocked(f)
}

// FlushAll writes every dirty resident page (across all files) back to disk and
// marks them clean; clean pages are skipped. Like FlushPage the writes reach
// only the OS page cache — call Sync afterwards for durability. Typically used
// at a checkpoint or when closing the pool.
//
// On the first write error it stops and returns, leaving the remaining dirty
// pages unflushed (and still marked dirty, so a later flush retries them).
func (bp *BufferPool) FlushAll() error {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	for _, f := range bp.pageTable {
		if err := bp.flushLocked(f); err != nil {
			return err
		}
	}
	return nil
}

// Sync forces every registered file's already-written bytes out of the OS page
// cache to the physical device (fsync). Flush/FlushAll only push bytes to the
// OS; Sync is the separate durability barrier, so the caller pays fsync's cost
// only when durability is needed — e.g. once after FlushAll at a checkpoint.
//
// It snapshots the file set under the lock, then fsyncs outside it, so a slow
// fsync does not block concurrent page traffic.
func (bp *BufferPool) Sync() error {
	bp.mu.Lock()
	dms := append([]diskManager(nil), bp.files...)
	bp.mu.Unlock()

	for _, dm := range dms {
		if err := dm.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// bindFrame records frame f as holding key with a single pin: it writes the
// frame's metadata and the pageTable entry, and deliberately leaves the frame
// out of the replacer, since a pinned frame is never an eviction candidate. It
// is the shared tail of fetch and newPage. The pool lock must be held.
func (bp *BufferPool) bindFrame(f frameID, key pageKey) {
	bp.meta[f] = frameMeta{key: key, pinCount: 1, dirty: false}
	bp.pageTable[key] = f
}

// flushLocked writes frame f back to its file when it is dirty and clears the
// flag. A clean frame is a no-op. The pool lock must be held; it is the shared
// core of flushPage and FlushAll.
func (bp *BufferPool) flushLocked(f frameID) error {
	if !bp.meta[f].dirty {
		return nil
	}
	key := bp.meta[f].key
	if err := bp.files[key.file].WritePage(key.pid, bp.frames[f].Bytes()); err != nil {
		return fmt.Errorf("buffer: flush %s: %w", key, err)
	}
	bp.meta[f].dirty = false
	return nil
}

// acquireFrame returns a frame that belongs to no page, ready for the caller to
// fill: it is absent from pageTable, freeList and replacer alike, owned solely
// by the caller until a page is installed (or it is returned to the free list).
//
// It reuses a free frame when one exists; otherwise it evicts the
// least-recently-used unpinned page, flushing it first if dirty. A dirty
// victim's bytes must reach disk before the frame is repurposed, so the
// write-back comes first and, on failure, the victim is put back into the
// candidate set and pageTable is left untouched — the pool is exactly as it was.
//
// The pool lock must be held.
func (bp *BufferPool) acquireFrame() (frameID, error) {
	if n := len(bp.freeList); n > 0 {
		f := bp.freeList[n-1]
		bp.freeList = bp.freeList[:n-1]
		return f, nil
	}

	v, ok := bp.replacer.Victim()
	if !ok {
		return 0, ErrNoFreeFrame
	}

	old := bp.meta[v].key
	if bp.meta[v].dirty {
		if err := bp.files[old.file].WritePage(old.pid, bp.frames[v].Bytes()); err != nil {
			bp.replacer.Unpin(v) // undo Victim's removal; old stays resident
			return 0, fmt.Errorf("buffer: flush victim %s: %w", old, err)
		}
	}
	delete(bp.pageTable, old)
	return v, nil
}
