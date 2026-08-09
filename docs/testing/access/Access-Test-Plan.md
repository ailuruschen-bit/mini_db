# Access Module Test Plan

> Language: **English** | [日本語](Access-Test-Plan.ja.md)

Test plan for the `internal/access` module — access methods that turn pages into tuples, on top of the buffer pool. Currently the `heap` package (a heap-file access method); a B-tree index method will join it later. The *how* (framework, `-race`, coverage) is in [Test Conventions](../Test-Conventions.md); this lists *what*, per component.

---

## 0. Shared helpers (heap)

- `newHeap(poolSize)` — a heap over a real buffer pool and disk file in a temp dir.
- `payload(seed, size)` — a distinct, verifiable tuple of a given size.
- `initPage()` — a formatted empty page, for building fake states.
- `fakePool` — a `pager` (the consumer-side interface the heap depends on) that fails `FetchPage`/`NewPage` on demand, so the heap's I/O error paths can be driven deterministically.

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

## Execution order

1. Scaffold: test file, helpers, `fakePool`.
2. Insert/Get round-trip, then multi-page spanning.
3. Scan — full visit, then early-stop.
4. Error paths — real (out-of-range, too large, reopen) and fake-injected (fetch/new/unexpected).
5. Wrap up: `make check` (vet + lint + race + coverage), commit as `feat(heap): ...`.
