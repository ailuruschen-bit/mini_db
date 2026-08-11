# B+Tree Index Design

> Language: **English** | [日本語](BTree-Index-Design.ja.md)

The B+Tree access method (`internal/access/nbtree`) is a **secondary index**: it maps an `int64` key to the `heap.TID` of the row that holds that key, so a query can find rows by value without scanning the whole heap. It is a disk-oriented B+Tree — each node is one page, nodes have high fan-out, and the tree stays shallow (3–4 levels for any realistic table).

```
        executor / SQL (future)
              │  Insert(key,tid) / Search(key) / Scan(lo,hi)
              ▼
        BTree  (this module)   ── turns pages into a sorted key → TID map
              │  FetchPage / NewPage / UnpinPage / NumPages
              ▼
        BufferPool → DiskManager   (its own index file)
              │
        heap.TID ─────────────────► Heap  (resolve TID → row bytes)
```

---

## 1. Purpose — why an index

The storage engine so far can only read a row by a **known** `heap.TID`. A real query filters by value (`WHERE age = 42`) and has no TID in hand; without an index the only option is a **full heap scan** — read every page, decode every row, compare — which is `O(N)`.

A B+Tree index precomputes a **sorted** map from key value to TID. Descending it is `O(log N)` in the number of keys, and because the fan-out is in the hundreds, that logarithm is 3–4 page reads even for hundreds of millions of rows. The index answers "which TIDs have this key / keys in this range?"; the heap then resolves each TID to the actual row.

---

## 2. File storage strategy

- **One index file per index.** An index is built over one column of a heap table; a table may carry several indexes. Which index belongs to which table/column is the job of the **system catalog** (deferred). In v1 the index file is bound to its own `DiskManager` and its own `BufferPool` instance — a separate memory budget from the heap's pool, because a v1 pool caches a single file (see debt: multi-file pool).
- **The index file is a pile of pages**, same as a heap file, but the page layout is a **B+Tree node**, not a slotted page. The buffer pool serves raw `*page.Page` frames and imposes no layout; `nbtree` overlays a **node view** on `p.Bytes()`, exactly as `heap` overlays `AsSlottedPage` — this is why the frame type is layout-agnostic.

```
index file
┌──────────┬──────────┬──────────┬──────────┬─────
│ page 0   │ page 1   │ page 2   │ page 3   │ ...
│ meta     │ node     │ node     │ node     │
└──────────┴──────────┴──────────┴──────────┴─────
   ▲ root_pid ──────────► the current root node (moves when the root splits)
```

- **Page 0 is a meta page**, not a node: it is the fixed anchor that records which page is the current root. The root's page id changes whenever the root splits, so it cannot itself be a fixed location — the meta page is.
- **Pages 1+ are nodes.** Each node is either an **internal** node (routes searches downward) or a **leaf** node (holds the actual key → TID entries). Data lives **only in leaves**; internal nodes hold separator keys only.

---

## 3. Layout constants

| Constant         | Value | Meaning                                        |
| :--------------- | :---- | :--------------------------------------------- |
| `PageSize`       | 8192  | Page size in bytes (from `internal/storage/page`) |
| `NodeHeaderSize` | 8     | Node header size in bytes                      |
| `MetaMagic`      | —     | Magic number identifying an index file         |
| `KeySize`         | 8    | `int64` key, big-endian                        |
| `ChildSize`       | 4    | child pointer = `disk.PageID` (`uint32`)       |
| `TIDSize`         | 6    | leaf value = `heap.TID` = `PageID(4) + Slot(2)` |
| `MaxInternalKeys` | 681  | Fixed key capacity of an internal node          |
| `MaxLeafKeys`     | 584  | Fixed key capacity of a leaf node               |
| `ChildAreaOff`    | 5456 | Fixed start offset of the children array (`8 + MaxInternalKeys·8`) |
| `TIDAreaOff`      | 4680 | Fixed start offset of the TID array (`8 + MaxLeafKeys·8`) |

All multi-byte fields are **big-endian**.

---

## 4. Meta page (page 0)

The meta page is one page whose only live content is the root pointer. It is read at the start of every operation (it stays hot in the pool) and rewritten only when the root changes.

| Offset | Length | Field      | Description                                        |
| :----- | :----- | :--------- | :------------------------------------------------- |
| 0      | 4      | `magic`    | Identifies the file as an nbtree index.            |
| 4      | 4      | `root_pid` | Page id of the current root node.                  |
| 8      | …      | —          | Reserved (future: free-page list, height, key type). |

---

## 5. Node page

### 5.1 Node header (8 bytes)

Shared by both node kinds.

| Offset | Length | Field       | Description                                                   |
| :----- | :----- | :---------- | :----------------------------------------------------------- |
| 0      | 1      | `node_type` | `1` = leaf, `0` = internal.                                  |
| 1      | 1      | —           | Reserved (future: level / flags).                           |
| 2      | 2      | `key_count` | Number of keys `N` in this node.                            |
| 4      | 4      | `next_leaf` | Leaf only: page id of the right-sibling leaf (`Invalid` on the last leaf and on internal nodes). |

