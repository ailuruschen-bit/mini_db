# Storage Module Test Plan

> Language: **English** | [日本語](Storage-Test-Plan.ja.md)

Test plan for the `internal/storage` module — the `page`, `disk` and `buffer` packages. The *how* (framework, table-driven, golden assertions, the four must-cover items, `-race` for concurrent code) is defined in [Test Conventions](../Test-Conventions.md); this document lists *what* to cover, per component.

The module is built bottom-up: `page` (byte layout) → `disk` (pages ↔ file) → `buffer` (page cache over the disk). Each layer's tests assume the layer below is already covered.

---

## 0. Shared helpers

**page** (`helpers_test.go`):

- [x] `blankPage()` — a zeroed `*SlottedPage`.
- [x] `blankSlotEntry()` / `blankTupleHeader()` / `blankTuple(size)` — standalone zeroed views, so a component can be exercised without building a page around it.
- [x] `beBytes(v, size)` — big-endian expectation builder for golden assertions.

**disk** (`disk_test.go`): `tempDBPath`, `writeZeroPages`, `fileSize`, `filledPage(b)`, `mustOpen`.

**buffer**: `probeOffset` + `writeProbe`/`readProbe` (a recognisable body byte), `mustPool` (real disk over a temp file), `readProbeFromDisk` (read a page straight from the file, bypassing the pool), and `fakeDisk` — an in-memory `diskManager` whose `ReadPage`/`WritePage`/`AllocatePage` can be made to fail on demand, so the pool's error-recovery paths can be driven deterministically.

---

## 1. PageHeader (`page_header_test.go`, white-box)

Fields (big-endian): `pd_lsn[0:8]` `pd_checksum[8:10]` `pd_flags[10:12]` `pd_lower[12:14]` `pd_upper[14:16]` `pd_special[16:18]` `pd_pagesize[18:20]` `pd_prune_xid[20:24]`.

- [x] Round-trip: set then get returns the same value, every field.
- [x] Golden offset / endianness: after a set, the raw bytes at the field's range hold the big-endian encoding.
- [x] Field isolation: writing one field leaves adjacent fields unchanged.
- [x] Boundaries: `0` and the type maximum (u16 / u32 / u64).

## 2. SlotEntry (`slot_entry_test.go`, white-box) — highest risk (4-byte bit packing)

Layout: `Offset` (bits 31–17), `Length` (bits 16–2), `Flags` (bits 1–0).

- [x] Round-trip of `Offset` / `Length` / `Flag`.
- [x] **Bit-field independence:** set one field to all-ones, set another, verify the first is unchanged (all pairings).
- [x] Boundaries: `0`; max (`Offset`/`Length` = 32767, `Flag` = 3).
- [x] Error path: `SetOffset`/`SetLength` (> 32767) and `SetFlag` (> 3) return an error and do not write.
- [x] Golden: known offset/length/flag → expected 4 raw bytes.

## 3. TupleHeader (`tuple_test.go`, white-box) — 12-byte layout

Layout: `t_xmin[0:4]` `t_xmax[4:8]` `flags`(6b)+`col_count`(10b) `[8:10]` `t_hoff[10]` reserved `[11]`.

- [x] `TupleHeader()` returns a view over the first 12 bytes.
- [x] Round-trip + golden offsets for `TxMin` / `TxMax`.
- [x] `Flags` / `ColumnCount` packed independence (setting one preserves the other), boundaries (flags = 63, col_count = 1023), error path (> max).
- [x] `Hoff` round-trip; reserved byte `[11]` stays untouched by all setters.
- [x] `HasNull` reflects the `FlagHasNull` bit.

## 4. SlottedPage assembly (`slotted_page_test.go`, white-box)

- [x] **`NewSlottedPage` value semantics:** mutating the input array after construction does not affect the page.
- [x] **Zero-copy alias invariant:** a write through `Header()` / a `SlotEntry` is visible via a freshly obtained view and in the raw `data`.
- [x] **SlotCount:** N when `pd_upper` implies N; `0` when `pd_upper == HeaderSize` (empty page).
- [x] **SlotEntryAt:** slot i maps to the window at `HeaderSize+i*SlotEntrySize`; a setter on the returned entry writes back to the page; an out-of-range index panics.
- [x] **Slots:** yields every entry in order, honours early `break`, and allocates nothing (locked in with `testing.AllocsPerRun`).
- [x] **LocateTupleByEntry:** returned slice covers exactly `[offset, offset+length)`.
- [x] **Init:** formats a valid empty page — `pd_upper = HeaderSize`, `pd_lower = PageSize`, `SlotCount == 0` — and zeroes leftover bytes, even when the backing array was full of another page's data. (A merely zeroed page underflows `SlotCount`, so the pointer values are pinned down explicitly.)

## 5. Round-trip fidelity (`roundtrip_test.go`, black-box `page_test`)

- [x] Assemble a page through the public API (header + slot + tuple) → take `data` → reconstruct via `NewSlottedPage` → every getter reads back the same value.

---

## 6. DiskManager (`disk_test.go` black-box, `disk_internal_test.go` white-box)

Fixed-size pages moved between a heap file and caller buffers, addressed by page number. The mutex guards `numPages` only; positioned I/O is lock-free.

