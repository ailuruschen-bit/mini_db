package nbtree

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/ailuruschen-bit/minidb/internal/access/heap"
	"github.com/ailuruschen-bit/minidb/internal/storage/disk"
	"github.com/ailuruschen-bit/minidb/internal/storage/page"
)

// newLeaf / newInternal return a freshly formatted empty node over a blank frame.
func newLeaf() node {
	n := asNode(page.NewPage())
	n.initLeaf()
	return n
}

func newInternal() node {
	n := asNode(page.NewPage())
	n.initInternal()
	return n
}

// tid builds a recognisable TID from a seed.
func tid(seed int) heap.TID {
	return heap.TID{Page: disk.PageID(seed * 10), Slot: uint16(seed)}
}

// The fixed-offset arithmetic must keep every field of a capacity-filled node
// inside the 8192-byte page — this pins down that the reserved regions fit.
func TestCapacitiesFitThePage(t *testing.T) {
	if got := childAreaOff() + (internalCap+1)*childSize; got > int(page.PageSize) {
		t.Errorf("internal node overflows the page: end=%d > %d", got, page.PageSize)
	}
	if got := tidAreaOff() + leafCap*tidSize; got > int(page.PageSize) {
		t.Errorf("leaf node overflows the page: end=%d > %d", got, page.PageSize)
	}
	// The second array's offset must equal the header-plus-keys arithmetic.
	if childAreaOff() != nodeHeader+internalCap*keySize {
		t.Errorf("childAreaOff = %d, want %d", childAreaOff(), nodeHeader+internalCap*keySize)
	}
	if tidAreaOff() != nodeHeader+leafCap*keySize {
		t.Errorf("tidAreaOff = %d, want %d", tidAreaOff(), nodeHeader+leafCap*keySize)
	}
}

// initLeaf / initInternal set the node type and start empty, zeroing any bytes a
// previous occupant left behind.
func TestInit(t *testing.T) {
	p := page.NewPage()
	for i := range p.Bytes() {
		p.Bytes()[i] = 0xFF // simulate a previous page's bytes
	}
	n := asNode(p)

	n.initLeaf()
	if !n.isLeaf() {
		t.Error("initLeaf did not produce a leaf")
	}
	if n.keyCount() != 0 {
		t.Errorf("fresh leaf key_count = %d, want 0", n.keyCount())
	}
	if n.nextLeaf() != invalidPID {
		t.Errorf("fresh leaf next_leaf = %d, want %d", n.nextLeaf(), invalidPID)
	}

	n.initInternal()
	if n.isLeaf() {
		t.Error("initInternal produced a leaf")
	}
	if n.nodeType() != nodeInternal {
		t.Errorf("node_type = %d, want internal", n.nodeType())
	}
}

// Keys round-trip through their accessors, negatives included: keys are stored
// as raw big-endian and compared as decoded int64, so sign must survive.
func TestKeyRoundTrip(t *testing.T) {
	n := newLeaf()
	keys := []int64{0, 1, -1, 42, -42, 9223372036854775807, -9223372036854775808}
	for i, k := range keys {
		n.setKeyAt(uint16(i), k)
	}
	for i, want := range keys {
		if got := n.keyAt(uint16(i)); got != want {
			t.Errorf("keyAt(%d) = %d, want %d", i, got, want)
		}
	}
}

// Child pointers and next_leaf round-trip.
func TestChildAndNextLeaf(t *testing.T) {
	n := newInternal()
	for i := uint16(0); i < 5; i++ {
		n.setChildAt(i, disk.PageID(100+i))
	}
	for i := uint16(0); i < 5; i++ {
		if got := n.childAt(i); got != disk.PageID(100+i) {
			t.Errorf("childAt(%d) = %d, want %d", i, got, 100+i)
		}
	}

	leaf := newLeaf()
	leaf.setNextLeaf(777)
	if got := leaf.nextLeaf(); got != 777 {
		t.Errorf("nextLeaf = %d, want 777", got)
	}
}

