# minidb

**English** | [日本語](README.ja.md)

> A PostgreSQL-inspired, disk-oriented relational database engine, written from scratch in Go.

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Status](https://img.shields.io/badge/status-WIP%20(storage%20engine)-orange)

## Overview

**minidb** reimplements the low-level internals of a relational database — storage, indexing, transactions — from first principles, modeled after PostgreSQL's on-disk architecture.

The goal is not a production database, but to understand how one works **at the byte level**: how a page is laid out, how variable-length records are packed into fixed-size pages, and how MVCC / WAL metadata is threaded through the storage format.

**Why build this**

- Learn database low-layer internals (storage / indexing / transactions) by building them, not just reading about them.
- Hand-write every byte/bit-level encoding — no ORM, no serialization library — to internalize the format.
- A portfolio piece for systems-level engineering in Go.

## Project status

This is an in-progress project. The **storage engine layer is implemented**; the higher layers are specified and on the roadmap.

- [x] Slotted-page storage layout (8 KB pages, PostgreSQL-style)
- [x] Page header (24 B) with MVCC / WAL metadata fields
- [x] Slot directory with hand-packed bit fields (`SlotEntry`)
- [x] Tuple / tuple-header layout (MVCC-aware) — *header parsing in progress*
- [ ] B-Tree index
- [ ] Buffer pool (LRU)
- [ ] Transactions & Write-Ahead Logging (WAL)
- [ ] SQL parser & `psql`-style CLI
- [ ] Python client driver

The full target design is described in [docs/overview/Project-Spec.md](docs/overview/Project-Spec.md). Unchecked items above are specified but **not yet implemented**.

## Design highlights

The storage engine is where the current work lives. A few deliberate decisions:

### Slotted page (8 KB)

Each page is a fixed **8 KB** block using a **slotted page** structure. This lets variable-length tuples live inside a fixed-size page while keeping a **stable slot id** even when the physical tuple location moves. The page size follows PostgreSQL's default.

### Bidirectional growth

Within a page, the **slot directory grows forward** from the end of the header, while **tuple data grows backward** from the end of the page. `pd_lower` and `pd_upper` track the two frontiers; the page is full when they would cross.

```
┌────────────┬──────────────┬───────────────┬──────────────┐
│ PageHeader │ Slot dir  →  │  free space   │  ←  Tuples    │
│  (24 B)    │ (grows fwd)  │               │ (grows back)  │
└────────────┴──────────────┴───────────────┴──────────────┘
             ^ pd_lower                      ^ pd_upper
```

### Hand-packed slot entries

Each `SlotEntry` is packed into **4 bytes** — a 15-bit offset, a 15-bit length, and a 2-bit status flag (`UNUSED / NORMAL / DELETED / REDIRECT`) — assembled with explicit mask/shift operations rather than a serialization library.

### MVCC / WAL-aware headers

The 24-byte page header reserves PostgreSQL-style fields — `pd_lsn` (WAL), `pd_checksum`, `pd_lower` / `pd_upper`, `pd_prune_xid` (MVCC / VACUUM) — and the tuple header carries transaction and visibility metadata. The format is ready for the transaction and recovery layers before they are built.

### Zero-copy access

Headers and entries are accessed as fixed-size array pointers (`*[N]byte`) that wrap the underlying page buffer directly, with big-endian getters/setters — no per-field copying.

See [docs/design/storage/Physical-Storage-Design.md](docs/design/storage/Physical-Storage-Design.md) and the page-layout diagram in [docs/design/storage/assets/Slotted-Page.png](docs/design/storage/assets/Slotted-Page.png) for the full byte layout.

## Project structure

```
.
├── cmd/
│   └── server/          # entry point (CLI / server — stub for now)
├── internal/
│   └── storage/         # storage engine
│       ├── slotted_page.go   # page: header + slot directory access
│       ├── page_header.go    # 24 B page header fields
│       ├── slot_entry.go     # 4 B slot entry (bit-packed)
│       └── tuple.go          # tuple & tuple header layout
├── docs/                # documentation (bilingual: *.md / *.ja.md)
│   ├── overview/        # project spec & roadmap
│   ├── design/          # design specs, grouped by module
│   └── testing/         # test conventions & plans
└── go.mod
```

## Getting started

Requires **Go 1.26+**.

```bash
git clone https://github.com/ailuruschen-bit/mini_db.git
cd mini_db
go build ./...
go run ./cmd/server   # entry point (currently a stub)
```

## Roadmap

| Phase | Layer | State |
|------|-------|-------|
| 1 | Storage engine — slotted page, tuple layout | **current** |
| 2 | B-Tree index — O(log N) lookups | planned |
| 3 | Buffer pool — LRU page cache | planned |
| 4 | Transactions & WAL — atomicity + crash recovery | planned |
| 5 | SQL & CLI — parser + `psql`-style shell | planned |
| 6 | Python client — driver / interface | planned |

---

*Under active development as a personal learning and portfolio project.*
