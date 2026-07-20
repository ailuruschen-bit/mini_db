# Storage Module Test Plan

> Language: **English** | [日本語](Storage-Test-Plan.ja.md)

Test plan for the `internal/storage` package. The *how* (framework, table-driven, golden assertions, the four must-cover items) is defined in [Test Conventions](../Test-Conventions.md); this document lists *what* to cover, per component.

**Out of scope:** disk file I/O (not implemented yet). The "external bytes → memory" path is covered as round-trip fidelity via `NewSlottedPage` + getters.

---

## 0. Shared helpers

- [ ] `blankPage()` — a zeroed `*SlottedPage`.
- [ ] `knownPage()` — a page pre-populated with known header / slot / tuple bytes.

---

## 1. PageHeader (`page_header_test.go`, white-box)

Fields (big-endian): `pd_lsn[0:8]` `pd_checksum[8:10]` `pd_flags[10:12]` `pd_lower[12:14]` `pd_upper[14:16]` `pd_special[16:18]` `pd_pagesize[18:20]` `pd_prune_xid[20:24]`.

- [ ] Round-trip: set then get returns the same value, every field.
- [ ] Golden offset / endianness: after a set, the raw bytes at the field's range hold the big-endian encoding.
- [ ] Field isolation: writing one field leaves adjacent fields unchanged.
- [ ] Boundaries: `0` and the type maximum (u16 / u32 / u64).

## 2. SlotEntry (`slot_entry_test.go`, white-box) — highest risk (4-byte bit packing)

Layout: `Offset` (bits 31–17), `Length` (bits 16–2), `Flags` (bits 1–0).

- [ ] Round-trip of `Offset` / `Length` / `Flag`.
- [ ] **Bit-field independence:** set one field to all-ones, set another, verify the first is unchanged (all pairings).
- [ ] Boundaries: `0`; max (`Offset`/`Length` = 32767, `Flag` = 3).
- [ ] Error path: `SetOffset`/`SetLength` (> 32767) and `SetFlag` (> 3) return an error and do not write.
- [ ] Golden: known offset/length/flag → expected 4 raw bytes.

## 3. TupleHeader (`tuple_test.go`, white-box) — 12-byte layout

Layout: `t_xmin[0:4]` `t_xmax[4:8]` `flags`(6b)+`col_count`(10b) `[8:10]` `t_hoff[10]` reserved `[11]`.

- [ ] `TupleHeader()` returns a view over the first 12 bytes.
- [ ] Round-trip + golden offsets for `TxMin` / `TxMax`.
- [ ] `Flags` / `ColumnCount` packed independence (setting one preserves the other), boundaries (flags = 63, col_count = 1023), error path (> max).
- [ ] `Hoff` round-trip; reserved byte `[11]` stays untouched by all setters.
- [ ] `HasNull` reflects the `FlagHasNull` bit.

## 4. SlottedPage assembly (`slotted_page_test.go`)

- [ ] **`NewSlottedPage` value semantics:** mutating the input array after construction does not affect the page.
- [ ] **Zero-copy alias invariant:** a write through `Header()` / a `SlotEntry` is visible via a freshly obtained view and in the raw `data`.
- [ ] **SlotDirectory:** N entries when `pd_upper` implies N; `0` when `pd_upper == HeaderSize` (empty page); a setter on a returned entry writes back to the page.
- [ ] **LocateTupleByEntry:** returned slice covers exactly `[offset, offset+length)`.

## 5. Round-trip fidelity (`storage_roundtrip_test.go`, black-box `storage_test`)

- [ ] Assemble a page through the public API (header + slot + tuple) → take `data` → reconstruct via `NewSlottedPage` → every getter reads back the same value.

---

## Execution order

1. Scaffold: test files + shared helpers; confirm an empty suite compiles.
2. PageHeader — establish the table-driven + golden pattern.
3. SlotEntry — bit packing, independence, error paths.
4. TupleHeader — 12-byte packed layout.
5. SlottedPage assembly — value semantics, alias, SlotDirectory, LocateTupleByEntry.
6. Round-trip (black-box).
7. Wrap up: `go test -cover`, `go vet`, commit as `test(storage): ...`.
