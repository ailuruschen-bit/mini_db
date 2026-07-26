package buffer

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// ErrNoFreeFrame is returned by FetchPage/NewPage when every frame is pinned, so
// no page can be evicted to make room. It signals that callers are holding too
// many pages pinned at once, not a corrupt state — releasing pins (UnpinPage)
// resolves it.
var ErrNoFreeFrame = errors.New("buffer: all frames pinned, no page can be evicted")

// diskManager is the slice of the disk layer the buffer pool depends on. It is
// declared here, consumer-side, so the pool couples to just these four methods
// rather than the concrete *disk.DiskManager — which keeps the dependency
// small, lets tests inject a fake that can fail on demand, and leaves room for
// the pool to later hold one manager per file. *disk.DiskManager satisfies it.
type diskManager interface {
	ReadPage(id disk.PageID, dst []byte) error
	WritePage(id disk.PageID, src []byte) error
	AllocatePage() (disk.PageID, error)
	Sync() error
}

// frameMeta is the bookkeeping the pool keeps for one frame, held parallel to
// frames: meta[i] describes frames[i]. It stays separate from SlottedPage on
// purpose — pin count, dirtiness and the resident page id are the pool's
// concern, not the page's byte layout.
//
// The fields are meaningful only while the frame is resident (in pageTable). A
// frame sitting in freeList carries stale meta that no one reads.
type frameMeta struct {
	// pid is the reverse of pageTable: the page this frame currently holds. It
	// lets eviction find, in O(1), which page id to drop from pageTable and, if
	// dirty, which page id to write the bytes back to. (When the pool grows to
	// multiple files this becomes a {fileID, pid} key; the algorithms do not
	// change, only the key does.)
	pid disk.PageID

	// pinCount is how many callers currently hold the page. A frame is
	// evictable only at zero; above zero it must stay put.
	pinCount int

	// dirty records whether the page was modified since it was loaded. It is
	// sticky: once set it stays set until the page is flushed, so a clean
	// eviction can skip the write-back.
	dirty bool
}

// BufferPool caches a fixed number of disk pages in memory. Callers ask for a
// page by id; the pool serves it from memory when resident, otherwise loads it
// from disk into a frame, evicting a least-recently-used unpinned page first if
// no frame is free.
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
	disk diskManager

	frames []*page.SlottedPage // frames[i] is the memory slot for frame i
	meta   []frameMeta         // meta[i] describes frames[i]

	pageTable map[disk.PageID]frameID // resident page id -> its frame
	freeList  []frameID               // frames holding no page, free to fill
	replacer  *LRUReplacer            // idle resident frames, ordered for eviction

	mu sync.Mutex
}

// NewBufferPool builds a pool of poolSize frames over dm. Every frame's backing
// page is allocated up front and reused for the pool's lifetime: loading a page
// overwrites a frame's bytes in place (disk.ReadPage into frame.Bytes()), so the
// run-time page path never allocates. All frames start free.
//
// poolSize is a start-up configuration value, not run-time input, so an invalid
// size is a programming error: NewBufferPool panics rather than returning an
// error the caller would have to handle on a path that can never legitimately
// occur.
func NewBufferPool(dm diskManager, poolSize int) *BufferPool {
	if poolSize <= 0 {
		panic(fmt.Sprintf("buffer: pool size must be positive, got %d", poolSize))
	}

	frames := make([]*page.SlottedPage, poolSize)
	freeList := make([]frameID, poolSize)
	for i := range frames {
		frames[i] = page.NewSlottedPage([page.PageSize]byte{})
		freeList[i] = frameID(i)
	}

	return &BufferPool{
		disk:      dm,
		frames:    frames,
		meta:      make([]frameMeta, poolSize),
		pageTable: make(map[disk.PageID]frameID),
		freeList:  freeList,
		replacer:  NewLRUReplacer(),
	}
}

