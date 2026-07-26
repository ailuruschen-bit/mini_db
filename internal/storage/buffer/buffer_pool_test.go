package buffer_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/storage/buffer"
	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// probeOffset is a byte in the page body (past the header, well before the end)
// used as a recognisable marker so a page's contents can be told apart across
// eviction, flush and reopen.
const probeOffset = 100

func writeProbe(p *page.SlottedPage, b byte) { p.Bytes()[probeOffset] = b }
func readProbe(p *page.SlottedPage) byte      { return p.Bytes()[probeOffset] }

// mustPool opens a real disk manager over a fresh temp file and wraps it in a
// pool of the given size. It returns the pool and the file path (so a test can
// reopen the file independently).
func mustPool(t *testing.T, poolSize int) (*buffer.BufferPool, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "buf.db")
	dm, err := disk.Open(path)
	if err != nil {
		t.Fatalf("open disk: %v", err)
	}
	t.Cleanup(func() { dm.Close() })
	return buffer.NewBufferPool(dm, poolSize), path
}

// readProbeFromDisk opens the file independently and reads the probe byte of a
// page straight from disk, bypassing the pool — used to prove a write actually
// reached the disk layer.
func readProbeFromDisk(t *testing.T, path string, pid disk.PageID) byte {
	t.Helper()
	dm, err := disk.Open(path)
	if err != nil {
		t.Fatalf("reopen disk: %v", err)
	}
	defer dm.Close()
	buf := make([]byte, page.PageSize)
	if err := dm.ReadPage(pid, buf); err != nil {
		t.Fatalf("read page %d: %v", pid, err)
	}
	return buf[probeOffset]
}

// While a page stays resident, fetching it returns the very same page instance.
func TestFetchReturnsSameInstanceWhileResident(t *testing.T) {
	bp, _ := mustPool(t, 3)

	p, pid, err := bp.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if err := bp.UnpinPage(pid, false); err != nil {
		t.Fatal(err)
	}

	got, err := bp.FetchPage(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Error("FetchPage returned a different *SlottedPage for a resident page")
	}
	bp.UnpinPage(pid, false)
}

// NewPage returns a valid, ready-to-use empty page, not a bag of zeros.
func TestNewPageReturnsFormattedEmptyPage(t *testing.T) {
	bp, _ := mustPool(t, 2)

	p, _, err := bp.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	if got := p.SlotCount(); got != 0 {
		t.Errorf("SlotCount = %d, want 0", got)
	}
	if got := p.Header().PdUpper(); got != page.HeaderSize {
		t.Errorf("PdUpper = %d, want %d", got, page.HeaderSize)
	}
	if got := p.Header().PdLower(); got != page.PageSize {
		t.Errorf("PdLower = %d, want %d", got, page.PageSize)
	}
}

// Each NewPage extends the file, so ids come out consecutively.
func TestNewPageAssignsIncreasingIDs(t *testing.T) {
	bp, _ := mustPool(t, 5)

	for want := disk.PageID(0); want < 3; want++ {
		_, pid, err := bp.NewPage()
		if err != nil {
			t.Fatal(err)
		}
		if pid != want {
			t.Errorf("NewPage id = %d, want %d", pid, want)
		}
		bp.UnpinPage(pid, true)
	}
}

// A dirty page evicted to make room is written back, so re-fetching it reloads
// the same contents from disk.
func TestDirtyPageSurvivesEviction(t *testing.T) {
	bp, _ := mustPool(t, 2)

	p0, pid0, _ := bp.NewPage()
	writeProbe(p0, 0xAA)
	bp.UnpinPage(pid0, true)

	_, pid1, _ := bp.NewPage()
	bp.UnpinPage(pid1, true)

	// A third page forces the least-recently-used (pid0) out; being dirty it is
	// written back to disk during eviction.
	_, pid2, _ := bp.NewPage()
	bp.UnpinPage(pid2, true)

	got, err := bp.FetchPage(pid0) // reloads pid0 from disk
	if err != nil {
		t.Fatal(err)
	}
	if b := readProbe(got); b != 0xAA {
		t.Errorf("probe after eviction+reload = %#x, want 0xAA (write-back lost)", b)
	}
	bp.UnpinPage(pid0, false)
}