// TIDs round-trip, page and slot both intact.
func TestTIDRoundTrip(t *testing.T) {
	n := newLeaf()
	for i := 0; i < 5; i++ {
		n.setTIDAt(uint16(i), tid(i+1))
	}
	for i := 0; i < 5; i++ {
		if got, want := n.tidAt(uint16(i)), tid(i+1); got != want {
			t.Errorf("tidAt(%d) = %+v, want %+v", i, got, want)
		}
	}
}

// search returns the lower bound and whether the key is present.
func TestSearch(t *testing.T) {
	n := newLeaf()
	// empty node: everything lands at 0, nothing found.
	if idx, found := n.search(42); idx != 0 || found {
		t.Errorf("empty search = (%d,%v), want (0,false)", idx, found)
	}

	for i, k := range []int64{10, 20, 30} {
		n.setKeyAt(uint16(i), k)
	}
	n.setKeyCount(3)

	tests := []struct {
		key     int64
		wantIdx uint16
		wantHit bool
	}{
		{5, 0, false}, {10, 0, true}, {15, 1, false}, {20, 1, true},
		{25, 2, false}, {30, 2, true}, {35, 3, false},
	}
	for _, tt := range tests {
		if idx, found := n.search(tt.key); idx != tt.wantIdx || found != tt.wantHit {
			t.Errorf("search(%d) = (%d,%v), want (%d,%v)", tt.key, idx, found, tt.wantIdx, tt.wantHit)
		}
	}
}

// childIndex descends into the child whose band contains the key; a key equal to
// a separator descends right.
func TestChildIndex(t *testing.T) {
	n := newInternal()
	for i, k := range []int64{10, 20, 30} {
		n.setKeyAt(uint16(i), k)
	}
	n.setKeyCount(3)

	tests := []struct {
		key  int64
		want uint16
	}{
		{5, 0}, {10, 1}, {15, 1}, {20, 2}, {25, 2}, {30, 3}, {35, 3},
	}
	for _, tt := range tests {
		if got := n.childIndex(tt.key); got != tt.want {
			t.Errorf("childIndex(%d) = %d, want %d", tt.key, got, tt.want)
		}
	}
}

// full reports capacity for both node kinds; a node one short is not full.
func TestFull(t *testing.T) {
	leaf := newLeaf()
	leaf.setKeyCount(uint16(leafCap - 1))
	if leaf.full() {
		t.Error("leaf one below capacity reported full")
	}
	leaf.setKeyCount(uint16(leafCap))
	if !leaf.full() {
		t.Error("leaf at capacity not reported full")
	}

	in := newInternal()
	in.setKeyCount(uint16(internalCap - 1))
	if in.full() {
		t.Error("internal one below capacity reported full")
	}
	in.setKeyCount(uint16(internalCap))
	if !in.full() {
		t.Error("internal at capacity not reported full")
	}
}

// insertLeafAt keeps keys and TIDs sorted and parallel across shifts, and can
// fill a leaf to capacity without overflowing the page.
func TestInsertLeafAt(t *testing.T) {
	n := newLeaf()
	// Insert out of order using the sorted position each time.
	for _, k := range []int64{30, 10, 50, 20, 40} {
		idx, _ := n.search(k)
		n.insertLeafAt(idx, k, tid(int(k)))
	}
	want := []int64{10, 20, 30, 40, 50}
	if int(n.keyCount()) != len(want) {
		t.Fatalf("key_count = %d, want %d", n.keyCount(), len(want))
	}
	for i, wk := range want {
		if got := n.keyAt(uint16(i)); got != wk {
			t.Errorf("keyAt(%d) = %d, want %d", i, got, wk)
		}
		if got, w := n.tidAt(uint16(i)), tid(int(wk)); got != w {
			t.Errorf("tidAt(%d) = %+v, want %+v (parallel array desynced)", i, got, w)
		}
	}

	// Fill a fresh leaf exactly to capacity: the last index must stay in-page.
	full := newLeaf()
	for i := 0; i < leafCap; i++ {
		full.insertLeafAt(full.keyCount(), int64(i), tid(i))
	}
	if !full.full() {
		t.Fatalf("leaf filled to leafCap not full (key_count=%d)", full.keyCount())
	}
	if got := full.keyAt(uint16(leafCap - 1)); got != int64(leafCap-1) {
		t.Errorf("last key = %d, want %d", got, leafCap-1)
	}
}

