# minidb Documentation

> Language: **English** | [日本語](README.ja.md)

Documentation is organized by purpose. Every document is bilingual: the base file (`*.md`) is English, and its `*.ja.md` sibling is Japanese.

## Structure

| Area          | Path                     | Contents                                        |
| :------------ | :----------------------- | :---------------------------------------------- |
| **Overview**  | [`overview/`](overview/) | Project requirements and roadmap.               |
| **Design**    | [`design/`](design/)     | Design specifications, grouped by module.       |
| **Testing**   | [`testing/`](testing/)   | Global test conventions and per-module test plans. |

## Index

### Overview
- [Project Specification](overview/Project-Spec.md)

### Design
- **Storage**
  - [Physical Storage Design](design/storage/Physical-Storage-Design.md)
- **Access**
  - [Heap Access Method Design](design/access/Heap-Design.md)
  - [B+Tree Index Design](design/access/BTree-Index-Design.md)
- **Record**
  - [Record Encoding Design](design/record/Record-Encoding-Design.md)

### Testing
- [Test Conventions](testing/Test-Conventions.md) — global rules for all modules
- **Storage**
  - [Storage Module Test Plan](testing/storage/Storage-Test-Plan.md)
- **Access**
  - [Access Module Test Plan](testing/access/Access-Test-Plan.md)
- **Record**
  - [Record Module Test Plan](testing/record/Record-Test-Plan.md)
