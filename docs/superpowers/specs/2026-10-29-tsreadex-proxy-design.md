# tsreadex-proxy 設計

作成日: 2026-10-29 / 状態: ドラフト(ユーザーレビュー待ち)

## 1. 目的

[tsreadex](https://github.com/xtne6f/tsreadex) をHTTPプロキシとして包み、Jellyfin の Live TV から Mirakurun/mirakc のライブ視聴を、副音声・字幕が安定化された状態で見られるようにする。

tsreadex は stdin/ファイルから MPEG-TS を読み、サービス選択・音声/字幕の補完・ARIB字幕のID3 timed-metadata化を行って stdout に出すCLIで、HTTPの口を持たない。本プロジェクトはその前後をHTTPでつなぐ薄いプロキシである。

### 成功基準

- Jellyfin に「M3Uチューナー」としてプロキシの `/playlist.m3u` を、「XMLTV」としてプロキシの `/xmltv` を登録すると、チャンネル一覧と番組表が表示される。
- チャンネルを選局すると、tsreadex を通した TS が再生される(映像・音声が出る。副音声・字幕の挙動は tsreadex のオプションに従う)。
- クライアント切断時に、上流接続と tsreadex プロセスが残らない。

### 非目的(YAGNI)

認証、TLS、メトリクス、設定のホットリロード、クエリによる tsreadex オプションの上書き、HDHomeRun互換API、録画ファイルの変換、複数クライアント間での上流共有。LAN内またはリバースプロキシ配下での運用を前提とする。

## 2. 前提と確認済みの事実

| 事実 | 出典 |
|---|---|
| Mirakurun の `/api/iptv/playlist` は `http(s)://{Host}/api/services/{id}/stream` を出力し、先頭行に `url-tvg="…/api/iptv/xmltv"`、ロゴに `tvg-logo="…/api/services/{id}/logo"` を持つ。`tvg-id` は `{id}` | Chinachu/Mirakurun `src/Mirakurun/api/iptv/playlist.ts` |
| mirakc の `/api/iptv/playlist` は `http://{host}/api/services/{id}/stream?{query}` を出力する。`url-tvg` は出さず、ストリームURLにクエリが付く | mirakc/mirakc `mirakc-core/src/web/api/iptv.rs` |
| `{id}` は `networkId*100000 + serviceId`(Mirakurun)。tsreadex の `-n` に渡すのは serviceId | Mirakurun の仕様(実装時に実機で再確認) |
| tsreadex は依存のない C++11。`make` 一発でビルドできる。最新タグは `master-260428`。リリース成果物は zip のみ | xtne6f/tsreadex `Makefile`, tags |
| `-n` が 0 のとき `-a/-b/-c/-u` は無視される | tsreadex Readme |
| 入力がパイプのとき、`-m 0` では `-t 0` でなければならず、`-m 2` では `-t` が非0でなければならない | tsreadex Readme |

### 未確認(実装前または実機検証で確認する)

- Jellyfin が選局時に stream URL に対して行うプローブ(ffprobe)の挙動。出力開始が遅れる場合の許容時間
- `-d` の既定値(`13` = 1+4+8)が Jellyfin 同梱の ffmpeg で期待どおり動くか
- `-m 2 -t N` のほうがライブ用途に適するか。既定の `-m 0 -t 0` でも上流切断で終了するため、まずは既定のまま動かして検証する
- Jellyfin 側のロゴ取得が M3U の `tvg-logo` に従うか

## 3. アーキテクチャ

```
Jellyfin ──GET /playlist.m3u──▶ proxy ──GET {MIRAKURUN_URL}/api/iptv/playlist──▶ Mirakurun
         ◀─ URLをproxy宛に書き換えたM3U ─┘
Jellyfin ──GET /stream/{id}[?q]──▶ proxy ──GET {MIRAKURUN_URL}/api/services/{id}/stream[?q]──▶ Mirakurun
                                     │  上流body ─▶ stdin [tsreadex -n <serviceId> <TSREADEX_ARGS>] stdout ─▶ レスポンス
```

接続ごとに tsreadex を1プロセス起動する(状態を持たないパススルー)。同一チャンネルの重複視聴によるチューナー共有は Mirakurun 側が行う。

### エンドポイント

| パス | 動作 |
|---|---|
| `GET /playlist.m3u` | 上流のM3Uを取得し、書き換えて返す(§4) |
| `GET /xmltv` | 上流の `/api/iptv/xmltv` をそのまま中継する |
| `GET /logo/{id}` | 上流の `/api/services/{id}/logo` をそのまま中継する |
| `GET /stream/{id}` | §5 のとおり。クエリ文字列は上流へそのまま転送する |
| `HEAD /stream/{id}` | 上流・tsreadex は起動せず、ヘッダ(`Content-Type: video/MP2T`)のみ 200 で返す |

`{id}` は10進数の数字列のみ受け付ける(それ以外は 400)。上流のホストとパスはプロキシ側で固定で、クライアントが指定できるのは `{id}` とクエリ文字列だけである。

## 4. M3U の書き換え

上流のM3Uを行単位で処理する。

- 行頭 `#EXTM3U` の `url-tvg="…"` は、プロキシの `{PUBLIC_BASE}/xmltv` に置き換える。属性がない場合(mirakc)は追加しない。
- `tvg-logo="…/api/services/{id}/logo"` は `{PUBLIC_BASE}/logo/{id}` に置き換える。
- URL行 `http(s)://…/api/services/{id}/stream[?query]` は `{PUBLIC_BASE}/stream/{id}[?query]` に置き換える。
- 上記に一致しない行は変更せずそのまま出力する。

`{PUBLIC_BASE}` は `PUBLIC_BASE_URL` があればそれ、なければリクエストの Host ヘッダからスキーム付きで導出する(`X-Forwarded-Proto` があればそれに従う)。上流が返す Host はプロキシから見た上流のホストにすぎず、Jellyfin から到達できるとは限らないため、書き換えの際は上流のホスト部分を無視してパスだけで判定する。

## 5. ストリーム処理

1. `{id}` を検証し、`serviceId = id % 100000` を計算する。`MAX_STREAMS` 超過なら 503。
2. 上流 `GET {MIRAKURUN_URL}/api/services/{id}/stream[?query]` を、リクエストの context 付きで開く。接続不可なら 502、上流が非2xxならそのステータスを返す。
3. `tsreadex -n <serviceId> <TSREADEX_ARGS> -` を `exec` する(シェルを介さない)。stdin は上流の body、stdout はパイプ。
4. stdout の最初のバイトを受け取るまでレスポンスヘッダは送らない。受け取ったら `200` と `Content-Type: video/MP2T` を送り、以降は読み取るたびに flush する。最初のバイトが `FIRST_BYTE_TIMEOUT`(既定15秒)以内に来なければ、プロセスを kill して 504 を返す。サービスIDが存在しない場合などの対策である。
5. 終了処理は次のとおり。
   - クライアントが切断した場合は、context のキャンセルで上流接続を閉じ、tsreadex を SIGKILL して `Wait` する(ゾンビを残さない)。
   - 上流が終了した場合は、tsreadex の stdin が閉じられて tsreadex が終了し、レスポンスも終わる。
   - tsreadex が異常終了した場合は、出力開始前なら 502、開始後は接続を切る。
6. tsreadex の stderr は、行単位でログに出力する。

## 6. 設定

起動時のみ。環境変数を主とし、同名のフラグでも指定できる。

| 変数 | 既定値 | 説明 |
|---|---|---|
| `MIRAKURUN_URL` | (必須) | 例: `http://mirakurun:40772`。mirakc もMirakurun互換APIなので同じ変数で指定する |
| `LISTEN_ADDR` | `:8080` | |
| `PUBLIC_BASE_URL` | (空) | M3Uに書き込む自身のURL。空ならリクエストから導出 |
| `TSREADEX_PATH` | `tsreadex` | |
| `TSREADEX_ARGS` | `-x 18/38/39 -b 1 -c 1 -u 2 -d 13` | 空白区切り。`-n` はプロキシが付与する |
| `MAX_STREAMS` | `8` | 同時ストリーム数の上限 |
| `FIRST_BYTE_TIMEOUT` | `15s` | tsreadex の出力開始待ち |

- `TSREADEX_ARGS` に `-n`、`-z`、`-s`、`-r` と、末尾の `src` に相当する位置引数が含まれていたら、起動時にエラーで終了する。
- 起動時に `TSREADEX_PATH` の実行可否を確認し、失敗したら終了する(fail fast)。

## 7. エラー処理(まとめ)

| 事象 | 挙動 |
|---|---|
| `{id}` が数字列でない | 400 |
| `MAX_STREAMS` 超過 | 503 |
| 上流に接続できない | 502 |
| 上流が非2xx | そのステータス |
| 出力開始前に tsreadex が終了 | 502 |
| `FIRST_BYTE_TIMEOUT` 超過 | 504、プロセスを kill |
| 出力開始後の tsreadex 異常終了 | 接続を切る |
| クライアント切断 | 上流を閉じ、tsreadex を kill して回収 |

## 8. 構成(Go)

モジュール `github.com/onodai145/tsreadex-proxy`。標準ライブラリのみを使う。

```
cmd/tsreadex-proxy/main.go   設定読み込み、サーバー起動
internal/config/             環境変数・フラグの読み込みと検証
internal/playlist/           M3Uの書き換え(純粋関数)
internal/stream/             上流取得+tsreadex実行+配信(プロセス管理)
internal/server/             ルーティングとハンドラ
```

`playlist` と `config` は副作用のない関数で構成する。`stream` は、実行ファイルのパスを差し替えられるようにしてテスト可能にする。

## 9. テスト

- **単体:**
  - M3U書き換え。Mirakurun形式と mirakc形式の両方をテストデータとする。
  - `TSREADEX_ARGS` のパースと禁止オプションの拒否。
  - `{id}` の検証とserviceIdの計算。
- **統合(自動):**
  - 偽の上流HTTPサーバーが固定のTSバイト列を返す。
  - tsreadex の代わりに、標準入力をそのまま出力するスタブを使う。
  - 検証項目は、バイト列がそのまま通ること、切断で子プロセスが終了すること、上流5xxで 502 になること、`FIRST_BYTE_TIMEOUT` で 504 になること、`MAX_STREAMS` で 503 になること。
- **実機(手順化のみ。自動化しない):**
  - 実際の Mirakurun、tsreadex、Jellyfin を使い、映像・音声・字幕の表示を確認する。
  - 手順は README に書く。§2 の未確認項目をここで潰す。

## 10. 配布

- Docker のマルチステージビルドを使う。
  1. `golang` イメージで静的ビルドする。
  2. tsreadex は、タグ(既定 `master-260428`)を `ARG` で指定してソースを取得し、`make` でビルドする。
  3. 最終イメージには、2つのバイナリのみを入れる。
- `docker-compose.yml` のサンプルを同梱する(Jellyfin と同じネットワークに置く)。

## 11. リスク

- Jellyfin の選局プローブが、`-n` の探索待ちにより遅れ、失敗する可能性がある。実機で確認する。
- 上流の playlist 形式が将来変わると、書き換えが一致しなくなる。一致しない行は素通しにして、壊れないようにしている。
- tsreadex の `-d` の `+4`/`+8` は、ffmpeg のバージョン依存である。Jellyfin 同梱の ffmpeg のバージョンで確認する。