### 5.2 Internal node

An internal node stores up to `MaxInternalKeys` (681) keys and one more child pointer than keys. The two arrays occupy **fixed reserved regions**: the children array starts at a **constant** offset (`ChildAreaOff = 5456`), not immediately after the variable-length keys. `key_count = N` records how many of each are live; the region tails past `N` are reserved slack.

```
0        8                         5456 (ChildAreaOff)             8184
+--------+--------------------------+-------------------------------+--+
| header | keys[0..N-1]  (+ slack)  | children[0..N]  (+ slack)     |  |
|  8 B   | region 681 × 8 B         | region 682 × 4 B              |  |
+--------+--------------------------+-------------------------------+--+
         key i @ 8 + i·8            child i @ 5456 + i·4
```

Because the children base is constant, inserting a key shifts only the keys tail — it never displaces the children array (see 5.4). The node is **full** when `key_count == MaxInternalKeys`.

Separator semantics — child `c[i]` covers exactly one key band:

```
        keys:      k0        k1        k2
     children:  c0     c1        c2        c3
     range:    <k0   [k0,k1)  [k1,k2)    ≥k2
```

`c[0]` holds keys `< k0`; interior `c[i]` holds keys in `[k[i-1], k[i])`; `c[N]` holds keys `≥ k[N-1]`. This is why there is one more child than keys.

### 5.3 Leaf node

A leaf stores up to `MaxLeafKeys` (584) keys and an equal number of TIDs (a 1:1 pairing), in fixed reserved regions like the internal node (TID array at the constant `TIDAreaOff = 4680`), plus the `next_leaf` pointer in the header that chains leaves left-to-right for range scans:

```
0        8                         4680 (TIDAreaOff)              8184
+--------+--------------------------+-------------------------------+--+
| header | keys[0..N-1]  (+ slack)  | tids[0..N-1]  (+ slack)       |  |    header.next_leaf ─► next leaf
|  8 B   | region 584 × 8 B         | region 584 × 6 B              |  |
+--------+--------------------------+-------------------------------+--+
         key i @ 8 + i·8            tid i @ 4680 + i·6
```

Full when `key_count == MaxLeafKeys`. Each `tids[i]` is the heap address of the row whose indexed column equals `keys[i]`. Resolving a search means: find the key in a leaf, read the parallel TID, then `heap.Get(tid)`.

### 5.4 Why there is no slot directory

A slotted page needs a slot **directory** because tuples are variable-length: the directory is the indirection that locates each tuple's bytes and lets a tuple move within the page without breaking references. A B+Tree node in v1 has **fixed-size** entries — `int64` keys, `PageID` children, `heap.TID` values — so a node reduces to a header plus two sorted arrays. Locating element `i` is pure arithmetic (`base + i · size`); binary search touches only the keys array. No directory, no indirection.

The two arrays are stored **separated** (all keys, then all pointers) rather than interleaved (`key, ptr, key, ptr, …`) because binary search scans only keys — packing them contiguously is better for cache — and because the array layout absorbs the "N keys, N+1 children" asymmetry cleanly: the pointer array is simply one element longer.

### 5.5 Fixed reserved regions (why the second array's offset is constant)

The pointer/TID array begins at a **constant** offset, not immediately after the keys. If instead it started right after the keys (at `8 + 8·key_count`), that base would move by 8 bytes on every key insertion — so inserting one key would displace the **entire** second array, adding an extra `i·(pointer size)` bytes of movement (worst at the tail, i.e. exactly the common append/sequential-insert case). With a constant base the two arrays are **decoupled**: a key insert shifts only the keys tail, a value insert only the value tail.

This costs no extra space. Both layouts leave the same total free bytes in a non-full node; the fixed layout merely splits that free space into two tails (after the keys, after the values) instead of one gap at the page end — and keys/values here always grow together (`N` and `N`/`N+1`), so the "shared elastic budget" a packed layout would offer is never useful. A sorted-array node still pays an `O(N)` tail shift per insert (inherent to keeping entries sorted); the fixed regions remove only the extra whole-array displacement, a constant-factor win.

(Variable-length / `Text` keys would reintroduce a directory-like indirection; deferred — see debt.)

### 5.6 Fan-out and height

The fixed capacities come from the 8192-byte budget:

- **Internal:** `8 + 8N + 4(N+1) ≤ 8192` → `N ≤ 681` (`MaxInternalKeys`), so up to **682 children**.
- **Leaf:** `8 + 8N + 6N ≤ 8192` → `N ≤ 584` (`MaxLeafKeys`), so up to **584 entries**.

With this fan-out the tree is extremely shallow:

| Levels | Capacity (order of magnitude) |
| :----- | :---------------------------- |
| 1 (leaf only) | ~584 keys            |
| 2 | ~682 × 584 ≈ **400 thousand**    |
| 3 | ~682² × 584 ≈ **270 million**    |
| 4 | ~682³ × 584 ≈ **185 billion**    |

