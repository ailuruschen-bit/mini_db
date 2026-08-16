# System Catalog Design

> Language: **English** | [日本語](Catalog-Design.ja.md)

The system catalog (`internal/catalog`) is the store of **metadata about the data**: which tables exist, their column schemas, the file backing each table, and the indexes on them. It is the name-resolution authority the Table and SQL layers build on — turning a name like `users` into a schema, a heap file, and a set of indexes.

```
        SQL / executor (future)
              │  "users" -> schema, heap file, indexes
              ▼
        Catalog  (this module)   ── metadata only; records file names
              │  reads/writes one JSON document
              ▼
        catalog.json  (in the database directory)
```

Until now schemas were handed to the `record` layer in memory by the caller; the catalog persists them so a reopened database — and SQL name resolution — knows what exists.

---

## 1. Scope

The catalog **records metadata only**. Opening files, registering them with the buffer pool, and wiring a table's heap and indexes together is the **Table layer's** job (next module); the catalog just stores the file *names*.

| Op | Behavior |
| :-- | :-- |
| `Open(dir) → *Catalog` | Load the catalog under `dir` (creating `dir` and an empty catalog if none exists). |
| `CreateTable(name, schema) → TableMeta` | Record a table, assign it a heap file, persist. |
| `CreateIndex(table, name, column) → IndexMeta` | Record an index on a table's column, assign it a file, persist. |
| `GetTable(name) → (TableMeta, bool)` | Resolve a table by name. |
| `ListTables() → []TableMeta` | Every table, ordered by name. |
| `Dir() → string` | The directory, so a caller resolves a meta's `File` to a full path. |

**Deferred:** `Drop` / `Alter` (need file reclamation and, for column changes, data rewrite).

---

## 2. Metadata shape

```go
type TableMeta struct {
    Name    string
    Schema  record.Schema   // reuses the record layer's schema
    File    string          // heap file name for this table
    Indexes []IndexMeta
}

type IndexMeta struct {
    Name   string
    Column int              // which column of the table's schema
    File   string           // index file name
}
```

Reusing `record.Schema` keeps one definition of "a table's columns" across the encode/decode layer and the catalog.

---

## 3. Storage: a JSON document behind a store interface

The catalog persists as a **single JSON file** (`catalog.json`) in the database directory, rewritten on every DDL. JSON is chosen for v1 because it is trivial and human-readable — you can open `catalog.json` and see the database's structure while developing.

The file I/O sits behind a small `blobStore` interface (`load`/`save`), exactly as the buffer pool sits behind `diskManager`:

- **Production** uses a `fileStore` that reads the JSON file and, on save, writes a temp file then renames it over the target — so a crash mid-write cannot leave a half-written catalog.
- **Tests** inject a `fakeStore` whose `load`/`save` fail on demand, driving the error and rollback paths deterministically.

Crucially, callers depend only on the `Catalog` *interface* — the JSON storage is hidden. It can later be replaced (e.g. by self-describing heap tables, PostgreSQL's `pg_class`/`pg_attribute` style) with **no change to the layers above**. That authentic-but-heavier design carries a bootstrap chicken-and-egg (reading the tables that describe tables), so it is deferred; the interface makes the swap a local change.

---

## 4. File naming — a shared counter

`CreateTable` and `CreateIndex` each assign a file from one monotonic counter (`NextID`), named `rel_<id>.db` — PostgreSQL's *relfilenode* style. A counter (rather than deriving names from table/column names) keeps files **stable under renames** and free of naming collisions. The counter is part of the persisted state, so ids keep increasing across reopens.

---

## 5. Persistence and rollback

Every mutation applies to the in-memory state, then calls `persist`. If the write fails, the change is **rolled back in memory** — the new table/index is removed and the id counter is restored — so memory and disk never diverge. Metadata handed back to callers (`GetTable`, `ListTables`) is a **deep copy**, so a caller mutating a returned schema or index list cannot corrupt the catalog.

---

## 6. Deferred (with their future home)

- **Drop / Alter** — dropping needs to reclaim the relation's file (pairs with disk free-space reclaim); altering a column needs a data rewrite. Both wait on those mechanisms.
- **Catalog as heap tables** — store the catalog in self-describing tables (`pg_class`/`pg_attribute` style), queryable via SQL, with a hardcoded bootstrap schema. The `blobStore` seam makes this an internal swap.
- **Constraints / types beyond the record layer** — primary keys, uniqueness, foreign keys, richer column types belong here once the SQL layer needs them.
