# minidb: A PostgreSQL-inspired Disk-Oriented Database Engine
## Requirement Specification

> Language: **English** | [日本語](Project-Spec.ja.md)

---

### 1. Project Overview

minidb is a disk-oriented relational database engine built from scratch in Go, modeled on PostgreSQL's internal architecture.

**Goals:**
* Deeply understand the low-level machinery of a database (storage, indexing, transactions).
* Build a high-performance storage engine tuned for Apple Silicon (M1).
* Serve as a job-seeking portfolio demonstrating engineering depth.

---

### 2. Core Features

#### ① Interface & Connectivity
* **CLI:** a command-line tool mimicking PostgreSQL's `psql` style — connect to the server and browse metadata from the terminal.
* **Multi-database management:** create multiple independent databases via `CREATE DATABASE`.
* **SQL execution:** support for basic DDL and DML.

#### ② Data Integrity & Constraints
* **Primary Key & Foreign Key:** enforce inter-table relationships and row identity.
* **Constraint checks:** `NOT NULL` and `UNIQUE`.

#### ③ Storage & Indexing
* **Heap File Storage:** data managed in fixed 8 KB pages using the PostgreSQL-style **Slotted Page** structure.
* **B-Tree Index:** O(log N) lookups over large datasets.
* **Buffer Pool Manager:** efficient memory/disk synchronization using an LRU policy.

#### ④ Transactions & ACID
* **ACID guarantees:** atomicity, consistency, isolation, durability.
* **WAL (Write-Ahead Logging):** log changes before applying them to guarantee crash recovery.
* **Isolation Level:** concurrency control targeting "Read Committed".

#### ⑤ Language Support
* **Python Client API:** a dedicated driver/interface for operating the database from Python.

---

### 3. Tech Stack

| Item                    | Choice                         |
| :---------------------- | :----------------------------- |
| **Language**            | Golang 1.26+ (LTS)             |
| **Development Environment** | MacBook Air M1 (Apple Silicon) |
| **Build Tool**          | Go Modules                     |
| **Testing**             | Go `testing` (standard library) |
| **Inspiration**         | PostgreSQL Architecture        |

---

### 4. Roadmap

- [ ] **Phase 1:** Page management, disk I/O, buffer pool (Storage Engine)
- [ ] **Phase 2:** B-Tree index, record CRUD (Indexing)
- [ ] **Phase 3:** Transaction management, WAL logging (ACID)
- [ ] **Phase 4:** SQL parser, CLI interface (Interface)
- [ ] **Phase 5:** Python SDK and final integration testing (Integration)

---

### 5. Design Principles
* **Performance:** exploit the sequential-write characteristics of the M1 Mac's NVMe SSD.
* **Code Quality:** robust implementation with a focus on unit-test coverage.
* **Documentation:** clear definitions of technical terms, kept bilingual (English / Japanese).