// insertInternalAt places the separator at slot i and its right child at i+1,
// leaving child[i] as the left child.
func TestInsertInternalAt(t *testing.T) {
	n := newInternal()
	// Start: keys [10,30], children [c0,c1,c2].
	for i, k := range []int64{10, 30} {
		n.setKeyAt(uint16(i), k)
	}
	for i := uint16(0); i < 3; i++ {
		n.setChildAt(i, disk.PageID(i))
	}
	n.setKeyCount(2)

	// Insert separator 20 with right child 99 at key slot 1.
	n.insertInternalAt(1, 20, 99)

	wantKeys := []int64{10, 20, 30}
	for i, wk := range wantKeys {
		if got := n.keyAt(uint16(i)); got != wk {
			t.Errorf("keyAt(%d) = %d, want %d", i, got, wk)
		}
	}
	// children: c0(<10), c1([10,20)), 99([20,30)), c2(>=30).
	wantChildren := []disk.PageID{0, 1, 99, 2}
	for i, wc := range wantChildren {
		if got := n.childAt(uint16(i)); got != wc {
			t.Errorf("childAt(%d) = %d, want %d", i, got, wc)
		}
	}
	if n.keyCount() != 3 {
		t.Errorf("key_count = %d, want 3", n.keyCount())
	}
}

// splitLeaf divides entries evenly; the separator is the right leaf's first key
// (copy-up: the key stays in the right leaf).
func TestSplitLeaf(t *testing.T) {
	left := newLeaf()
	keys := []int64{10, 20, 30, 40, 50, 60, 70}
	for _, k := range keys {
		left.insertLeafAt(left.keyCount(), k, tid(int(k)))
	}

	right := newLeaf()
	left.splitLeaf(right)

	mid := uint16(len(keys)) / 2 // 3
	if left.keyCount() != mid {
		t.Errorf("left key_count = %d, want %d", left.keyCount(), mid)
	}
	if right.keyCount() != uint16(len(keys))-mid {
		t.Errorf("right key_count = %d, want %d", right.keyCount(), uint16(len(keys))-mid)
	}
	// Lower half stays left, upper half moves right, TIDs following their keys.
	for i := uint16(0); i < left.keyCount(); i++ {
		if got := left.keyAt(i); got != keys[i] {
			t.Errorf("left keyAt(%d) = %d, want %d", i, got, keys[i])
		}
	}
	for i := uint16(0); i < right.keyCount(); i++ {
		wk := keys[mid+i]
		if got := right.keyAt(i); got != wk {
			t.Errorf("right keyAt(%d) = %d, want %d", i, got, wk)
		}
		if got, w := right.tidAt(i), tid(int(wk)); got != w {
			t.Errorf("right tidAt(%d) = %+v, want %+v", i, got, w)
		}
	}
	if sep := right.keyAt(0); sep != keys[mid] {
		t.Errorf("separator (copy-up) = %d, want %d", sep, keys[mid])
	}
}

