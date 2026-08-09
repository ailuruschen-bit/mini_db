# Record Module Test Plan

> Language: **English** | [日本語](Record-Test-Plan.ja.md)

Test plan for `internal/record` — encoding typed rows to and from tuple bytes. The *how* is in [Test Conventions](../Test-Conventions.md); this lists *what*.

---

## 1. Encode / Decode (`record_test.go`, black-box `record_test`)

- [x] **Round-trip:** values and types survive Encode → Decode, including empty text (not NULL) and the `int64` boundary.
- [x] **NULL round-trip:** a `nil` in a nullable column round-trips, and the tuple's `HasNull` flag is set.
- [x] **No-NULL omits the bitmap:** with no NULLs, `t_hoff` equals the header size and `HasNull` is clear (also checks header interop with the `page` layer: `col_count`, `t_hoff`).
- [x] **Encode errors:** arity mismatch (too few / too many values); `nil` in a non-nullable column; wrong Go type for an int / text column; text longer than a `uint16`.
- [x] **Unknown type:** rejected by both Encode and Decode.
- [x] **Too many columns:** a 1024-column schema (> the 10-bit `col_count` limit) is rejected.
- [x] **Decode errors:** shorter than the header; `col_count` mismatch with the schema; inconsistent `t_hoff`; truncated before the bitmap; truncated int; truncated text length; truncated text body.

## 2. Full-stack integration (`integration_test.go`, black-box)

- [x] A typed row is Encoded, stored via `heap.Insert`, read back by its TID, and Decoded to the same row — across multiple rows including one with a NULL. Proves the record ↔ heap ↔ buffer ↔ disk stack round-trips.

---

## Execution order

1. Round-trip (with and without NULL), header interop.
2. Encode errors, Decode errors (table-driven).
3. Boundary: too many columns.
4. Full-stack integration.
5. Wrap up: `make check` (vet + lint + race + coverage), commit as `feat(record): ...`.