A point lookup therefore costs 3–4 page reads for any realistic table.

---

## 6. Operations (v1)

| Op | Behavior |
| :-- | :-- |
| `Insert(key, tid) → error` | Insert a key → TID entry; split nodes as needed. `ErrDuplicateKey` on a repeat (unique keys, v1). |
| `Search(key) → (TID, bool, error)` | Point lookup; `bool` reports whether the key exists. |
| `Scan(lo, hi, fn) → error` | Visit every entry with `lo ≤ key ≤ hi` in key order, calling `fn(key, tid)`. |

### 6.1 Search (point)

Start at the root (from `meta.root_pid`). At each internal node, binary-search the keys for `i = upper_bound(keys, K)` (the count of keys `≤ K`), read `children[i]`, and `FetchPage` the child. At the leaf, binary-search for `K`: found → return its TID; absent → not found. Each level is one binary search plus one array shift to the child pointer.

### 6.2 Range scan

Descend as in point search to the leaf that would contain `lo`. From the first entry `≥ lo`, walk the leaf's arrays, calling `fn`; at the end of a leaf, follow `next_leaf` to the next leaf. Stop when a key exceeds `hi`. The leaf chain is what lets a range scan avoid returning to the upper levels. Like `heap.Scan`, `fn` returns an error to stop early, and any I/O error propagates out.

### 6.3 Insert and split

Descend to the target leaf (as in search) and insert the `(key, TID)` in sorted position, shifting the tail of the arrays by one. If the node still fits, done. If it overflows, it **splits**, and the split may cascade upward. The driver is recursive: an insert returns an optional split result `{sepKey, rightPID}` that the parent absorbs.

**Leaf split — copy-up.** A full leaf is divided in two; the first key of the new right leaf is **copied** up into the parent as the separator. The key itself stays in the leaf, because leaves hold the real data and nothing may be lost. The `next_leaf` chain is repaired: `right.next = left.next; left.next = right`.

**Internal split — push-up.** A full internal node is divided in two; the **middle** key is **moved** up into the parent — it is *not* kept in either half, because an internal key is only a separator, and the promoted middle key becomes the exact separator between the two halves. The child pointers split with the keys.

**Root split — grow at the top.** When a split reaches the root and the root itself splits, a **new root** is created (one separator, two children) and `meta.root_pid` is updated. This is the *only* way the tree gets taller: it grows from the top, pushing every leaf down one level together.

**Balance invariant.** Because growth happens only at the root and all leaves move down in lockstep, **every leaf is always at the same depth**, independent of insertion order. A B+Tree cannot degenerate into a tall/unbalanced tree the way an unbalanced BST can. In an insert-only workload, a split leaves each half at least ~half full, so nodes never approach empty; the one node exempt from the half-full rule is the root (a fresh root has a single key by construction).

---

## 7. Dependencies

**The `pager` interface.** Like the heap, the B+Tree depends on a small consumer-side interface, not the concrete `*buffer.BufferPool`:

```go
type pager interface {
    FetchPage(pid disk.PageID) (*page.Page, error)
    NewPage() (*page.Page, disk.PageID, error)
    UnpinPage(pid disk.PageID, dirty bool) error
    NumPages() disk.PageID
}
```

The pool serves raw frames; `nbtree` overlays its node view. A descent pins one page per level and holds parents pinned until a child's split is absorbed, so the **index pool must have more frames than the tree is tall**.

**`heap.TID`.** A leaf value is a heap row address, so it is typed as `heap.TID` (v1 imports it directly; the dependency `nbtree → heap` is acyclic). Extracting TID into a neutral shared package is deferred (see debt).

---

## 8. Deferred (with their future home)

- **Delete** — needs the underflow half of balancing: **merge** or **redistribute** with a sibling when a node drops below half full. Like heap delete, it also waits on MVCC. Absent it, deletes would waste space but still never make the tree taller.
- **Duplicate keys** — v1 requires unique keys; a non-unique index (many rows per key value) needs duplicate-key handling in split/search.
- **Variable-length / `Text` keys** — reintroduce a directory-like indirection inside the node; v1 keys are fixed `int64`.
- **Multi-file shared pool** — v1 gives the index its own pool over its own file; one pool serving heap + index files needs the composite `{fileID, pid}` key (buffer-pool debt).
- **Neutral home for `TID`** — currently `heap.TID`; a shared physical-row-address type would decouple the two access methods.
- **Doubly-linked leaves / bulk load** — v1 chains leaves forward only; sequential bulk insertion leaves nodes ~half full (space, not height).

---

## 9. References

- [PostgreSQL Documentation: B-Tree Indexes](https://www.postgresql.org/docs/current/btree.html)
- [PostgreSQL `nbtree` README](https://github.com/postgres/postgres/blob/master/src/backend/access/nbtree/README)
