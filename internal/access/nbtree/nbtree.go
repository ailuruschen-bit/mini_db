package nbtree

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ailuruschen-bit/minidb/internal/access/heap"
	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// ErrDuplicateKey is returned by Insert when the key already exists. v1 indexes
// are unique; duplicate-key support is deferred (see design doc).
var ErrDuplicateKey = errors.New("nbtree: duplicate key")

// pager is the slice of the buffer pool the index depends on, declared here
// consumer-side so the index couples to just these operations rather than the
// whole *buffer.BufferPool. It matches the heap's pager. *buffer.BufferPool
// satisfies it. The pool serves raw frames; nbtree overlays its node/meta views.
type pager interface {
	FetchPage(pid disk.PageID) (*page.Page, error)
	NewPage() (*page.Page, disk.PageID, error)
	UnpinPage(pid disk.PageID, dirty bool) error
	NumPages() disk.PageID
}

// Meta page (page 0): the fixed anchor recording the current root. It is not a
// node — the root's page id moves when the root splits, so it cannot itself be a
// fixed location.
const (
	metaPID   disk.PageID = 0
	metaMagic uint32      = 0x6E627472 // "nbtr"
	offMagic              = 0          // uint32
	offRoot               = 4          // uint32: current root PID
)

type metaPage struct{ data []byte }

func asMeta(p *page.Page) metaPage { return metaPage{p.Bytes()} }

func (m metaPage) magic() uint32          { return binary.BigEndian.Uint32(m.data[offMagic:]) }
func (m metaPage) setMagic(v uint32)      { binary.BigEndian.PutUint32(m.data[offMagic:], v) }
func (m metaPage) root() disk.PageID      { return disk.PageID(binary.BigEndian.Uint32(m.data[offRoot:])) }
func (m metaPage) setRoot(pid disk.PageID) { binary.BigEndian.PutUint32(m.data[offRoot:], uint32(pid)) }

// BTree is a B+Tree index over one index file, mapping int64 keys to heap TIDs.
//
// It has no lock of its own and is not safe for concurrent use in v1: like the
// heap, it mutates page contents outside the pool lock, which v1 does not
// isolate (no per-page latch yet).
type BTree struct {
	pool pager
}

// NewBTree opens (or, on an empty file, bootstraps) a B+Tree over pool. The pool
// is shared and owned elsewhere; the tree only borrows it. Because a descent
// pins one page per level and holds parents pinned to absorb splits, the pool
// must have more frames than the tree is tall.
func NewBTree(pool pager) (*BTree, error) {
	t := &BTree{pool: pool}
	if pool.NumPages() == 0 {
		return t, t.bootstrap()
	}
	// Existing file: check it is an nbtree index.
	mp, err := pool.FetchPage(metaPID)
	if err != nil {
		return nil, fmt.Errorf("nbtree: open: %w", err)
	}
	ok := asMeta(mp).magic() == metaMagic
	_ = pool.UnpinPage(metaPID, false)
	if !ok {
		return nil, errors.New("nbtree: not an index file (bad magic)")
	}
	return t, nil
}

// bootstrap lays out a fresh index file: a meta page at page 0 and an empty root
// leaf at page 1, with the meta pointing at the root.
func (t *BTree) bootstrap() error {
	mp, mpid, err := t.pool.NewPage()
	if err != nil {
		return fmt.Errorf("nbtree: bootstrap meta: %w", err)
	}
	rp, rpid, err := t.pool.NewPage()
	if err != nil {
		_ = t.pool.UnpinPage(mpid, false)
		return fmt.Errorf("nbtree: bootstrap root: %w", err)
	}
	asNode(rp).initLeaf()
	m := asMeta(mp)
	m.setMagic(metaMagic)
	m.setRoot(rpid)
	_ = t.pool.UnpinPage(rpid, true)
	_ = t.pool.UnpinPage(mpid, true)
	return nil
}

