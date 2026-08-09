# レコードモジュール テスト計画

> 言語: [English](Record-Test-Plan.md) | **日本語**

`internal/record`（型付き行とタプルバイトの相互変換）のテスト計画。*どう書くか*は [テスト規約](../Test-Conventions.ja.md) にあり、本書は *何を* を列挙する。

---

## 1. Encode / Decode（`record_test.go`、ブラックボックス `record_test`）

- [x] **往復:** 値と型が Encode → Decode を通して保たれる。空テキスト（NULL ではない）と `int64` の境界も含む。
- [x] **NULL 往復:** nullable 列の `nil` が往復し、タプルの `HasNull` フラグが立つ。
- [x] **NULL なしはビットマップを省く:** NULL がなければ `t_hoff` はヘッダーサイズに等しく `HasNull` は落ちる（`page` 層とのヘッダー相互運用 `col_count`・`t_hoff` も確認）。
- [x] **Encode エラー:** 列数不一致（値が少ない/多い）; 非 nullable 列の `nil`; int / text 列に対する誤った Go 型; `uint16` を超えるテキスト。
- [x] **未知の型:** Encode と Decode の両方が拒否する。
- [x] **列が多すぎる:** 1024 列のスキーマ（10 ビットの `col_count` 上限超え）を拒否する。
- [x] **Decode エラー:** ヘッダーより短い; スキーマと `col_count` 不一致; 不整合な `t_hoff`; ビットマップ手前での切り詰め; int の切り詰め; text 長さの切り詰め; text 本体の切り詰め。

## 2. 全スタック統合（`integration_test.go`、ブラックボックス）

- [x] 型付き行を Encode し、`heap.Insert` で格納し、TID で読み戻し、同じ行へ Decode する — NULL を含む複数行にわたって。record ↔ heap ↔ buffer ↔ disk のスタックが往復することを証明する。

---

## 実施順序

1. 往復（NULL あり/なし）、ヘッダー相互運用。
2. Encode エラー、Decode エラー（テーブル駆動）。
3. 境界: 列が多すぎる。
4. 全スタック統合。
5. 仕上げ: `make check`（vet + lint + race + カバレッジ）、`feat(record): ...` でコミット。