// The dirty flag is a cooperative contract: a change unpinned as clean is not
// written back, and is silently lost on eviction. This pins that behaviour down.
func TestReadOnlyUnpinDoesNotPersist(t *testing.T) {
	bp, path := mustPool(t, 1)

	// Establish pid0 on disk with a known probe.
	p0, pid0, _ := bp.NewPage()
	writeProbe(p0, 0xAA)
	bp.UnpinPage(pid0, true)
	if err := bp.FlushPage(pid0); err != nil {
		t.Fatal(err)
	}

	// Modify the page but report it clean.
	p, _ := bp.FetchPage(pid0)
	writeProbe(p, 0xBB)
	bp.UnpinPage(pid0, false) // change not reported

	// Force eviction (pool size 1): the clean page is dropped, not written.
	_, pid1, _ := bp.NewPage()
	bp.UnpinPage(pid1, true)

	if got := readProbeFromDisk(t, path, pid0); got != 0xAA {
		t.Errorf("disk probe = %#x, want 0xAA (an unreported change must not persist)", got)
	}
}

// FlushPage pushes a dirty page's bytes to the disk layer without evicting it.
func TestFlushPagePushesToDisk(t *testing.T) {
	bp, path := mustPool(t, 2)

	p, pid, _ := bp.NewPage()
	writeProbe(p, 0xCC)
	bp.UnpinPage(pid, true)

	if err := bp.FlushPage(pid); err != nil {
		t.Fatal(err)
	}
	if got := readProbeFromDisk(t, path, pid); got != 0xCC {
		t.Errorf("disk probe after FlushPage = %#x, want 0xCC", got)
	}

	// The page is now clean, so a second flush is a no-op that still succeeds.
	if err := bp.FlushPage(pid); err != nil {
		t.Errorf("re-flushing a clean page: %v", err)
	}
}

// dirty is sticky: once any holder reports a change, a later clean unpin must
// not clear it, or the change would be lost.
func TestDirtyIsStickyAcrossUnpins(t *testing.T) {
	bp, path := mustPool(t, 2)

	// Start from a clean, flushed page (probe 0x00 on disk).
	_, pid, _ := bp.NewPage()
	bp.UnpinPage(pid, false)
	if err := bp.FlushPage(pid); err != nil {
		t.Fatal(err)
	}

	p, _ := bp.FetchPage(pid) // holder 1
	if _, err := bp.FetchPage(pid); err != nil {
		t.Fatal(err) // holder 2
	}
	writeProbe(p, 0xDD)
	bp.UnpinPage(pid, true)  // writer reports the change
	bp.UnpinPage(pid, false) // reader reports clean — must not clear dirty

	if err := bp.FlushPage(pid); err != nil {
		t.Fatal(err)
	}
	if got := readProbeFromDisk(t, path, pid); got != 0xDD {
		t.Errorf("disk probe = %#x, want 0xDD (a clean unpin wrongly cleared dirty)", got)
	}
}

// Unbalanced unpins are contract violations and return an error.
func TestUnpinErrors(t *testing.T) {
	bp, _ := mustPool(t, 2)

	if err := bp.UnpinPage(0, false); err == nil {
		t.Error("unpinning a non-resident page did not error")
	}

	_, pid, _ := bp.NewPage()
	bp.UnpinPage(pid, false) // pin count -> 0
	if err := bp.UnpinPage(pid, false); err == nil {
		t.Error("unpinning a page with zero pins did not error")
	}
}

// Flushing a page that is not resident is a contract violation.
func TestFlushNonresidentErrors(t *testing.T) {
	bp, _ := mustPool(t, 2)
	if err := bp.FlushPage(0); err == nil {
		t.Error("flushing a non-resident page did not error")
	}
}

// Fetching a page that was never allocated on disk fails (and, verified in the
// white-box test, returns the frame to the free list).
func TestFetchOutOfRangeErrors(t *testing.T) {
	bp, _ := mustPool(t, 2)
	if _, err := bp.FetchPage(999); err == nil {
		t.Error("fetching a page that does not exist on disk did not error")
	}
}

