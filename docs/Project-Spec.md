# MicroDB: A PostgreSQL-inspired Disk-Oriented Database Engine
## プロジェクト要件定義書 (Requirement Specification)

---

### 1. プロジェクト概要 (Project Overview)
本プロジェクトは、PostgreSQLの内部アーキテクチャを参考に、Goでゼロから開発するディスク常駐型（Disk-oriented）リレーショナルデータベースエンジンです。

**開発の目的:** * データベースの低レイヤ（ストレージ、インデックス、トランザクション）の仕組みを深く理解する。
* Apple Silicon (M1) 環境に最適化された高性能なストレージエンジンの構築。
* 技術的な深さとエンジニアリング能力を証明するための求職用ポートフォリオ。

---

### 2. 主要機能 (Core Features)

#### ① インターフェースと接続 (Interface & Connectivity)
* **CLI (Command Line Interface):** - PostgreSQLの `psql` スタイルを模倣したコマンドラインツールを提供。
    - ターミナルからデータベースサーバへの接続、メタデータの閲覧。
* **マルチデータベース管理:** - `CREATE DATABASE` コマンドによる複数の独立したデータベース作成。
* **SQL実行:** - 基本的な DDL (Data Definition Language) および DML (Data Manipulation Language) のサポート。

#### ② データ整合性と制約 (Data Integrity & Constraints)
* **Primary Key & Foreign Key:** テーブル間のリレーションシップとデータの識別性を保証。
* **制約チェック:** `NOT NULL` および `UNIQUE` 制約の実装。

#### ③ 高度なストレージとインデックス (Storage & Indexing)
* **Heap File Storage:** - データは 8KB 固定サイズのページ（Page）単位で管理。
    - PostgreSQL準拠の **Slotted Page** 構造を採用。
* **B-Tree Index:** - 大規模データに対する $O(\log N)$ の高速検索を実現。
* **バッファプール管理 (Buffer Pool Manager):** - LRU（Least Recently Used）策略によるメモリとディスクの効率的な同期。

#### ④ トランザクションとACID特性 (Transactions & ACID)
* **ACID特性の保証:** 原子性、一貫性、分離性、持続性の実装。
* **WAL (Write-Ahead Logging):** - データの変更前にログを記録し、システムクラッシュ後のリカバリ（Recovery）を保証。
* **隔離レベル (Isolation Level):** - 「Read Committed」レベルを目標とした同時実行制御。

#### ⑤ 外部連携 (Language Support)
* **Python Client API:** - Pythonからデータベースを操作するための専用ドライバ/インターフェースを提供。

---

### 3. 技術スタック (Tech Stack)

| 項目                        | 選定内容                       |
| :-------------------------- | :----------------------------- |
| **Language**                | Golang 1.26+ (LTS)             |
| **Development Environment** | MacBook Air M1 (Apple Silicon) |
| **Build Tool**              | Go Modules                     |
| **Testing**                 | Testing                        |
| **Inspiration**             | PostgreSQL Architecture        |

---

### 4. 開発ロードマップ (Roadmap)

- [ ] **Phase 1:** ページ管理、ディスクI/O、バッファプール (Storage Engine)
- [ ] **Phase 2:** B-Treeインデックス、レコードのCRUD (Indexing)
- [ ] **Phase 3:** トランザクション管理、WALログ (ACID)
- [ ] **Phase 4:** SQLパーサー、CLIインターフェース (Interface)
- [ ] **Phase 5:** Python SDK開発と最終テスト (Integration)

---

### 5. 設計上のこだわり (Design Principles)
* **Performance:** M1 MacのNVMe SSDの特性を活かした高速なシーケンシャル・ライト（Sequential Write）。
* **Code Quality:** 単体テスト（Unit Test）のカバレッジを重視した堅牢な実装。
* **Documentation:** 日本のエンジニアチームでも理解しやすいよう、専門用語の定義を明確化。