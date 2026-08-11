# Access Module Test Plan

> Language: **English** | [日本語](Access-Test-Plan.ja.md)

Test plan for the `internal/access` module — access methods on top of the buffer pool: the `heap` package (a heap-file access method, turning pages into tuples) and the `nbtree` package (a B+Tree index, mapping keys to TIDs). The *how* (framework, `-race`, coverage) is in [Test Conventions](../Test-Conventions.md); this lists *what*, per component.

---

## 0. Shared helpers

**heap:**

- `newHeap(poolSize)` — a heap over a real buffer pool and disk file in a temp dir.
- `payload(seed, size)` — a distinct, verifiable tuple of a given size.
- `initPage()` — a formatted empty page, for building fake states.
- `fakePool` — a `pager` (the consumer-side interface the heap depends on) that fails `FetchPage`/`NewPage` on demand, so the heap's I/O error paths can be driven deterministically.

**nbtree:**

- `newTree(poolSize)` — a B+Tree over a real buffer pool and disk file in a temp dir (black-box, production capacities).
- `fakePool` — an in-memory `pager` that never evicts (a whole tree stays resident), with injectable `failFetch`/`failNew` for the I/O error paths (white-box).
- `smallCaps(leaf, internal)` — shrinks the node capacities for a test so a handful of keys forces leaf splits, internal splits and root growth.
- `newLeaf()` / `newInternal()` — a formatted empty node over a blank frame; `tid(seed)` — a recognisable TID.

---

## 1. Heap (`heap_test.go`, black-box `heap_test`)

`TID = {Page, Slot}` is a tuple's physical address. v1 operations: Insert / Get / Scan (delete and update wait for the MVCC/transaction layer). Tuple bytes are opaque to the heap.

- [x] **Round-trip:** Insert then Get returns the same bytes.
- [x] **Spans pages:** enough large tuples spill onto new pages, and every one reads back by its id.
- [x] **Scan:** visits every tuple exactly once, across pages and through eviction (pool smaller than the page count).
- [x] **Scan early-stop:** a callback error aborts the scan and propagates out.
- [x] **Get bounds:** an out-of-range slot is rejected; a non-existent page is rejected.
- [x] **Insert too large:** a tuple that cannot fit an empty page is rejected, not dropped.
- [x] **Persistence:** tuple ids stay valid across close/reopen (Get by an old TID after reopening the file).
- [x] **Error propagation (`fakePool`):** Insert surfaces a `FetchPage` failure and a `NewPage` failure; Insert surfaces an unexpected (non-`ErrNoSpace`) page error rather than treating it as "page full"; Scan surfaces a `FetchPage` failure.

---

## 2. B+Tree index (`nbtree`)

Key → `heap.TID` map, one node per page over an index file (page 0 = meta, pages 1+ = nodes). v1: unique `int64` keys; Search / Insert(+split) / range Scan; delete deferred. See [B+Tree Index Design](../../design/access/BTree-Index-Design.md).

### 2.1 Node byte layout (`nbtree_internal_test.go`, white-box)

- [x] **Capacities fit the page:** the fixed-offset arithmetic keeps every field of a capacity-filled internal/leaf node inside 8192 bytes; the second array's offset equals the header-plus-keys arithmetic.
- [x] **Init:** `initLeaf`/`initInternal` set the node type, start empty, and zero any bytes a previous occupant left behind.
- [x] **Field round-trips:** keys (negatives included — stored raw, compared as decoded `int64`), child pointers, TIDs (page + slot), and `next_leaf`.
- [x] **search:** lower-bound index and found flag — empty node, exact hit, miss, and both boundaries.
- [x] **childIndex:** descends into the band containing the key; a key equal to a separator descends right.
- [x] **full:** reports capacity for both node kinds (and not one short).
- [x] **insertLeafAt / insertInternalAt:** sorted shifts keep keys and their parallel TIDs/children aligned; a separator's right child lands at `i+1` with `child[i]` left intact; a leaf fills to capacity without overflowing the page.
- [x] **splitLeaf:** divides entries evenly; the separator is the right leaf's first key (copy-up — it stays in the leaf).
- [x] **splitInternal:** pushes the middle key up (removed from both halves) and splits the children with the keys.

### 2.2 Tree operations — white-box (`fakePool` + `smallCaps`)

- [x] **Insert/Search/Scan round-trip:** a scrambled batch round-trips; a full scan returns every key in ascending order. Tiny capacities force many leaf splits, internal splits and several levels of root growth.
- [x] **Duplicate rejected:** a repeat key returns `ErrDuplicateKey` and does not overwrite the first value.
- [x] **Range scan:** honours `[lo, hi]`, crosses leaf boundaries via `next_leaf`; empty range and single-point range handled.
- [x] **Scan early-stop:** a callback error aborts the scan and propagates.
- [x] **Empty tree:** Search finds nothing, Scan visits nothing.
- [x] **Reopen:** a non-empty valid index reopens and sees its data.

### 2.3 Error paths — white-box (`fakePool`)

- [x] **Bootstrap:** a `NewPage` failure surfaces for the meta page and, separately, for the root leaf.
- [x] **Open:** a bad magic number is rejected; a meta read error on open surfaces.
- [x] **Meta read:** a meta-page fetch failure surfaces from Insert, Search and Scan.
- [x] **Node fetch:** a fetch failure below the meta page surfaces from a descent (recursive Insert), a Search, and a scan's later leaf in the chain (after the first leaf succeeded).
- [x] **Split allocation:** a `NewPage` failure surfaces at each split site — a leaf split, an internal split, and the new-root allocation — and a meta-update failure surfaces after a root split.

### 2.4 Integration — black-box (`nbtree_test.go`, real pool, production capacities)

- [x] **Multi-level through eviction:** >584 keys split leaves at the real capacity and build a multi-level tree; with a pool smaller than the node count this drives eviction/reload. Every key reads back and a full scan is sorted.
- [x] **Persistence:** the index survives close/reopen — session 2 validates the meta magic, reads the persisted root, and finds every key session 1 inserted (odd keys, never inserted, are absent).

---

## Execution order

**heap:**

1. Scaffold: test file, helpers, `fakePool`.
2. Insert/Get round-trip, then multi-page spanning.
3. Scan — full visit, then early-stop.
4. Error paths — real (out-of-range, too large, reopen) and fake-injected (fetch/new/unexpected).
5. Wrap up: `make check`, commit as `feat(heap): ...`.

**nbtree:**

1. Node byte layout + node-level ops (white-box): fields, search, insert, split.
2. `fakePool` + `smallCaps`; Insert/Search/Scan with forced splits and multi-level growth.
3. Range scan, duplicate, empty tree, early-stop, reopen.
4. Error paths: bootstrap / open / meta read / node fetch / split allocation / meta update.
5. Black-box integration over a real pool: multi-level + eviction, then persistence.
6. Wrap up: `make check`, commit as `feat(nbtree): ...`.