// When every frame is pinned, there is nothing to evict: NewPage reports
// ErrNoFreeFrame, and freeing a pin makes room again.
func TestPoolFullReturnsErrNoFreeFrame(t *testing.T) {
	bp, _ := mustPool(t, 2)

	_, pid0, err := bp.NewPage() // pinned, kept
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := bp.NewPage(); err != nil { // pinned, kept
		t.Fatal(err)
	}

	if _, _, err := bp.NewPage(); !errors.Is(err, buffer.ErrNoFreeFrame) {
		t.Errorf("NewPage on a full pool: err = %v, want ErrNoFreeFrame", err)
	}

	bp.UnpinPage(pid0, false)
	if _, _, err := bp.NewPage(); err != nil {
		t.Errorf("NewPage after freeing a pin: %v", err)
	}
}

// The full durability path: create pages, flush, sync, close, reopen, read back.
func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.db")
	probes := []byte{0x11, 0x22, 0x33}

	// Session 1: write and persist.
	dm1, err := disk.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	bp1 := buffer.NewBufferPool(dm1, 4)
	for i, b := range probes {
		p, pid, err := bp1.NewPage()
		if err != nil {
			t.Fatal(err)
		}
		if pid != disk.PageID(i) {
			t.Fatalf("pid = %d, want %d", pid, i)
		}
		writeProbe(p, b)
		bp1.UnpinPage(pid, true)
	}
	if err := bp1.FlushAll(); err != nil {
		t.Fatal(err)
	}
	if err := bp1.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := dm1.Close(); err != nil {
		t.Fatal(err)
	}

	// Session 2: reopen and verify.
	dm2, err := disk.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dm2.Close()
	bp2 := buffer.NewBufferPool(dm2, 4)
	for i, want := range probes {
		p, err := bp2.FetchPage(disk.PageID(i))
		if err != nil {
			t.Fatal(err)
		}
		if got := readProbe(p); got != want {
			t.Errorf("page %d probe after reopen = %#x, want %#x", i, got, want)
		}
		bp2.UnpinPage(disk.PageID(i), false)
	}
}

// Many goroutines hammer the pool concurrently, each owning a disjoint set of
// pages so no two ever touch the same page's contents (v1 has no per-page
// latch). This exercises the metadata lock under -race, through heavy eviction.
func TestConcurrentPartitionedAccess(t *testing.T) {
	const (
		poolSize   = 8
		workers    = 6
		pagesEach  = 5
		iterations = 40
	)
	bp, _ := mustPool(t, poolSize)

	// Pre-allocate every worker's private pages: worker w owns pids
	// [w*pagesEach, (w+1)*pagesEach). Pool < total pages, so access evicts.
	total := workers * pagesEach
	for i := 0; i < total; i++ {
		_, pid, err := bp.NewPage()
		if err != nil {
			t.Fatal(err)
		}
		if pid != disk.PageID(i) {
			t.Fatalf("setup pid = %d, want %d", pid, i)
		}
		bp.UnpinPage(pid, true)
	}
	if err := bp.FlushAll(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for it := 0; it < iterations; it++ {
				for k := 0; k < pagesEach; k++ {
					pid := disk.PageID(w*pagesEach + k)
					p, err := bp.FetchPage(pid)
					if err != nil {
						t.Errorf("worker %d fetch %d: %v", w, pid, err)
						return
					}
					// The pin holds the frame, so no one can evict it while this
					// worker writes — its content stays race-free.
					writeProbe(p, byte(w+1))
					if err := bp.UnpinPage(pid, true); err != nil {
						t.Errorf("worker %d unpin %d: %v", w, pid, err)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()

	// Each page keeps its owner's probe; all pins are balanced (fetch succeeds).
	for w := 0; w < workers; w++ {
		for k := 0; k < pagesEach; k++ {
			pid := disk.PageID(w*pagesEach + k)
			p, err := bp.FetchPage(pid)
			if err != nil {
				t.Fatal(err)
			}
			if got := readProbe(p); got != byte(w+1) {
				t.Errorf("page %d probe = %#x, want %#x", pid, got, byte(w+1))
			}
			bp.UnpinPage(pid, false)
		}
	}
}