// root reads the current root PID from the meta page.
func (t *BTree) root() (disk.PageID, error) {
	mp, err := t.pool.FetchPage(metaPID)
	if err != nil {
		return 0, fmt.Errorf("nbtree: read meta: %w", err)
	}
	root := asMeta(mp).root()
	_ = t.pool.UnpinPage(metaPID, false)
	return root, nil
}

// setRoot records a new root PID in the meta page (after a root split).
func (t *BTree) setRoot(pid disk.PageID) error {
	mp, err := t.pool.FetchPage(metaPID)
	if err != nil {
		return fmt.Errorf("nbtree: update meta: %w", err)
	}
	asMeta(mp).setRoot(pid)
	_ = t.pool.UnpinPage(metaPID, true)
	return nil
}

// Search looks up key, returning its TID and whether it was found.
func (t *BTree) Search(key int64) (heap.TID, bool, error) {
	pid, err := t.root()
	if err != nil {
		return heap.TID{}, false, err
	}
	for {
		p, err := t.pool.FetchPage(pid)
		if err != nil {
			return heap.TID{}, false, fmt.Errorf("nbtree: search page %d: %w", pid, err)
		}
		n := asNode(p)
		if n.isLeaf() {
			idx, found := n.search(key)
			var tid heap.TID
			if found {
				tid = n.tidAt(idx)
			}
			_ = t.pool.UnpinPage(pid, false)
			return tid, found, nil
		}
		child := n.childAt(n.childIndex(key))
		_ = t.pool.UnpinPage(pid, false)
		pid = child
	}
}

// Scan visits every entry with lo <= key <= hi in key order, calling fn for
// each. It descends to the leaf holding lo, then walks the leaf chain via
// next_leaf. fn returns an error to stop early; that error, and any I/O error,
// is returned from Scan.
func (t *BTree) Scan(lo, hi int64, fn func(key int64, tid heap.TID) error) error {
	pid, err := t.findLeaf(lo)
	if err != nil {
		return err
	}
	first := true
	for pid != invalidPID {
		p, err := t.pool.FetchPage(pid)
		if err != nil {
			return fmt.Errorf("nbtree: scan page %d: %w", pid, err)
		}
		n := asNode(p)
		i := uint16(0)
		if first {
			i, _ = n.search(lo) // start at the first key >= lo
			first = false
		}
		next := n.nextLeaf()
		done, cbErr := scanLeaf(n, i, hi, fn)
		_ = t.pool.UnpinPage(pid, false)
		if cbErr != nil {
			return cbErr
		}
		if done {
			return nil
		}
		pid = next
	}
	return nil
}

// scanLeaf feeds entries [i, keyCount) of a leaf to fn while key <= hi. It
// returns done=true once a key exceeds hi (the scan is finished), or the first
// callback error.
func scanLeaf(n node, i uint16, hi int64, fn func(int64, heap.TID) error) (done bool, err error) {
	for ; i < n.keyCount(); i++ {
		k := n.keyAt(i)
		if k > hi {
			return true, nil
		}
		if err := fn(k, n.tidAt(i)); err != nil {
			return false, err
		}
	}
	return false, nil
}

// findLeaf descends from the root to the leaf whose key band contains key.
func (t *BTree) findLeaf(key int64) (disk.PageID, error) {
	pid, err := t.root()
	if err != nil {
		return 0, err
	}
	for {
		p, err := t.pool.FetchPage(pid)
		if err != nil {
			return 0, fmt.Errorf("nbtree: descend page %d: %w", pid, err)
		}
		n := asNode(p)
		if n.isLeaf() {
			_ = t.pool.UnpinPage(pid, false)
			return pid, nil
		}
		child := n.childAt(n.childIndex(key))
		_ = t.pool.UnpinPage(pid, false)
		pid = child
	}
}

// split carries a node split back to the parent: the separator key and the PID
// of the new right node.
type split struct {
	sepKey int64
	right  disk.PageID
}

