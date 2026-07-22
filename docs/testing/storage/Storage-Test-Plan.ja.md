# ストレージモジュール テスト計画

> 言語: [English](Storage-Test-Plan.md) | **日本語**

`internal/storage/page` パッケージのテスト計画。*どう書くか*（フレームワーク、テーブル駆動、ゴールデン検証、必須 4 項目）は [テスト規約](../Test-Conventions.ja.md) で定義済み。本書はコンポーネントごとに *何をカバーするか* を列挙します。

**対象外:** ディスクファイル I/O（未実装）。「外部バイト → メモリ」の経路は `NewSlottedPage` + getter による往復（round-trip）忠実性として検証します。

---

## 0. 共有ヘルパー

- [x] `blankPage()` — ゼロ初期化された `*SlottedPage`。
- [x] `blankSlotEntry()` / `blankTupleHeader()` / `blankTuple(size)` — 単体のゼロ初期化ビュー。ページを組み立てずにコンポーネントを検証できる。
- [x] `beBytes(v, size)` — ゴールデン検証用のビッグエンディアン期待値ビルダー。

事前設定済みの `knownPage()` ヘルパーは不要と判明した。各テストは `blankPage()` から必要な状態だけを組み立てており、そのほうが個々のテストを単独で読める。

---

## 1. PageHeader（`page_header_test.go`、ホワイトボックス）

フィールド（ビッグエンディアン）: `pd_lsn[0:8]` `pd_checksum[8:10]` `pd_flags[10:12]` `pd_lower[12:14]` `pd_upper[14:16]` `pd_special[16:18]` `pd_pagesize[18:20]` `pd_prune_xid[20:24]`。

- [x] 往復: set 後に get が同じ値を返す（全フィールド）。
- [x] ゴールデンオフセット / バイト順: set 後、当該範囲の生バイトがビッグエンディアン表現を保持する。
- [x] フィールド独立性: あるフィールドの書き込みが隣接フィールドを変えない。
- [x] 境界値: `0` と型の最大値（u16 / u32 / u64）。

## 2. SlotEntry（`slot_entry_test.go`、ホワイトボックス）— 最高リスク（4 バイトのビットパック）

レイアウト: `Offset`（ビット 31–17）、`Length`（ビット 16–2）、`Flags`（ビット 1–0）。

- [x] `Offset` / `Length` / `Flag` の往復。
- [x] **ビットフィールド独立性:** あるフィールドを全 1 にし、別のフィールドを set して、最初のフィールドが不変であることを検証（全組み合わせ）。
- [x] 境界値: `0`、最大値（`Offset`/`Length` = 32767、`Flag` = 3）。
- [x] エラーパス: `SetOffset`/`SetLength`（> 32767）と `SetFlag`（> 3）はエラーを返し、書き込まない。
- [x] ゴールデン: 既知の offset/length/flag → 期待する生 4 バイト。

## 3. TupleHeader（`tuple_test.go`、ホワイトボックス）— 12 バイトレイアウト

レイアウト: `t_xmin[0:4]` `t_xmax[4:8]` `flags`(6b)+`col_count`(10b) `[8:10]` `t_hoff[10]` 予約 `[11]`。

- [x] `TupleHeader()` が先頭 12 バイトのビューを返す。
- [x] `TxMin` / `TxMax` の往復とゴールデンオフセット。
- [x] `Flags` / `ColumnCount` のパック独立性（一方の set が他方を保持）、境界値（flags = 63、col_count = 1023）、エラーパス（> 最大値）。
- [x] `Hoff` の往復。予約バイト `[11]` がすべての setter で不変。
- [x] `HasNull` が `FlagHasNull` ビットを反映する。

## 4. SlottedPage 組み立て（`slotted_page_test.go`）

- [x] **`NewSlottedPage` の値セマンティクス:** 構築後に入力配列を変更してもページに影響しない。
- [x] **ゼロコピーの別名不変条件:** `Header()` / `SlotEntry` 経由の書き込みが、新たに取得したビューと生 `data` の両方で見える。
- [x] **SlotCount:** `pd_upper` が N を示すとき N; `pd_upper == HeaderSize`（空ページ）のとき `0`。
- [x] **SlotEntryAt:** スロット i が `HeaderSize+i*SlotEntrySize` の窓に対応する; 返された entry への setter がページに書き戻される; 範囲外の添字は panic する。
- [x] **Slots:** すべての entry を順に yield し、早期 `break` を尊重し、一切アロケートしない（`testing.AllocsPerRun` で固定）。
- [x] **LocateTupleByEntry:** 返されたスライスが `[offset, offset+length)` を正確に覆う。

## 5. 往復忠実性（`roundtrip_test.go`、ブラックボックス `page_test`）

- [x] 公開 API でページを組み立て（ヘッダー + スロット + タプル）→ `data` を取り出す → `NewSlottedPage` で再構築 → すべての getter が同じ値を読み戻す。

---

## 実施順序

1. スキャフォールド: テストファイル + 共有ヘルパー。空のスイートがコンパイルできることを確認。
2. PageHeader — テーブル駆動 + ゴールデンのパターンを確立。
3. SlotEntry — ビットパック、独立性、エラーパス。
4. TupleHeader — 12 バイトのパックレイアウト。
5. SlottedPage 組み立て — 値セマンティクス、別名、スロットアクセス、LocateTupleByEntry。
6. 往復（ブラックボックス）。
7. 仕上げ: `go test -cover`、`go vet`、`test(storage): ...` でコミット。