// FetchPage returns the page with the given id, pinned so it will not be evicted
// until the caller releases it with UnpinPage. A resident page is served from
// memory; otherwise it is loaded from disk into a free frame, evicting a
// least-recently-used unpinned page first when no frame is free. It returns
// ErrNoFreeFrame if every frame is pinned.
//
// The returned pointer is valid only until the matching UnpinPage: once
// unpinned the frame may be reused for another page, so callers must not retain
// it past that point.
func (bp *BufferPool) FetchPage(pid disk.PageID) (*page.SlottedPage, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	// Hit: already resident. Pin it and take it out of the eviction candidates
	// (Pin is a no-op if it was already pinned, hence not a candidate).
	if f, ok := bp.pageTable[pid]; ok {
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
	if err := bp.disk.ReadPage(pid, bp.frames[f].Bytes()); err != nil {
		bp.freeList = append(bp.freeList, f)
		return nil, fmt.Errorf("buffer: load page %d: %w", pid, err)
	}

	bp.bindFrame(f, pid)
	return bp.frames[f], nil
}

// NewPage allocates a brand-new page on disk, loads it into a frame pinned to
// the caller, and returns it together with its id. Like FetchPage, the page is
// pinned until UnpinPage and returns ErrNoFreeFrame when no frame can be freed.
//
// A frame is acquired before the disk page is allocated, on purpose: acquiring
// can fail (pool full) and undoes cleanly, whereas AllocatePage grows the file
// and cannot be undone in v1. Doing the reversible step first means a full pool
// never leaks an unreferenced page on disk.
func (bp *BufferPool) NewPage() (*page.SlottedPage, disk.PageID, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	f, err := bp.acquireFrame()
	if err != nil {
		return nil, 0, err
	}

	pid, err := bp.disk.AllocatePage()
	if err != nil {
		bp.freeList = append(bp.freeList, f) // give the frame back untouched
		return nil, 0, fmt.Errorf("buffer: allocate page: %w", err)
	}

	// Format the frame into a valid empty page in memory. AllocatePage wrote a
	// zero page to disk, but a zeroed page is not a usable SlottedPage (pd_upper
	// would be 0 and SlotCount would underflow), so Init sets the boundary
	// pointers. Init also clears any bytes left by the frame's previous occupant,
	// so no reading back from disk is needed.
	bp.frames[f].Init()

	bp.bindFrame(f, pid)
	// The formatted header lives only in memory; disk still holds the zero page
	// AllocatePage wrote. Mark the page dirty so the format reaches disk on
	// flush — a clean eviction would otherwise drop it, leaving an invalid zero
	// page on disk.
	bp.meta[f].dirty = true
	return bp.frames[f], pid, nil
}

// UnpinPage releases one pin the caller holds on pid, reporting via dirty
// whether it modified the page. When the last pin is released the frame becomes
// an eviction candidate again.
//
// dirty is merged, never cleared: a page stays dirty until it is flushed, so a
// read-only unpin (dirty=false) cannot mask an earlier writer's change. Only
// FlushPage clears it.
//
// Unpinning a page that is not resident, or one whose pin count is already
// zero, is a caller contract violation (unbalanced pin/unpin) and returns an
// error without touching any state.
func (bp *BufferPool) UnpinPage(pid disk.PageID, dirty bool) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	f, ok := bp.pageTable[pid]
	if !ok {
		return fmt.Errorf("buffer: unpin page %d that is not resident", pid)
	}
	if bp.meta[f].pinCount == 0 {
		return fmt.Errorf("buffer: unpin page %d that is not pinned", pid)
	}

	bp.meta[f].dirty = bp.meta[f].dirty || dirty
	bp.meta[f].pinCount--
	if bp.meta[f].pinCount == 0 {
		bp.replacer.Unpin(f)
	}
	return nil
}

// bindFrame records frame f as holding page pid with a single pin: it writes the
// frame's metadata and the pageTable entry, and deliberately leaves the frame
// out of the replacer, since a pinned frame is never an eviction candidate. It
// is the shared tail of FetchPage and NewPage. The pool lock must be held.
func (bp *BufferPool) bindFrame(f frameID, pid disk.PageID) {
	bp.meta[f] = frameMeta{pid: pid, pinCount: 1, dirty: false}
	bp.pageTable[pid] = f
}

// FlushPage writes page pid back to disk when it is dirty and marks it clean. A
// clean page is a no-op — memory already matches disk. Flushing neither unpins
// nor evicts the page; it only reconciles memory with disk. It returns an error
// if pid is not resident.
//
// This is the only place the dirty flag is cleared, closing the lifecycle
// UnpinPage opens (UnpinPage only ever sets dirty). The write reaches only the
// OS page cache; call Sync for a durability barrier.
//
// v1 has no per-page latch, so the byte copy is not isolated from a caller
// concurrently mutating the page it holds pinned — flush a page only when no
// writer is actively changing it.
func (bp *BufferPool) FlushPage(pid disk.PageID) error {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	f, ok := bp.pageTable[pid]
	if !ok {
		return fmt.Errorf("buffer: flush page %d that is not resident", pid)
	}
	return bp.flushLocked(f)
}

// FlushAll writes every dirty resident page back to disk and marks them clean;
// clean pages are skipped. Like FlushPage the writes reach only the OS page
// cache — call Sync afterwards for durability, where a single Sync covers the
// whole batch. Typically used at a checkpoint or when closing the pool.
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

// Sync forces everything already written to the file out of the OS page cache
// to the physical device (fsync). Flush/FlushAll only push bytes to the OS;
// Sync is the separate durability barrier, so the caller pays fsync's cost only
// when durability is needed — e.g. once after FlushAll at a checkpoint.
//
// It takes no pool lock: it touches no pool metadata, and disk.Sync is itself
// safe without one, so a slow fsync does not block concurrent page traffic.
func (bp *BufferPool) Sync() error {
	return bp.disk.Sync()
}

// flushLocked writes frame f back to disk when it is dirty and clears the flag.
// A clean frame is a no-op. The pool lock must be held; it is the shared core of
// FlushPage and FlushAll.
func (bp *BufferPool) flushLocked(f frameID) error {
	if !bp.meta[f].dirty {
		return nil
	}
	pid := bp.meta[f].pid
	if err := bp.disk.WritePage(pid, bp.frames[f].Bytes()); err != nil {
		return fmt.Errorf("buffer: flush page %d: %w", pid, err)
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

	oldpid := bp.meta[v].pid
	if bp.meta[v].dirty {
		if err := bp.disk.WritePage(oldpid, bp.frames[v].Bytes()); err != nil {
			bp.replacer.Unpin(v) // undo Victim's removal; oldpid stays resident
			return 0, fmt.Errorf("buffer: flush victim page %d: %w", oldpid, err)
		}
	}
	delete(bp.pageTable, oldpid)
	return v, nil
}
