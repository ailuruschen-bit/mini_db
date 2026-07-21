# Storage Module Test Plan

> Language: **English** | [日本語](Storage-Test-Plan.ja.md)

Test plan for the `internal/storage` package. The *how* (framework, table-driven, golden assertions, the four must-cover items) is defined in [Test Conventions](../Test-Conventions.md); this document lists *what* to cover, per component.

**Out of scope:** disk file I/O (not implemented yet). The "external bytes → memory" path is covered as round-trip fidelity via `NewSlottedPage` + getters.

---

## 0. Shared helpers

- [x] `blankPage()` — a zeroed `*SlottedPage`.
- [x] `blankSlotEntry()` / `blankTupleHeader()` / `blankTuple(size)` — standalone zeroed views, so a component can be exercised without building a page around it.
- [x] `beBytes(v, size)` — big-endian expectation builder for golden assertions.

A pre-populated `knownPage()` helper turned out to be unnecessary: every test builds exactly the state it needs from `blankPage()`, which keeps each test readable on its own.

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

## 4. SlottedPage assembly (`slotted_page_test.go`)

- [x] **`NewSlottedPage` value semantics:** mutating the input array after construction does not affect the page.
- [x] **Zero-copy alias invariant:** a write through `Header()` / a `SlotEntry` is visible via a freshly obtained view and in the raw `data`.
- [x] **SlotCount:** N when `pd_upper` implies N; `0` when `pd_upper == HeaderSize` (empty page).
- [x] **SlotEntryAt:** slot i maps to the window at `HeaderSize+i*SlotEntrySize`; a setter on the returned entry writes back to the page; an out-of-range index panics.
- [x] **Slots:** yields every entry in order, honours early `break`, and allocates nothing (locked in with `testing.AllocsPerRun`).
- [x] **LocateTupleByEntry:** returned slice covers exactly `[offset, offset+length)`.

## 5. Round-trip fidelity (`storage_roundtrip_test.go`, black-box `storage_test`)

- [x] Assemble a page through the public API (header + slot + tuple) → take `data` → reconstruct via `NewSlottedPage` → every getter reads back the same value.

---

## Execution order

1. Scaffold: test files + shared helpers; confirm an empty suite compiles.
2. PageHeader — establish the table-driven + golden pattern.
3. SlotEntry — bit packing, independence, error paths.
4. TupleHeader — 12-byte packed layout.
5. SlottedPage assembly — value semantics, alias, slot access, LocateTupleByEntry.
6. Round-trip (black-box).
7. Wrap up: `go test -cover`, `go vet`, commit as `test(storage): ...`.
