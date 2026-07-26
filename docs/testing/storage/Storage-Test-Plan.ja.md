# ストレージモジュール テスト計画

> 言語: [English](Storage-Test-Plan.md) | **日本語**

`internal/storage` モジュール — `page`・`disk`・`buffer` パッケージのテスト計画。*どう書くか*（フレームワーク、テーブル駆動、ゴールデン検証、必須 4 項目、並行コードの `-race`）は [テスト規約](../Test-Conventions.ja.md) で定義済み。本書はコンポーネントごとに *何をカバーするか* を列挙します。

モジュールはボトムアップに構築される: `page`（バイトレイアウト）→ `disk`（ページ ↔ ファイル）→ `buffer`（ディスク上のページキャッシュ）。各層のテストは、下の層が既にカバー済みであることを前提とする。

---

## 0. 共有ヘルパー

**page**（`helpers_test.go`）:

- [x] `blankPage()` — ゼロ初期化された `*SlottedPage`。
- [x] `blankSlotEntry()` / `blankTupleHeader()` / `blankTuple(size)` — 単体のゼロ初期化ビュー。ページを組み立てずにコンポーネントを検証できる。
- [x] `beBytes(v, size)` — ゴールデン検証用のビッグエンディアン期待値ビルダー。

**disk**（`disk_test.go`）: `tempDBPath`、`writeZeroPages`、`fileSize`、`filledPage(b)`、`mustOpen`。

**buffer**: `probeOffset` + `writeProbe`/`readProbe`（識別可能なボディバイト）、`mustPool`（一時ファイル上の実ディスク）、`readProbeFromDisk`（プールを介さずファイルから直接ページを読む）、そして `fakeDisk` — `ReadPage`/`WritePage`/`AllocatePage` をオンデマンドで失敗させられるインメモリの `diskManager`。プールのエラー回復経路を決定論的に駆動するために使う。

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

## 4. SlottedPage 組み立て（`slotted_page_test.go`、ホワイトボックス）

- [x] **`NewSlottedPage` の値セマンティクス:** 構築後に入力配列を変更してもページに影響しない。
- [x] **ゼロコピーの別名不変条件:** `Header()` / `SlotEntry` 経由の書き込みが、新たに取得したビューと生 `data` の両方で見える。
- [x] **SlotCount:** `pd_upper` が N を示すとき N; `pd_upper == HeaderSize`（空ページ）のとき `0`。
- [x] **SlotEntryAt:** スロット i が `HeaderSize+i*SlotEntrySize` の窓に対応する; 返された entry への setter がページに書き戻される; 範囲外の添字は panic する。
- [x] **Slots:** すべての entry を順に yield し、早期 `break` を尊重し、一切アロケートしない（`testing.AllocsPerRun` で固定）。
- [x] **LocateTupleByEntry:** 返されたスライスが `[offset, offset+length)` を正確に覆う。
- [x] **Init:** 妥当な空ページに整形する — `pd_upper = HeaderSize`、`pd_lower = PageSize`、`SlotCount == 0` — バッキング配列が別ページのデータで埋まっていても残バイトをゼロにする。（単にゼロにしただけのページは `SlotCount` がアンダーフローするため、ポインタ値を明示的に固定する。）

## 5. 往復忠実性（`roundtrip_test.go`、ブラックボックス `page_test`）

- [x] 公開 API でページを組み立て（ヘッダー + スロット + タプル）→ `data` を取り出す → `NewSlottedPage` で再構築 → すべての getter が同じ値を読み戻す。

---

## 6. DiskManager（`disk_test.go` ブラックボックス、`disk_internal_test.go` ホワイトボックス）

ページ番号で指定される固定サイズページを、ヒープファイルと呼び出し側バッファの間で移動する。ミューテックスは `numPages` のみを守り、位置指定 I/O はロックフリー。

- [x] **Open:** 欠けているファイルを作成する; 既存ファイルのページ数を数える; ページの整数倍でないサイズ（破損）を拒否する。
- [x] **Close** がハンドルを解放する。
- [x] **AllocatePage:** 最初の id は 0; id は連番; 新ページはゼロ埋め; 既存ファイルのページ数を継続する; 割り当てたページは再オープンしても残る; **並行**割り当てが互いに異なる id を返す（多数の goroutine、`-race`）; 拡張失敗時は `numPages` が不変; `MaxPageID` を超える割り当てを拒否する（ホワイトボックス、カウンタを強制設定）。
- [x] **ReadPage / WritePage:** write→read の往復; あるページへの書き込みが隣接ページを乱さない（独立性）; 書いたページは再オープンしても残る; 範囲外の id を拒否; ちょうど 1 ページ長でないバッファを拒否。
- [x] **並行混在操作:** ストレスの総仕上げ — 多数の goroutine が各自のページを割り当て・書き・読みしつつ、`NumPages` を読むポーラーも走らせる（`-race`）。カウンタのロックとロックフリー I/O が安全に共存することを証明する。
- [x] **Sync** が開いたファイルで成功する; **閉じたファイルへの Sync** はエラーを表面化する。

## 7. LRUReplacer（`lru_replacer_test.go`、ホワイトボックス）

