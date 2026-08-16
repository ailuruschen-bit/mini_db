# Catalog Module Test Plan

> Language: **English** | [日本語](Catalog-Test-Plan.ja.md)

Test plan for `internal/catalog` — the metadata store (tables, schemas, files, indexes) persisted as a JSON document behind a `blobStore`. The *how* (framework, `-race`, coverage) is in [Test Conventions](../Test-Conventions.md); this lists *what*. See the [Catalog Design](../../design/catalog/Catalog-Design.md).

---

## 0. Shared helpers

- `sampleSchema()` — a two-column schema (`id` int, `name` text nullable).
- `open(t)` — a catalog over a fresh temp dir (black-box).
- `fakeStore` — an in-memory `blobStore` whose `load`/`save` fail on demand (white-box), so error and rollback paths run without touching the OS.
- `oneCol()` — a one-column schema (white-box).

---

## 1. Behaviour (`catalog_test.go`, black-box `catalog_test`)

- [x] **Create & get:** a created table reads back by name with its schema and assigned file (`rel_0.db`); a missing name is reported absent.
- [x] **File names by counter:** tables and indexes draw from one counter — `rel_0/1/2.db` across two tables then an index.
- [x] **Create-table errors:** empty name (`ErrEmptyName`), no columns (`ErrNoColumns`), duplicate name (`ErrTableExists`).
- [x] **Index:** records column and file, and appears on its table's metadata.
- [x] **Index errors:** empty name; unknown table (`ErrTableNotFound`); out-of-range column, incl. negative (`ErrBadColumn`); duplicate index name (`ErrIndexExists`).
- [x] **List:** every table, ordered by name.
- [x] **Returned copies are independent:** mutating a returned `Schema`/`Indexes` does not reach into the catalog (deep copy).
- [x] **Dir:** reports the directory, for resolving a meta's relative `File`.
- [x] **Persistence:** across a reopen, tables, indexes and the id counter all survive (a table created after reopen gets the next file id).

## 2. Real-file failures (black-box)

- [x] **Open mkdir failure:** opening under a path whose component is a file errors.
- [x] **Open load failure:** an unreadable catalog file (here, a directory at `catalog.json`) surfaces a load error.
- [x] **Open parse failure:** a corrupt `catalog.json` surfaces a parse error.
- [x] **Save write failure:** a blocked temp-file write surfaces from `CreateTable` and the change is rolled back (the table is absent afterwards).

## 3. Injected failures (`catalog_internal_test.go`, white-box, `fakeStore`)

- [x] **Load error** surfaces from `openWith`.
- [x] **Missing tables map:** a stored `"tables": null` is recovered to an empty map, keeping the loaded id counter (a create then uses that id).
- [x] **CreateTable save rollback:** a save failure removes the table and restores the id counter, so a later create reuses the id.
- [x] **CreateIndex save rollback:** a save failure drops the appended index and restores the id counter.

---

## Execution order

1. Scaffold: `sampleSchema`, `open`, `fakeStore`, `oneCol`.
2. Create/get/list/index happy paths + counter-based file names.
3. Logical errors (create-table, create-index).
4. Returned-copy independence, `Dir`, persistence across reopen.
5. Real-file failures (mkdir/load/parse/save) and injected failures (load/rollback).
6. Wrap up: `make check`, commit as `feat(catalog): ...`.
