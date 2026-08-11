// Package nbtree implements a B+Tree index: a sorted map from an int64 key to a
// heap.TID, stored on disk one node per page over its own index file. See
// docs/design/access/BTree-Index-Design.md for the full design.
package nbtree

import (
	"encoding/binary"

	"github.com/ailuruschen-bit/minidb/internal/access/heap"
	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// Node kinds, stored in the node_type header byte.
const (
	nodeInternal uint8 = 0
	nodeLeaf     uint8 = 1
)

// Node header layout (8 bytes), big-endian. Shared by both node kinds.
const (
	offNodeType = 0 // uint8: nodeInternal / nodeLeaf
	// offset 1: reserved (future: level / flags)
	offKeyCount = 2 // uint16: number of live keys N
	offNextLeaf = 4 // uint32: right-sibling leaf PID (leaves only)
	nodeHeader  = 8
)

// Entry sizes.
const (
	keySize   = 8 // int64
	childSize = 4 // disk.PageID
	tidSize   = 6 // heap.TID = PageID(4) + Slot(2)
)

// internalCap / leafCap are the key capacities of the two node kinds. A node
// splits the moment an insert fills it to capacity, so these are also the
// physical array sizes. The defaults come from the 8192-byte page budget (design
// doc §5.6): 681 keys (682 children) internal, 584 keys (584 TIDs) leaf.
//
// They are var, not const, only so tests can shrink them to force splits on tiny
// trees; production never changes them. The second array's start offset derives
// from the key capacity — it is a constant for a given capacity, so inserting a
// key shifts only the keys tail and never displaces the second array.
var (
	internalCap = 681
	leafCap     = 584
)

func childAreaOff() int { return nodeHeader + internalCap*keySize }
func tidAreaOff() int   { return nodeHeader + leafCap*keySize }

// invalidPID marks "no page" (e.g. the last leaf's next pointer). Page 0 is
// always the meta page, never a node, so 0 is a safe nil sentinel.
const invalidPID disk.PageID = 0

// node is a B+Tree node view over a raw page frame: it aliases the frame's bytes
// (via page.Bytes) and reads/writes structured fields in place, the way the heap
// overlays a SlottedPage. It copies nothing, so it is valid only while the frame
// is pinned. Build one with asNode.
type node struct {
	data []byte
}

func asNode(p *page.Page) node { return node{p.Bytes()} }

// --- header ---

func (n node) nodeType() uint8 { return n.data[offNodeType] }
func (n node) isLeaf() bool    { return n.nodeType() == nodeLeaf }

func (n node) keyCount() uint16     { return binary.BigEndian.Uint16(n.data[offKeyCount:]) }
func (n node) setKeyCount(k uint16) { binary.BigEndian.PutUint16(n.data[offKeyCount:], k) }

func (n node) nextLeaf() disk.PageID {
	return disk.PageID(binary.BigEndian.Uint32(n.data[offNextLeaf:]))
}
func (n node) setNextLeaf(pid disk.PageID) {
	binary.BigEndian.PutUint32(n.data[offNextLeaf:], uint32(pid))
}

// initLeaf / initInternal format a raw frame into an empty node of the given
// kind: they zero the frame (so no bytes of a previous occupant survive) and set
// the node type. key_count and next_leaf are left at zero (empty, no sibling).
func (n node) initLeaf()     { n.initType(nodeLeaf) }
func (n node) initInternal() { n.initType(nodeInternal) }

func (n node) initType(kind uint8) {
	clear(n.data)
	n.data[offNodeType] = kind
}

// --- keys (both kinds; the key array starts right after the header) ---

func (n node) keyAt(i uint16) int64 {
	off := nodeHeader + int(i)*keySize
	return int64(binary.BigEndian.Uint64(n.data[off:]))
}
func (n node) setKeyAt(i uint16, key int64) {
	off := nodeHeader + int(i)*keySize
	binary.BigEndian.PutUint64(n.data[off:], uint64(key))
}

// --- children (internal only) ---

func (n node) childAt(i uint16) disk.PageID {
	off := childAreaOff() + int(i)*childSize
	return disk.PageID(binary.BigEndian.Uint32(n.data[off:]))
}
func (n node) setChildAt(i uint16, pid disk.PageID) {
	off := childAreaOff() + int(i)*childSize
	binary.BigEndian.PutUint32(n.data[off:], uint32(pid))
}

// --- values / TIDs (leaf only) ---

func (n node) tidAt(i uint16) heap.TID {
	off := tidAreaOff() + int(i)*tidSize
	return heap.TID{
		Page: disk.PageID(binary.BigEndian.Uint32(n.data[off:])),
		Slot: binary.BigEndian.Uint16(n.data[off+childSize:]),
	}
}
func (n node) setTIDAt(i uint16, tid heap.TID) {
	off := tidAreaOff() + int(i)*tidSize
	binary.BigEndian.PutUint32(n.data[off:], uint32(tid.Page))
	binary.BigEndian.PutUint16(n.data[off+childSize:], tid.Slot)
}

// --- search ---

// search binary-searches the key array for key. idx is the lower bound: the
// first position with keys[idx] >= key (== keyCount if key exceeds every key).
// found reports whether keys[idx] == key. On a leaf, idx is the slot to read or
// to insert at; on an internal node, childIndex derives the descent from it.
func (n node) search(key int64) (idx uint16, found bool) {
	lo, hi := uint16(0), n.keyCount()
	for lo < hi {
		mid := (lo + hi) / 2
		if n.keyAt(mid) < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, lo < n.keyCount() && n.keyAt(lo) == key
}

// childIndex returns the child to descend into for key: the count of keys <= key
// (upper bound). child c[i] covers keys in [key[i-1], key[i]), and c[N] covers
// keys >= key[N-1], so a key equal to a separator descends right.
func (n node) childIndex(key int64) uint16 {
	idx, found := n.search(key)
	if found {
		return idx + 1
	}
	return idx
}

// --- capacity ---

// full reports whether the node has reached its key capacity and must split. An
// insert always fits first (a resting node holds fewer than capacity keys), then
// the caller splits when this returns true.
func (n node) full() bool {
	if n.isLeaf() {
		return int(n.keyCount()) >= leafCap
	}
	return int(n.keyCount()) >= internalCap
}

// --- mutation ---

// insertLeafAt inserts (key, tid) at slot i, shifting the keys and TIDs from i
// onward right by one. Only this leaf's own tails move; the TID array's base is
// fixed, so the keys insert never displaces it. The caller guarantees room.
func (n node) insertLeafAt(i uint16, key int64, tid heap.TID) {
	cnt := n.keyCount()
	for j := cnt; j > i; j-- {
		n.setKeyAt(j, n.keyAt(j-1))
		n.setTIDAt(j, n.tidAt(j-1))
	}
	n.setKeyAt(i, key)
	n.setTIDAt(i, tid)
	n.setKeyCount(cnt + 1)
}

// insertInternalAt inserts separator key at slot i and its right child pointer
// at child slot i+1, shifting the keys from i and the children from i+1 right by
// one. The existing child[i] stays as the separator's left child. The caller
// guarantees room.
func (n node) insertInternalAt(i uint16, key int64, rightChild disk.PageID) {
	cnt := n.keyCount()
	for j := cnt; j > i; j-- {
		n.setKeyAt(j, n.keyAt(j-1))
	}
	n.setKeyAt(i, key)
	for j := cnt + 1; j > i+1; j-- {
		n.setChildAt(j, n.childAt(j-1))
	}
	n.setChildAt(i+1, rightChild)
	n.setKeyCount(cnt + 1)
}

// splitLeaf moves the upper half of a full leaf's entries into right (a freshly
// initialised empty leaf), leaving the lower half in n. It fixes neither the
// sibling chain nor the separator — the caller does that (copy-up: the separator
// is right.keyAt(0)).
func (n node) splitLeaf(right node) {
	cnt := n.keyCount()
	mid := cnt / 2
	for j := mid; j < cnt; j++ {
		right.setKeyAt(j-mid, n.keyAt(j))
		right.setTIDAt(j-mid, n.tidAt(j))
	}
	right.setKeyCount(cnt - mid)
	n.setKeyCount(mid)
}

// splitInternal divides a full internal node, moving the upper keys/children
// into right (a freshly initialised empty internal node) and returning the
// middle key to push up. The middle key is removed from both halves — it becomes
// the separator between them in the parent (push-up). n keeps keys [0,mid) with
// children [0,mid]; right gets keys (mid,cnt) with children [mid+1,cnt].
func (n node) splitInternal(right node) (upKey int64) {
	cnt := n.keyCount()
	mid := cnt / 2
	upKey = n.keyAt(mid)
	for j := mid + 1; j < cnt; j++ {
		right.setKeyAt(j-mid-1, n.keyAt(j))
	}
	for j := mid + 1; j <= cnt; j++ {
		right.setChildAt(j-mid-1, n.childAt(j))
	}
	right.setKeyCount(cnt - mid - 1)
	n.setKeyCount(mid)
	return upKey
}
