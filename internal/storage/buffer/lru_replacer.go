// Package buffer is the storage engine's page cache. It keeps a fixed number of
// disk pages in memory (frames), serves them to callers, and writes dirty ones
// back to disk, loading and evicting pages on demand via the disk manager.
package buffer

import "container/list"

// frameID indexes a frame in the buffer pool: 0 .. poolSize-1.
type frameID int

// LRUReplacer tracks which frames are currently evictable (pin count 0) and, on
// request, gives up the least-recently-used one.
//
// It reasons only about frame ids, never pages. It is NOT safe for concurrent
// use on its own: the buffer pool always calls it while holding the pool lock,
// so a second lock here would be redundant.
//
// Recency is approximated by unpin order: a frame is placed at the front (most
// recent) when unpinned, and the victim is taken from the back (least recent).
// O(1) for every operation via a doubly-linked list plus a map from frame id to
// its list element, which is what lets Pin remove an arbitrary frame in O(1).
type LRUReplacer struct {
	order *list.List                // front = most recently used, back = least
	index map[frameID]*list.Element // frame id -> its element in order
}

func NewLRUReplacer() *LRUReplacer {
	return &LRUReplacer{
		order: list.New(),
		index: make(map[frameID]*list.Element),
	}
}

// Unpin marks frame f evictable, placing it at the most-recently-used end.
// A frame already tracked keeps its position — being unpinned again does not
// reorder it (its recency was set the first time it became evictable).
func (r *LRUReplacer) Unpin(f frameID) {
	if _, ok := r.index[f]; ok {
		return
	}
	r.index[f] = r.order.PushFront(f)
}

// Pin marks frame f no longer evictable, removing it from the candidate set.
// A no-op if f is not currently tracked.
func (r *LRUReplacer) Pin(f frameID) {
	e, ok := r.index[f]
	if !ok {
		return
	}
	r.order.Remove(e)
	delete(r.index, f)
}

// Victim removes and returns the least-recently-used evictable frame. The
// boolean is false when there is no evictable frame.
func (r *LRUReplacer) Victim() (frameID, bool) {
	e := r.order.Back()
	if e == nil {
		return 0, false
	}
	f := e.Value.(frameID)
	r.order.Remove(e)
	delete(r.index, f)
	return f, true
}

// Size reports how many frames are currently evictable.
func (r *LRUReplacer) Size() int {
	return r.order.Len()
}