// splitInternal pushes the middle key up (removed from both halves) and splits
// the children with the keys.
func TestSplitInternal(t *testing.T) {
	left := newInternal()
	keys := []int64{10, 20, 30, 40, 50}
	for i, k := range keys {
		left.setKeyAt(uint16(i), k)
	}
	for i := uint16(0); i <= uint16(len(keys)); i++ { // 6 children
		left.setChildAt(i, disk.PageID(100+i))
	}
	left.setKeyCount(uint16(len(keys)))

	right := newInternal()
	upKey := left.splitInternal(right)

	if upKey != 30 { // middle of 5 keys
		t.Errorf("push-up key = %d, want 30", upKey)
	}
	// left keeps [10,20] + children [100,101,102]
	if left.keyCount() != 2 {
		t.Errorf("left key_count = %d, want 2", left.keyCount())
	}
	for i, wk := range []int64{10, 20} {
		if got := left.keyAt(uint16(i)); got != wk {
			t.Errorf("left keyAt(%d) = %d, want %d", i, got, wk)
		}
	}
	for i, wc := range []disk.PageID{100, 101, 102} {
		if got := left.childAt(uint16(i)); got != wc {
			t.Errorf("left childAt(%d) = %d, want %d", i, got, wc)
		}
	}
	// right gets [40,50] + children [103,104,105]
	if right.keyCount() != 2 {
		t.Errorf("right key_count = %d, want 2", right.keyCount())
	}
	for i, wk := range []int64{40, 50} {
		if got := right.keyAt(uint16(i)); got != wk {
			t.Errorf("right keyAt(%d) = %d, want %d", i, got, wk)
		}
	}
	for i, wc := range []disk.PageID{103, 104, 105} {
		if got := right.childAt(uint16(i)); got != wc {
			t.Errorf("right childAt(%d) = %d, want %d", i, got, wc)
		}
	}
}

// === tree driver (nbtree.go) ===

var errBoom = errors.New("boom")

// fakePool is an in-memory pager: pages live in a slice and never evict, so a
// whole tree stays resident. failFetch / failNew inject errors on demand to
// drive the tree's I/O error paths.
type fakePool struct {
	pages     []*page.Page
	failFetch func(disk.PageID) error
	failNew   func() error
}

func (f *fakePool) FetchPage(pid disk.PageID) (*page.Page, error) {
	if f.failFetch != nil {
		if err := f.failFetch(pid); err != nil {
			return nil, err
		}
	}
	if int(pid) >= len(f.pages) {
		return nil, fmt.Errorf("fakePool: page %d out of range", pid)
	}
	return f.pages[pid], nil
}

func (f *fakePool) NewPage() (*page.Page, disk.PageID, error) {
	if f.failNew != nil {
		if err := f.failNew(); err != nil {
			return nil, 0, err
		}
	}
	p := page.NewPage()
	f.pages = append(f.pages, p)
	return p, disk.PageID(len(f.pages) - 1), nil
}

func (f *fakePool) UnpinPage(disk.PageID, bool) error { return nil }
func (f *fakePool) NumPages() disk.PageID             { return disk.PageID(len(f.pages)) }

// smallCaps shrinks the node capacities for the duration of a test, so a handful
// of keys forces leaf splits, internal splits and root growth.
func smallCaps(t *testing.T, leaf, internal int) {
	t.Helper()
	origL, origI := leafCap, internalCap
	leafCap, internalCap = leaf, internal
	t.Cleanup(func() { leafCap, internalCap = origL, origI })
}

// A scrambled batch of keys round-trips through Insert/Search, and a full Scan
// returns them all in ascending order. Tiny capacities force many leaf splits,
// internal splits and several levels of root growth.
func TestTreeInsertSearchScan(t *testing.T) {
	smallCaps(t, 4, 4)
	tr, err := NewBTree(&fakePool{})
	if err != nil {
		t.Fatal(err)
	}

	const n = 200
	for _, k := range rand.New(rand.NewSource(1)).Perm(n) {
		if err := tr.Insert(int64(k), tid(k)); err != nil {
			t.Fatalf("insert %d: %v", k, err)
		}
	}

	for k := 0; k < n; k++ {
		got, found, err := tr.Search(int64(k))
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Errorf("key %d missing", k)
		}
		if got != tid(k) {
			t.Errorf("key %d: tid = %+v, want %+v", k, got, tid(k))
		}
	}
	if _, found, _ := tr.Search(n + 100); found {
		t.Error("found a key that was never inserted")
	}

	var keys []int64
	err = tr.Scan(math.MinInt64, math.MaxInt64, func(k int64, _ heap.TID) error {
		keys = append(keys, k)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != n {
		t.Fatalf("scan returned %d keys, want %d", len(keys), n)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] <= keys[i-1] {
			t.Fatalf("scan not sorted at %d: %d then %d", i, keys[i-1], keys[i])
		}
	}
}

