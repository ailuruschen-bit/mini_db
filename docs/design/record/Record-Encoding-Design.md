# Record Encoding Design

> Language: **English** | [日本語](Record-Encoding-Design.ja.md)

The record layer (`internal/record`) converts **typed, schema-described rows** to and from the opaque tuple bytes the heap stores. It is the bridge between "a row of values" and "a `[]byte`".

```
        executor / SQL (future)      — rows: [42, "alice", nil]
              │  Schema.Encode(row) → []byte
              │  Schema.Decode([]byte) → row
              ▼
        record  (this module)
              ▼
        heap → buffer → disk         — opaque bytes
```

---

## 1. Tuple byte layout

The encoded tuple matches the layout in [Physical Storage Design](../storage/Physical-Storage-Design.md):

```
[ TupleHeader (12 B) ] [ null bitmap (optional) ] [ column data ]
```

- **TupleHeader** — the 12-byte header. The record layer writes `col_count`, `t_hoff` (offset where column data starts), and the `HasNull` flag; it leaves the MVCC fields `t_xmin`/`t_xmax` **zero** for the future transaction layer. The header is written and read through `page.NewTuple(buf).TupleHeader()`, so the byte layout lives only in the `page` package.
- **null bitmap** — present **only** when some column is NULL (indicated by `HasNull`). It is `ceil(col_count / 8)` bytes; **bit `i` set means column `i` is NULL**.
- **column data** — the non-NULL columns, in schema order. NULL columns occupy no bytes.

---

## 2. Column types (v1)

| Type | Go value | Encoding |
| :-- | :-- | :-- |
| `TypeInt` | `int64` | 8 bytes, big-endian |
| `TypeText` | `string` | `uint16` length prefix (big-endian) + UTF-8 bytes |
| NULL | `nil` | no bytes (marked in the bitmap) |

Big-endian matches the page header. `Bool` and other types are easy to add later.

---

## 3. Layout strategy

Columns are laid out **sequentially and self-describing**: a fixed type occupies its fixed width, a variable type carries its own length prefix, and a NULL contributes nothing. Decoding walks the columns in order from `t_hoff`. Full-row decode is O(total size); random access to column *N* is O(N) (must walk the earlier columns) — acceptable for v1.

No alignment: columns are packed tightly, read via `encoding/binary` (which handles unaligned access).

---

## 4. API

```go
type Type uint8
const ( TypeInt Type = iota; TypeText )

type Column struct { Name string; Type Type; Nullable bool }
type Schema struct { Columns []Column }
type Row []any   // int64 | string | nil

func (s Schema) Encode(row Row) ([]byte, error)
func (s Schema) Decode(data []byte) (Row, error)
```

**Encode** rejects: an arity mismatch, a `nil` in a non-nullable column, a value whose Go type does not match the column, a text longer than a `uint16`, and a schema with more columns than the 10-bit `col_count` field allows.

**Decode** rejects: bytes shorter than the header, a `col_count` that disagrees with the schema, a `t_hoff` inconsistent with the column count and null flag, and truncation partway through the column data.

---

## 5. Deferred

- **More types** — `Bool`, floats, timestamps, etc.
- **Alignment** — pad columns to their natural boundaries for faster access.
- **O(1) column access** — an in-tuple offset array, instead of the sequential walk.
- **Persistent catalog** — schemas are passed in by the caller for now; a system catalog will store them on disk later.
- **MVCC** — `t_xmin`/`t_xmax` are left zero; the transaction layer will set them at insert time.