// Insert adds a key -> TID entry, splitting nodes as needed. It returns
// ErrDuplicateKey if the key already exists (v1 indexes are unique).
func (t *BTree) Insert(key int64, tid heap.TID) error {
	rootPID, err := t.root()
	if err != nil {
		return err
	}
	sp, err := t.insertInto(rootPID, key, tid)
	if err != nil {
		return err
	}
	if sp == nil {
		return nil
	}

	// The root split: grow the tree by one level with a new root over the old
	// root (left) and the new right node.
	p, pid, err := t.pool.NewPage()
	if err != nil {
		return fmt.Errorf("nbtree: new root: %w", err)
	}
	nr := asNode(p)
	nr.initInternal()
	nr.setKeyAt(0, sp.sepKey)
	nr.setChildAt(0, rootPID)
	nr.setChildAt(1, sp.right)
	nr.setKeyCount(1)
	_ = t.pool.UnpinPage(pid, true)
	return t.setRoot(pid)
}

// insertInto inserts (key, tid) into the subtree rooted at pid, returning a
// non-nil *split when pid overflowed and split. It keeps pid pinned across the
// descent so it can absorb a child's split on the way back up.
func (t *BTree) insertInto(pid disk.PageID, key int64, tid heap.TID) (*split, error) {
	p, err := t.pool.FetchPage(pid)
	if err != nil {
		return nil, fmt.Errorf("nbtree: fetch page %d: %w", pid, err)
	}
	n := asNode(p)

	if n.isLeaf() {
		idx, found := n.search(key)
		if found {
			_ = t.pool.UnpinPage(pid, false)
			return nil, ErrDuplicateKey
		}
		n.insertLeafAt(idx, key, tid)
		if !n.full() {
			_ = t.pool.UnpinPage(pid, true)
			return nil, nil
		}
		sp, err := t.splitLeafNode(n)
		_ = t.pool.UnpinPage(pid, true)
		return sp, err
	}

	// Internal: descend into the child, holding this node pinned.
	ci := n.childIndex(key)
	childSplit, err := t.insertInto(n.childAt(ci), key, tid)
	if err != nil {
		_ = t.pool.UnpinPage(pid, false)
		return nil, err
	}
	if childSplit == nil {
		_ = t.pool.UnpinPage(pid, false)
		return nil, nil
	}

	// Absorb the child's split: insert its separator and right child here.
	n.insertInternalAt(ci, childSplit.sepKey, childSplit.right)
	if !n.full() {
		_ = t.pool.UnpinPage(pid, true)
		return nil, nil
	}
	sp, err := t.splitInternalNode(n)
	_ = t.pool.UnpinPage(pid, true)
	return sp, err
}

// splitLeafNode splits a full leaf n: it allocates a new right leaf, moves the
// upper half into it, links it into the sibling chain, and returns the
// copy-up separator (the right leaf's first key).
func (t *BTree) splitLeafNode(n node) (*split, error) {
	rp, rpid, err := t.pool.NewPage()
	if err != nil {
		return nil, fmt.Errorf("nbtree: split leaf: %w", err)
	}
	right := asNode(rp)
	right.initLeaf()
	n.splitLeaf(right)
	right.setNextLeaf(n.nextLeaf()) // right inherits n's old successor
	n.setNextLeaf(rpid)             // n now points at right
	sep := right.keyAt(0)
	_ = t.pool.UnpinPage(rpid, true)
	return &split{sepKey: sep, right: rpid}, nil
}

// splitInternalNode splits a full internal node n: it allocates a new right
// node, moves the upper keys/children into it, and returns the pushed-up middle
// key as the separator.
func (t *BTree) splitInternalNode(n node) (*split, error) {
	rp, rpid, err := t.pool.NewPage()
	if err != nil {
		return nil, fmt.Errorf("nbtree: split internal: %w", err)
	}
	right := asNode(rp)
	right.initInternal()
	upKey := n.splitInternal(right)
	_ = t.pool.UnpinPage(rpid, true)
	return &split{sepKey: upKey, right: rpid}, nil
}
