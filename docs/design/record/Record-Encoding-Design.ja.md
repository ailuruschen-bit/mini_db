# レコードエンコード設計

> 言語: [English](Record-Encoding-Design.md) | **日本語**

レコード層（`internal/record`）は、**型付き・スキーマ記述された行**と、heap が格納する不透明なタプルバイトとを相互変換する。「値の行」と「`[]byte`」の橋渡し。

```
        エグゼキュータ / SQL（将来）    — 行: [42, "alice", nil]
              │  Schema.Encode(row) → []byte
              │  Schema.Decode([]byte) → row
              ▼
        record  （本モジュール）
              ▼
        heap → buffer → disk           — 不透明なバイト
```

---

## 1. タプルのバイト構成

エンコードされたタプルは [物理ストレージ設計](../storage/Physical-Storage-Design.ja.md) の構成に一致する:

```
[ TupleHeader (12 B) ] [ null ビットマップ（任意） ] [ 列データ ]
```

- **TupleHeader** — 12 バイトのヘッダー。レコード層は `col_count`、`t_hoff`（列データ開始オフセット）、`HasNull` フラグを書く; MVCC フィールド `t_xmin`/`t_xmax` は将来のトランザクション層のために **0** のまま残す。ヘッダーは `page.NewTuple(buf).TupleHeader()` を通じて読み書きし、バイト構成は `page` パッケージにのみ存在する。
- **null ビットマップ** — いずれかの列が NULL のとき（`HasNull` で示す）**のみ**存在。`ceil(col_count / 8)` バイトで、**ビット `i` が立つと列 `i` が NULL**。
- **列データ** — NULL でない列を、スキーマ順に。NULL 列はバイトを占めない。

---

## 2. 列の型（v1）

| 型 | Go の値 | エンコード |
| :-- | :-- | :-- |
| `TypeInt` | `int64` | 8 バイト、ビッグエンディアン |
| `TypeText` | `string` | `uint16` 長さ前置（ビッグエンディアン）+ UTF-8 バイト |
| NULL | `nil` | バイトなし（ビットマップで印） |

ビッグエンディアンはページヘッダーと揃える。`Bool` など他の型は後で容易に追加できる。

---

## 3. レイアウト戦略

列は **順次・自己記述的** に並ぶ: 固定型は固定幅を占め、可変型は自身の長さ前置を持ち、NULL は何も寄与しない。デコードは `t_hoff` から列を順に辿る。行全体のデコードは O(全体サイズ); 列 *N* へのランダムアクセスは O(N)（前の列を辿る必要がある） — v1 では許容。

アライメントなし: 列は詰めて配置し、`encoding/binary`（非アラインアクセスを扱える）で読む。

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

**Encode** が拒否するもの: 列数の不一致、非 nullable 列の `nil`、列と一致しない Go 型の値、`uint16` を超えるテキスト、10 ビットの `col_count` フィールドに収まらない列数のスキーマ。

**Decode** が拒否するもの: ヘッダーより短いバイト列、スキーマと食い違う `col_count`、列数と null フラグに整合しない `t_hoff`、列データ途中での切り詰め。

---

## 5. 先送り

- **型の追加** — `Bool`、浮動小数点、タイムスタンプなど。
- **アライメント** — 高速アクセスのため列を自然境界にパディング。
- **O(1) 列アクセス** — 順次走査に代えてタプル内オフセット配列。
- **永続カタログ** — 今はスキーマを呼び出し側が渡す; 将来システムカタログがディスクに保持する。
- **MVCC** — `t_xmin`/`t_xmax` は 0 のまま; トランザクション層が挿入時に設定する。