- [x] **Open:** creates a missing file; counts the pages of an existing file; rejects a size that is not a whole number of pages (corrupt).
- [x] **Close** releases the handle.
- [x] **AllocatePage:** first id is 0; ids are sequential; the new page is zero-filled; it continues an existing file's count; allocated pages survive a reopen; **concurrent** allocation hands out distinct ids (many goroutines, `-race`); a failed extend leaves `numPages` unchanged; it refuses to allocate past `MaxPageID` (white-box, forces the counter).
- [x] **ReadPage / WritePage:** write-then-read round-trip; writing one page does not disturb its neighbours (isolation); a written page survives a reopen; an out-of-range id is rejected; a buffer that is not exactly one page long is rejected.
- [x] **Concurrent mixed operations:** a stress capstone — many goroutines allocating, writing, reading their own pages plus pollers reading `NumPages`, under `-race` — proving the counter lock and lock-free I/O coexist safely.
- [x] **Sync** succeeds on an open file; **Sync on a closed file** surfaces the error.

## 7. LRUReplacer (`lru_replacer_test.go`, white-box)

Tracks evictable frames (pin count 0) and yields the least-recently-used one. No internal lock — always called under the pool lock.

- [x] **Empty:** a fresh replacer has no victim; size 0.
- [x] **Order:** the least-recently-unpinned frame is evicted first.
- [x] **Pin removes** a frame from the candidate set, so it is skipped as a victim.
- [x] **Repeated Unpin keeps order:** unpinning an already-tracked frame does not move it (recency is fixed when it first becomes evictable).
- [x] **Pin then Unpin is most recent:** re-entering a frame puts it at the most-recent end.
- [x] **Pin untracked** is a harmless no-op and does not affect other frames.

## 8. BufferPool (`buffer_pool_test.go` black-box `buffer_test`, `buffer_pool_internal_test.go` white-box)

A fixed set of frames caching disk pages, with pin counting, a dirty flag, LRU eviction, and flush/sync. v1 uses a single lock over all metadata and holds it across disk I/O; page *contents* are not lock-protected, so concurrency tests partition page ownership.

**Construction (white-box)**

- [x] Non-positive pool size panics.
- [x] A fresh pool has every frame in the free list; nothing resident or evictable.

**Fetch & pin**

- [x] While resident, `FetchPage` returns the same `*SlottedPage` instance.
- [x] Pin count rises per fetch, falls per unpin; a frame becomes an eviction candidate only at zero (white-box on the counter and replacer).
- [x] A miss with every frame pinned returns `ErrNoFreeFrame` — from both `FetchPage` (white-box, seeded fake disk) and `NewPage` (black-box), and freeing a pin makes room again.

**NewPage**

- [x] Returns a formatted, ready-to-use empty page (`SlotCount == 0`, `pd_upper = HeaderSize`, `pd_lower = PageSize`), not a bag of zeros.
- [x] Assigns consecutive ids (each call extends the file).
- [x] Starts dirty — the formatted header lives only in memory until flushed (white-box).

**Dirty lifecycle, eviction & persistence**

- [x] A dirty page evicted to make room is written back, so re-fetching reloads the same contents.
- [x] The dirty flag is cooperative: a change unpinned as clean is not written back and is lost on eviction.
- [x] `FlushPage` pushes a dirty page's bytes to disk without evicting; a second flush of a now-clean page is a no-op.
- [x] `dirty` is sticky: a later clean unpin does not clear a change an earlier holder reported.
- [x] **Reopen capstone:** create pages, write probes, `FlushAll`, `Sync`, close; reopen the file and read every probe back.

**Error paths (mostly white-box via `fakeDisk`)**

- [x] Unpinning a non-resident page, or a page with zero pins, errors without touching state.
- [x] Flushing a non-resident page errors; fetching a never-allocated page errors.
- [x] A failed write-back of a dirty victim restores the pool exactly (victim stays resident, dirty, evictable; no leaked frame).
- [x] A failed `AllocatePage` returns the acquired frame to the free list; the pool recovers.
- [x] A failed page load (phase B) returns the acquired frame to the free list.
- [x] A write error while flushing propagates out of `FlushPage` and `FlushAll`; the page stays dirty for a retry.

**Concurrency (`-race`)**

- [x] Many goroutines each own a disjoint set of pages and repeatedly fetch → write their own byte → unpin dirty, driving heavy eviction; the metadata lock keeps the pool consistent (final probes and balanced pins verified). The pin holds each frame while its owner writes, so page contents never race.

---

## Execution order

1. Scaffold: test files + shared helpers; confirm an empty suite compiles.
2. PageHeader — establish the table-driven + golden pattern.
3. SlotEntry — bit packing, independence, error paths.
4. TupleHeader — 12-byte packed layout.
5. SlottedPage assembly — value semantics, alias, slot access, `Init`.
6. Round-trip (black-box).
7. DiskManager — Open/Close, AllocatePage, Read/Write, concurrency (`-race`), Sync.
8. LRUReplacer — order and pin/unpin semantics.
9. BufferPool — construction, fetch/pin, NewPage, dirty/eviction/persistence, error paths via `fakeDisk`, concurrency (`-race`).
10. Wrap up: `go test -race -cover ./...`, `go vet`, commit as `test(<pkg>): ...`.
