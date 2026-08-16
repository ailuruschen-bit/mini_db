# カタログモジュール テスト計画

> 言語: [English](Catalog-Test-Plan.md) | **日本語**

`internal/catalog` のテスト計画 — `blobStore` の後ろに JSON 文書として永続化されるメタデータ格納庫（テーブル、スキーマ、ファイル、インデックス）。*どう書くか*（フレームワーク、`-race`、カバレッジ）は [テスト規約](../Test-Conventions.ja.md) にあり、本書は *何を* を列挙する。[カタログ設計](../../design/catalog/Catalog-Design.ja.md) 参照。

---

## 0. 共有ヘルパー

- `sampleSchema()` — 2 列スキーマ（`id` int、`name` text nullable）。
- `open(t)` — 新しい一時ディレクトリ上のカタログ（ブラックボックス）。
- `fakeStore` — `load`/`save` をオンデマンドで失敗させるインメモリの `blobStore`（ホワイトボックス）。OS に触れずにエラー・ロールバック経路を走らせる。
- `oneCol()` — 1 列スキーマ（ホワイトボックス）。

---

## 1. 挙動（`catalog_test.go`、ブラックボックス `catalog_test`）

- [x] **作成と取得:** 作成したテーブルが名前でスキーマと割当ファイル（`rel_0.db`）付きで読み戻る; 無い名前は不在と報告。
- [x] **カウンタによるファイル名:** テーブルとインデックスは 1 つのカウンタから引く — 2 テーブル＋インデックスで `rel_0/1/2.db`。
- [x] **テーブル作成エラー:** 空名（`ErrEmptyName`）、列なし（`ErrNoColumns`）、名前重複（`ErrTableExists`）。
- [x] **インデックス:** 列とファイルを記録し、そのテーブルのメタデータに現れる。
- [x] **インデックスエラー:** 空名; 未知テーブル（`ErrTableNotFound`）; 範囲外の列（負値含む、`ErrBadColumn`）; インデックス名重複（`ErrIndexExists`）。
- [x] **一覧:** 全テーブルを名前順で。
- [x] **返り値のコピーは独立:** 返された `Schema`/`Indexes` を変更してもカタログに届かない（ディープコピー）。
- [x] **Dir:** ディレクトリを報告し、meta の相対 `File` を解決できる。
- [x] **永続化:** 再オープンをまたいでテーブル・インデックス・id カウンタが全て生存（再オープン後に作ったテーブルは次のファイル id を得る）。

## 2. 実ファイルの失敗（ブラックボックス）

- [x] **Open の mkdir 失敗:** 経路の途中がファイルであるパス下での Open はエラー。
- [x] **Open の load 失敗:** 読めないカタログファイル（ここでは `catalog.json` がディレクトリ）が load エラーを表面化。
- [x] **Open の parse 失敗:** 壊れた `catalog.json` が parse エラーを表面化。
- [x] **save の書き込み失敗:** テンポラリ書き込みを塞ぐと `CreateTable` から表面化し、変更はロールバックされる（後でテーブルは不在）。

## 3. 注入した失敗（`catalog_internal_test.go`、ホワイトボックス、`fakeStore`）

- [x] **load エラー**が `openWith` から表面化。
- [x] **tables マップ欠落:** 格納された `"tables": null` を空マップに復元し、読み込んだ id カウンタを保つ（続く作成がその id を使う）。
- [x] **CreateTable の save ロールバック:** save 失敗はテーブルを除去し id カウンタを戻すので、後の作成が id を再利用する。
- [x] **CreateIndex の save ロールバック:** save 失敗は追加したインデックスを取り消し id カウンタを戻す。

---

## 実施順序

1. スキャフォールド: `sampleSchema`、`open`、`fakeStore`、`oneCol`。
2. 作成/取得/一覧/インデックスの正常系 + カウンタ由来のファイル名。
3. 論理エラー（テーブル作成、インデックス作成）。
4. 返り値コピーの独立性、`Dir`、再オープンをまたぐ永続化。
5. 実ファイルの失敗（mkdir/load/parse/save）と注入した失敗（load/ロールバック）。
6. 仕上げ: `make check`、`feat(catalog): ...` でコミット。