追い出し可能なフレーム（pin 数 0）を追跡し、最も長く使われていないものを返す。内部ロックはない — 常にプールロック下で呼ばれる。

- [x] **空:** 新品の replacer には victim がない; サイズ 0。
- [x] **順序:** 最も長く unpin されていないフレームが最初に追い出される。
- [x] **Pin による除外:** フレームを候補集合から外し、victim としてスキップされる。
- [x] **繰り返し Unpin は順序を保つ:** 既に追跡中のフレームの unpin は位置を動かさない（最近性はそのフレームが最初に追い出し可能になった時点で固定される）。
- [x] **Pin してから Unpin は最新:** 再投入されたフレームは最新端に入る。
- [x] **未追跡フレームの Pin** は無害な no-op であり、他のフレームに影響しない。

## 8. BufferPool（`buffer_pool_test.go` ブラックボックス `buffer_test`、`buffer_pool_internal_test.go` ホワイトボックス）

ディスクページをキャッシュする固定数のフレーム。pin カウント、dirty フラグ、LRU 追い出し、flush/sync を持つ。v1 は全メタデータを 1 本のロックで守り、ディスク I/O をまたいで保持する; ページの*内容*はロックで守られないため、並行テストはページ所有権を分割する。

**構築（ホワイトボックス）**

- [x] 非正のプールサイズは panic する。
- [x] 新品のプールは全フレームがフリーリストにあり、常駐も追い出し候補もない。

**Fetch と pin**

- [x] 常駐中は `FetchPage` が同一の `*SlottedPage` インスタンスを返す。
- [x] pin カウントは fetch ごとに増え unpin ごとに減る; フレームが追い出し候補になるのは 0 のときだけ（カウンタと replacer をホワイトボックスで検証）。
- [x] 全フレームが pin された状態でのミスは `ErrNoFreeFrame` を返す — `FetchPage`（ホワイトボックス、シード済み fakeDisk）と `NewPage`（ブラックボックス）の両方から。pin を 1 つ解放すれば再び空きができる。

**NewPage**

- [x] 整形済みで即使用可能な空ページ（`SlotCount == 0`、`pd_upper = HeaderSize`、`pd_lower = PageSize`）を返す。ゼロの塊ではない。
- [x] 連番の id を割り当てる（呼び出しごとにファイルを拡張）。
- [x] dirty で始まる — 整形したヘッダーは flush まではメモリにのみ存在する（ホワイトボックス）。

**dirty ライフサイクル・追い出し・永続化**

- [x] 空きを作るために追い出された dirty ページは書き戻されるため、再 fetch で同じ内容が再ロードされる。
- [x] dirty フラグは協調的: clean として unpin された変更は書き戻されず、追い出しで失われる。
- [x] `FlushPage` は追い出さずに dirty ページのバイトをディスクへ押し出す; 既に clean なページの 2 度目の flush は no-op。
- [x] `dirty` は sticky: 後の clean な unpin が、先の保持者が報告した変更を消さない。
- [x] **再オープンの総仕上げ:** ページを作成しプローブを書き、`FlushAll`・`Sync`・close; ファイルを再オープンして全プローブを読み戻す。

**エラーパス（大半は `fakeDisk` によるホワイトボックス）**

- [x] 非常駐ページ、または pin 数 0 のページの unpin は、状態を触らずにエラーを返す。
- [x] 非常駐ページの flush はエラー; 未割り当てページの fetch はエラー。
- [x] dirty な victim の書き戻し失敗はプールを完全に復元する（victim は常駐・dirty・追い出し可能のまま; リークしたフレームなし）。
- [x] `AllocatePage` の失敗は取得済みフレームをフリーリストへ戻す; プールは回復する。
- [x] ページロード（フェーズ B）の失敗は取得済みフレームをフリーリストへ戻す。
- [x] flush 中の書き込みエラーは `FlushPage` と `FlushAll` から伝播する; ページは再試行のため dirty のまま。

**並行性（`-race`）**

- [x] 多数の goroutine が互いに素なページ集合を所有し、fetch → 自分のバイトを書く → dirty で unpin を繰り返して、追い出しを多発させる; メタデータロックがプールの整合性を保つ（最終プローブと pin の均衡を検証）。所有者が書く間は pin がフレームを保持するため、ページ内容は競合しない。

---

## 実施順序

1. スキャフォールド: テストファイル + 共有ヘルパー。空のスイートがコンパイルできることを確認。
2. PageHeader — テーブル駆動 + ゴールデンのパターンを確立。
3. SlotEntry — ビットパック、独立性、エラーパス。
4. TupleHeader — 12 バイトのパックレイアウト。
5. SlottedPage 組み立て — 値セマンティクス、別名、スロットアクセス、`Init`。
6. 往復（ブラックボックス）。
7. DiskManager — Open/Close、AllocatePage、Read/Write、並行性（`-race`）、Sync。
8. LRUReplacer — 順序と pin/unpin セマンティクス。
9. BufferPool — 構築、fetch/pin、NewPage、dirty/追い出し/永続化、`fakeDisk` によるエラーパス、並行性（`-race`）。
10. 仕上げ: `go test -race -cover ./...`、`go vet`、`test(<pkg>): ...` でコミット。