// A duplicate key is rejected and does not overwrite the first value.
func TestDuplicateRejected(t *testing.T) {
	smallCaps(t, 4, 4)
	tr, _ := NewBTree(&fakePool{})
	if err := tr.Insert(5, tid(5)); err != nil {
		t.Fatal(err)
	}
	if err := tr.Insert(5, tid(99)); !errors.Is(err, ErrDuplicateKey) {
		t.Errorf("second insert err = %v, want ErrDuplicateKey", err)
	}
	if got, _, _ := tr.Search(5); got != tid(5) {
		t.Errorf("value after duplicate = %+v, want the original %+v", got, tid(5))
	}
}

// Scan honours its [lo, hi] bounds and crosses leaf boundaries via next_leaf.
func TestRangeScan(t *testing.T) {
	smallCaps(t, 4, 4)
	tr, _ := NewBTree(&fakePool{})
	for k := 0; k < 20; k++ {
		if err := tr.Insert(int64(k), tid(k)); err != nil {
			t.Fatal(err)
		}
	}

	collect := func(lo, hi int64) []int64 {
		var got []int64
		if err := tr.Scan(lo, hi, func(k int64, _ heap.TID) error {
			got = append(got, k)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := collect(5, 15); !equalRange(got, 5, 15) {
		t.Errorf("Scan(5,15) = %v, want 5..15", got)
	}
	if got := collect(-100, 1000); len(got) != 20 {
		t.Errorf("Scan(all) returned %d, want 20", len(got))
	}
	if got := collect(100, 200); len(got) != 0 {
		t.Errorf("Scan of an empty range returned %v, want none", got)
	}
	if got := collect(7, 7); len(got) != 1 || got[0] != 7 {
		t.Errorf("Scan(7,7) = %v, want [7]", got)
	}
}

func equalRange(got []int64, lo, hi int64) bool {
	if len(got) != int(hi-lo+1) {
		return false
	}
	for i, v := range got {
		if v != lo+int64(i) {
			return false
		}
	}
	return true
}

// A callback error stops the scan and propagates out.
func TestScanEarlyStop(t *testing.T) {
	smallCaps(t, 4, 4)
	tr, _ := NewBTree(&fakePool{})
	for k := 0; k < 20; k++ {
		if err := tr.Insert(int64(k), tid(k)); err != nil {
			t.Fatal(err)
		}
	}

	stop := errors.New("stop")
	seen := 0
	err := tr.Scan(math.MinInt64, math.MaxInt64, func(int64, heap.TID) error {
		seen++
		if seen == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Errorf("Scan err = %v, want the sentinel", err)
	}
	if seen != 3 {
		t.Errorf("callback ran %d times, want 3", seen)
	}
}

// An empty tree: Scan visits nothing and Search finds nothing.
func TestEmptyTree(t *testing.T) {
	tr, _ := NewBTree(&fakePool{})
	if _, found, err := tr.Search(42); err != nil || found {
		t.Errorf("search on empty tree = (found %v, err %v), want (false, nil)", found, err)
	}
	n := 0
	if err := tr.Scan(math.MinInt64, math.MaxInt64, func(int64, heap.TID) error {
		n++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("scan of empty tree visited %d, want 0", n)
	}
}

// --- error paths ---

// Bootstrap surfaces a NewPage failure for the meta page and, separately, for
// the root leaf (which also exercises returning the meta frame).
func TestBootstrapErrors(t *testing.T) {
	if _, err := NewBTree(&fakePool{failNew: func() error { return errBoom }}); err == nil {
		t.Error("NewBTree did not surface the meta-page NewPage failure")
	}

	calls := 0
	fp := &fakePool{failNew: func() error {
		calls++
		if calls == 2 {
			return errBoom
		}
		return nil
	}}
	if _, err := NewBTree(fp); err == nil {
		t.Error("NewBTree did not surface the root-leaf NewPage failure")
	}
}

// Opening a non-empty file that is not an nbtree index (bad magic) fails, as
// does a meta read error on open.
func TestOpenErrors(t *testing.T) {
	if _, err := NewBTree(&fakePool{pages: []*page.Page{page.NewPage()}}); err == nil {
		t.Error("NewBTree accepted a file with a bad magic number")
	}

	fp := &fakePool{
		pages:     []*page.Page{page.NewPage()},
		failFetch: func(disk.PageID) error { return errBoom },
	}
	if _, err := NewBTree(fp); err == nil {
		t.Error("NewBTree did not surface the meta read error on open")
	}
}

// A meta-page read failure surfaces from Insert (root), Search and Scan.
func TestMetaFetchErrorPropagates(t *testing.T) {
	tr, fp := newFakeTree(t)
	fp.failFetch = func(disk.PageID) error { return errBoom }

	if err := tr.Insert(1, tid(1)); err == nil {
		t.Error("Insert did not surface the meta read error")
	}
	if _, _, err := tr.Search(1); err == nil {
		t.Error("Search did not surface the meta read error")
	}
	if err := tr.Scan(0, 10, func(int64, heap.TID) error { return nil }); err == nil {
		t.Error("Scan did not surface the meta read error")
	}
}

// A node fetch failure below the meta page surfaces from a descent: Insert
// (recursively), Search and Scan's findLeaf.
func TestNodeFetchErrorPropagates(t *testing.T) {
	tr, fp := newFakeTree(t) // caps already shrunk
	// Build a multi-level tree, then fail every non-meta fetch.
	for k := 0; k < 30; k++ {
		if err := tr.Insert(int64(k), tid(k)); err != nil {
			t.Fatal(err)
		}
	}
	fp.failFetch = func(pid disk.PageID) error {
		if pid != metaPID {
			return errBoom
		}
		return nil
	}

	if _, _, err := tr.Search(5); err == nil {
		t.Error("Search did not surface a node fetch error")
	}
	if err := tr.Scan(0, 10, func(int64, heap.TID) error { return nil }); err == nil {
		t.Error("Scan did not surface a node fetch error (findLeaf)")
	}
	if err := tr.Insert(1000, tid(1)); err == nil {
		t.Error("Insert did not surface a node fetch error")
	}
}

// A NewPage failure surfaces from each split site: a leaf split, an internal
// split, and the new-root allocation. A meta update failure surfaces too.
func TestSplitAllocErrors(t *testing.T) {
	t.Run("leaf split", func(t *testing.T) {
		tr, fp := newFakeTree(t)
		tr.Insert(0, tid(0))
		tr.Insert(1, tid(1))
		tr.Insert(2, tid(2))
		fp.failNew = func() error { return errBoom }
		if err := tr.Insert(3, tid(3)); err == nil { // fills the leaf -> split
			t.Error("Insert did not surface the leaf-split NewPage failure")
		}
	})

	t.Run("new root", func(t *testing.T) {
		tr, fp := newFakeTree(t)
		tr.Insert(0, tid(0))
		tr.Insert(1, tid(1))
		tr.Insert(2, tid(2))
		n := 0
		fp.failNew = func() error { // 1st alloc (right leaf) ok, 2nd (new root) fails
			n++
			return boomOn(n, 2)
		}
		if err := tr.Insert(3, tid(3)); err == nil {
			t.Error("Insert did not surface the new-root NewPage failure")
		}
	})

	t.Run("meta update", func(t *testing.T) {
		tr, fp := newFakeTree(t)
		tr.Insert(0, tid(0))
		tr.Insert(1, tid(1))
		tr.Insert(2, tid(2))
		c := 0
		fp.failFetch = func(pid disk.PageID) error { // 1st meta read ok, 2nd (setRoot) fails
			if pid == metaPID {
				c++
				return boomOn(c, 2)
			}
			return nil
		}
		if err := tr.Insert(3, tid(3)); err == nil {
			t.Error("Insert did not surface the meta-update failure")
		}
	})

	t.Run("internal split", func(t *testing.T) {
		tr, fp := newFakeTree(t)
		// Ascending inserts up to just before the root (internal) fills.
		for k := 0; k < 9; k++ {
			if err := tr.Insert(int64(k), tid(k)); err != nil {
				t.Fatal(err)
			}
		}
		n := 0
		fp.failNew = func() error { // 1st alloc (leaf split) ok, 2nd (internal split) fails
			n++
			return boomOn(n, 2)
		}
		if err := tr.Insert(9, tid(9)); err == nil {
			t.Error("Insert did not surface the internal-split NewPage failure")
		}
	})
}

// boomOn returns errBoom when count == target, else nil.
func boomOn(count, target int) error {
	if count == target {
		return errBoom
	}
	return nil
}

// newFakeTree builds an empty tree over a fakePool with shrunk capacities.
func newFakeTree(t *testing.T) (*BTree, *fakePool) {
	t.Helper()
	smallCaps(t, 4, 4)
	fp := &fakePool{}
	tr, err := NewBTree(fp)
	if err != nil {
		t.Fatalf("new btree: %v", err)
	}
	return tr, fp
}

// Reopening a non-empty, valid index succeeds and sees the existing data.
func TestReopenExisting(t *testing.T) {
	fp := &fakePool{}
	tr, err := NewBTree(fp) // bootstrap
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Insert(1, tid(1)); err != nil {
		t.Fatal(err)
	}

	tr2, err := NewBTree(fp) // reopen the same, valid file
	if err != nil {
		t.Fatalf("reopen valid index: %v", err)
	}
	if got, found, _ := tr2.Search(1); !found || got != tid(1) {
		t.Errorf("reopened tree = (%+v, found %v), want key 1 present", got, found)
	}
}

// build0to7 makes a fixed 2-level tree (root [2,4,6] over four leaves) so tests
// can target a specific fetch in a descent or a leaf-chain walk.
func build0to7(t *testing.T) (*BTree, *fakePool) {
	t.Helper()
	tr, fp := newFakeTree(t) // caps 4/4
	for k := 0; k < 8; k++ {
		if err := tr.Insert(int64(k), tid(k)); err != nil {
			t.Fatal(err)
		}
	}
	return tr, fp
}

// failNthNonMeta fails the count-th fetch of a non-meta page, letting meta reads
// through. It targets one fetch deep inside a descent or scan.
func failNthNonMeta(fp *fakePool, n int) {
	c := 0
	fp.failFetch = func(pid disk.PageID) error {
		if pid == metaPID {
			return nil
		}
		c++
		return boomOn(c, n)
	}
}

// Insert surfaces a fetch error from a child of the root (the recursive descent),
// not just from the root fetch.
func TestInsertRecursiveFetchError(t *testing.T) {
	tr, fp := build0to7(t)
	// Insert(100) descends root (non-meta #1) then leaf pid5 (non-meta #2).
	failNthNonMeta(fp, 2)
	if err := tr.Insert(100, tid(100)); err == nil {
		t.Error("Insert did not surface a fetch error from the recursive descent")
	}
}

// Scan surfaces a fetch error on a later leaf in the chain, after findLeaf and
// the first leaf both succeeded.
func TestScanMidChainFetchError(t *testing.T) {
	tr, fp := build0to7(t)
	// findLeaf reaches leaf pid1 (non-meta #1 root, #2 leaf); the scan loop
	// re-fetches pid1 (#3) then follows next_leaf to pid2 (#4) — fail there.
	failNthNonMeta(fp, 4)
	err := tr.Scan(0, 100, func(int64, heap.TID) error { return nil })
	if err == nil {
		t.Error("Scan did not surface a fetch error on a later leaf")
	}
}
