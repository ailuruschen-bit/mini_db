# Heap Access Method Design

> Language: **English** | [日本語](Heap-Design.ja.md)

The heap access method (`internal/access/heap`) stores a table's rows as a **heap file** — an *unordered* pile of tuples spread across pages — on top of the buffer pool. "Heap" names the unordered organization (as in PostgreSQL's heap access method), not memory nor the heap data structure.

```
        executor / SQL (future)
              │  Insert / Get / Scan
              ▼
        Heap  (this module)   ── turns pages into tuples
              │  FetchPage / NewPage / UnpinPage / NumPages
              ▼
        BufferPool → DiskManager
```

---

## 1. Tuple identity — TID

A tuple's physical address is a **TID** (tuple id):

```
TID = { Page: disk.PageID, Slot: uint16 }
```

`Page` is the page number in the file; `Slot` is the slot-directory index within that page. Because a slot is an indirection (the slot number is stable even if the tuple moves inside the page during future compaction), a TID is a **stable handle**: it survives eviction/reload and a close/reopen of the file, and a future index can point at rows by TID.

---

## 2. Opaqueness

Tuple bytes are **opaque** to the heap. Assembling a tuple (its `TupleHeader`, null bitmap, column data) and interpreting it (types, MVCC visibility) belong to the layers above — the record/encoding layer and, later, the transaction layer. The heap only places bytes into pages and hands them back.

---

## 3. Operations (v1)

| Op | Behavior |
| :-- | :-- |
| `Insert(data) → TID` | Append the tuple to a page and return its TID. |
| `Get(TID) → []byte` | Return a **copy** of the tuple's bytes. |
| `Scan(fn) → error` | Visit every tuple in physical order, calling `fn(TID, data)` for each. |

**Insert — page selection.** It tries the **last** page first; if the tuple does not fit (`page.ErrNoSpace`), it grows the file by one page (`NewPage`) and inserts there. This v1 strategy does **not** search earlier pages for space — there is no free-space map yet, so space freed in earlier pages is not reused (see debt).

**Get — copy out.** The tuple bytes are copied before the page is unpinned, because the frame may be evicted and reused for another page afterwards. The returned slice is the caller's to keep.

**Scan — callback form.** `Scan(fn func(TID, []byte) error) error` drives the loop internally (page by page, slot by slot) and calls `fn` per tuple. The callback returns `nil` to continue or an error to stop the scan early; that error, and any I/O error, is returned from `Scan`. A callback form is used over a range-style iterator (`iter.Seq2`) because scanning does fallible disk I/O, and `iter.Seq2` has nowhere to report such an error. The `data` passed to `fn` is a **view** valid only during the call — copy it to keep it.

---

## 4. Dependency — the `pager` interface

The heap depends on a small consumer-side interface, not the concrete `*buffer.BufferPool`:

```go
type pager interface {
    FetchPage(pid disk.PageID) (*page.Page, error)
    NewPage() (*page.Page, disk.PageID, error)
    UnpinPage(pid disk.PageID, dirty bool) error
    NumPages() disk.PageID
}
```

Named for the capability (something that serves pages), not an implementation. It keeps the coupling small, lets tests substitute a fake pool that fails on demand, and leaves room for a different page source later. `*buffer.BufferPool` satisfies it.

The pool serves **raw frames** (`*page.Page`): it owns the 8 KB bytes but imposes no layout. The heap overlays its own interpretation with `page.AsSlottedPage(raw)` — a zero-copy view — so the buffer pool can back files of different page kinds (a heap file here, a B+Tree index file later). `NewPage` returns a zeroed, unformatted frame; the heap formats it with `Init` and, because that format then lives only in memory, unpins it dirty so it reaches disk — even when the tuple is too large to fit, so the file never holds an unformatted page that a later `Scan` would misread.

---

## 5. Deferred (with their future home)

- **Update / Delete** — belong to the heap but need the MVCC/transaction layer. An update never overwrites a live tuple in place: it appends a **new version** and marks the old one dead by setting its `t_xmax`. Delete just sets `t_xmax`. Hence tuple data is effectively append-only; the `t_xmin`/`t_xmax` fields already in `TupleHeader` are the machinery for this.
- **VACUUM** — a separate maintenance operation that physically reclaims the space of dead tuple versions no longer visible to any transaction (in-page compaction + dead-slot reuse).
- **Free-space map** — smarter page selection than "last page, else new".
- **Concurrency** — `Insert` mutates page contents outside the pool lock, so the heap is not concurrency-safe in v1 (waits on the buffer pool's per-page latch).
- **Large tuples** — a tuple larger than a page is rejected; overflow/TOAST storage is future work.
