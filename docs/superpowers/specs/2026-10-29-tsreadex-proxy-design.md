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

認証、TLS、メトリクス、設定のホットリロード、クエリによる tsreadex オプションの上書き、HDHomeRun互換API、録画ファイルの変換、複数クライアント間での上流共有。セキュリティ水準は Mirakurun と同等とする(認証・TLSなし、信頼されたLAN内での運用を前提とし、Mirakurun の「セキュリティ上の留意事項」に準拠したアクセス制御を実装する。詳細は §12)。

## 2. 前提と確認済みの事実

| 事実 | 出典 |
|---|---|
| Mirakurun の `/api/iptv/playlist` は `http(s)://{Host}/api/services/{id}/stream` を出力し、先頭行に `url-tvg="…/api/iptv/xmltv"`、ロゴに `tvg-logo="…/api/services/{id}/logo"` を持つ。`tvg-id` は `{id}` | Chinachu/Mirakurun `src/Mirakurun/api/iptv/playlist.ts` |
| mirakc の `/api/iptv/playlist` は `http://{host}/api/services/{id}/stream?{query}` を出力する。`url-tvg` は出さず、ストリームURLにクエリが付く | mirakc/mirakc `mirakc-core/src/web/api/iptv.rs` |
| `{id}` は `networkId*100000 + serviceId`(Mirakurun)。tsreadex の `-n` に渡すのは serviceId | Mirakurun の仕様(実装時に実機で再確認) |
| tsreadex は依存のない C++11。`make` 一発でビルドできる。最新タグは `master-260428`。リリース成果物は zip のみ | xtne6f/tsreadex `Makefile`, tags |
| `-n` が 0 のとき `-a/-b/-c/-u` は無視される | tsreadex Readme |
| 入力がパイプのとき、`-m 0` では `-t 0` でなければならず、`-m 2` では `-t` が非0でなければならない | tsreadex Readme |
| Mirakurun は LAN 内専用で、既定で送信元をプライベートIPのみに制限する(`allowIPv4CidrRanges` 等)。DNS Rebinding / CSRF 対策として `hostname` と `allowOrigins` を持つ。リバースプロキシの下に置くこと(特にHTTPS化)は保護をバイパスして危険度が増すため非推奨で、代わりにVPNやSSHトンネルを使う | Mirakurun `doc/Configuration.ja.md`「セキュリティ上の留意事項」 |
| Mirakurun の入口ミドルウェアは、(1) 接続元IPが許可範囲外なら応答せず `socket.end()`、(2) `Origin` があり、ホスト名が `localhost`・設定の `hostname`・許可範囲のIPのいずれでもなく `allowOrigins` にもなければ 403、(3) `Referer` も同様(`allowOrigins` は前方一致)、の順で判定する。`Host` ヘッダは検証しない。CORS は `cors()`(`*`)を返す | Mirakurun `src/Mirakurun/Server.ts`, `system.ts` |

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

全エンドポイント(`HEAD` を含む)は、ルーティングの前に §12 のアクセス制御ミドルウェアを通る。

`{id}` は10進数の数字列のみ受け付け、10桁以内で `ParseUint` できること、かつ `serviceId = id % 100000` が 1〜65535 であることを検証する(満たさなければ 400)。`serviceId` が 0 になると `-n 0` となり、サービス選択が無効になって `-b/-c/-u/-d` が無視された素通しになるため、これを防ぐ。上流のホストとパスはプロキシ側で固定で、クライアントが指定できるのは `{id}` とクエリ文字列だけである。

## 4. M3U の書き換え

上流のM3Uを行単位で処理する。

- 行頭 `#EXTM3U` の `url-tvg="…"` は、プロキシの `{PUBLIC_BASE}/xmltv` に置き換える。属性がない場合(mirakc)は追加しない。
- `tvg-logo="…/api/services/{id}/logo"` は `{PUBLIC_BASE}/logo/{id}` に置き換える。
- URL行 `http(s)://…/api/services/{id}/stream[?query]` は `{PUBLIC_BASE}/stream/{id}[?query]` に置き換える。
- 上記に一致しない行は変更せずそのまま出力する。

M3U は全体を読み込んでから書き換えるため、上流の読み込みに上限(`io.LimitReader`、既定 8MiB)を設ける。上限を超えたら 502 にする。

`{PUBLIC_BASE}` は `PUBLIC_BASE_URL` があればそれ、なければリクエストの Host ヘッダとリクエスト自身のスキームから導出する(Mirakurun の playlist も `req.headers.host` から導出する)。導出に使う Host は §12 の検証を通過したものに限る(Mirakurun は Host を検証しないため、本プロキシのほうがやや厳格である)。`X-Forwarded-*` ヘッダは信頼しない。上流が返す Host はプロキシから見た上流のホストにすぎず、Jellyfin から到達できるとは限らないため、書き換えの際は上流のホスト部分を無視してパスだけで判定する。

## 5. ストリーム処理

1. `{id}` を検証し、`serviceId = id % 100000` を計算する。`MAX_STREAMS` 超過なら 503。
2. 上流 `GET {MIRAKURUN_URL}/api/services/{id}/stream[?query]` を、リクエストの context 付きで開く。クライアントのリクエストヘッダは転送せず、リダイレクトは追従しない。接続不可なら 502、上流が非2xxならそのステータスのみを返す(上流のレスポンス本文は返さない)。
3. `tsreadex -n <serviceId> <TSREADEX_ARGS> -` を `exec` する(シェルを介さない)。環境変数は最小限(`PATH` 程度)だけ渡す。stdin は上流の body、stdout はパイプ。
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
| `ALLOW_IPV4_CIDR_RANGES` | `10.0.0.0/8,127.0.0.0/8,172.16.0.0/12,192.168.0.0/16` | 許可する送信元IPv4(Mirakurun の既定と同じ。カンマ区切り) |
| `ALLOW_IPV6_CIDR_RANGES` | `fc00::/7` | 許可する送信元IPv6(Mirakurun の既定と同じ) |
| `ALLOW_ORIGINS` | (空) | `Origin`/`Referer` を追加で許可するオリジン(カンマ区切り) |
| `ALLOWED_HOSTNAME` | (空) | Jellyfin などから到達する際のホスト名(例: `tsreadex-proxy`)。`localhost` と許可範囲のIPアドレスは常に許可する |

- `TSREADEX_ARGS` に `-n`、`-z`、`-s`、`-r` と、末尾の `src` に相当する位置引数が含まれていたら、起動時にエラーで終了する。
- 起動時に `TSREADEX_PATH` の実行可否を確認し、失敗したら終了する(fail fast)。

## 7. エラー処理(まとめ)

| 事象 | 挙動 |
|---|---|
| 送信元IPが許可範囲外 | 応答せず接続を閉じる(Mirakurun と同じ) |
| `Origin`/`Referer`/`Host` が不許可 | 403 |
| `{id}` が数字列でない、または `serviceId` が範囲外 | 400 |
| `MAX_STREAMS` 超過 | 503 |
| 上流に接続できない | 502 |
| 上流が非2xx | そのステータスのみ(上流の本文や `MIRAKURUN_URL` はクライアントに返さない) |
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

加えて `internal/access/`(送信元IP・Origin・Referer・Host の検証ミドルウェア)を置く。`playlist`、`config`、`access` の判定部は副作用のない関数で構成する。`stream` は、実行ファイルのパスを差し替えられるようにしてテスト可能にする。

## 9. テスト

- **単体:**
  - M3U書き換え。Mirakurun形式と mirakc形式の両方をテストデータとする。
  - `TSREADEX_ARGS` のパースと禁止オプションの拒否。
  - `{id}` の検証とserviceIdの計算。
  - アクセス制御。CIDR判定(IPv4射影IPv6 `::ffff:a.b.c.d` を含む)、`Origin`/`Referer`/`Host` の許可・拒否の組み合わせ(`localhost`、`ALLOWED_HOSTNAME`、許可範囲のIP、`ALLOW_ORIGINS`、不正な値)、許可範囲外IPで応答なしに閉じられること。
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
  3. 最終イメージには、2つのバイナリのみを入れ、非 root ユーザーで実行する。
- `docker-compose.yml` のサンプルを同梱する。Jellyfin と共有する内部ネットワークに置き、プロキシの `ports` は公開しない(ホストから直接見たい場合のみ利用者が追加する)。サンプルでは `ALLOWED_HOSTNAME` に、Jellyfin から到達するサービス名を設定する。Mirakurun 側の `allowIPv4CidrRanges` を既定から絞っている場合は、プロキシの送信元アドレスも許可に含める必要がある(README に明記する)。

## 11. リスク

- Jellyfin の選局プローブが、`-n` の探索待ちにより遅れ、失敗する可能性がある。実機で確認する。
- 上流の playlist 形式が将来変わると、書き換えが一致しなくなる。一致しない行は素通しにして、壊れないようにしている。
- `ALLOWED_HOSTNAME` を設定し忘れると、Jellyfin がサービス名でアクセスしたときに Host 検証で 403 になる。compose サンプルと README で明示し、403 のログに拒否理由(どのヘッダか)を出す。
- Docker の構成によっては、プロキシから見た送信元アドレスが変換されて、IP制限が意図どおりに働かない可能性がある(例: ポート公開時の転送方式による。未確認)。実機で `X-Forwarded-For` ではなく `RemoteAddr` が実際のクライアントのアドレスになるかを確認する。
- tsreadex の `-d` の `+4`/`+8` は、ffmpeg のバージョン依存である。Jellyfin 同梱の ffmpeg のバージョンで確認する。
- 出力開始後に上流または tsreadex が無出力で止まった場合、プロセスとチューナーが占有され続ける可能性がある。`FIRST_BYTE_TIMEOUT` は開始前のみを対象とする。実機検証で `-m 2 -t N` の効果を確認し、必要なら出力アイドルタイムアウトを別途追加する(§2 の未確認項目と同じ検証で扱う)。

## 12. セキュリティ方針

### 12.1 基準

Mirakurun の「セキュリティ上の留意事項」(`doc/Configuration.ja.md`)と実装(`src/Mirakurun/Server.ts`、`system.ts`)に準拠する。Mirakurun は LAN 内専用サーバーで、認証・TLS は持たず、(a) 送信元IP制限、(b) DNS Rebinding / CSRF 対策、(c) リバースプロキシ非推奨、の組み合わせでリスクを緩和している。本プロキシも同じ前提と同じ緩和を取る。

プロキシは Mirakurun の許可範囲の内側にいる。プロキシが同じ制限を持たなければ、LAN 外から Mirakurun のストリームへ至る経路になりうる。プロキシが Mirakurun の保護を迂回する経路にならないことを、最優先の基準とする。

### 12.2 アクセス制御ミドルウェア

全エンドポイント(`HEAD` を含む)に対し、ルーティングの前に、次の順で判定する。1〜3 は Mirakurun の `Server.ts` と同じ順序・同じ判定である。

1. **送信元IP:** 接続元アドレス(`RemoteAddr`)を `ALLOW_IPV4_CIDR_RANGES` / `ALLOW_IPV6_CIDR_RANGES` と照合する。IPv4射影IPv6(`::ffff:a.b.c.d`)はIPv4に戻して判定する。許可範囲外なら、応答せず接続を閉じる。`X-Forwarded-For` 等は見ない。
2. **Origin:** ヘッダがあれば、ホスト名が `localhost`・`ALLOWED_HOSTNAME`・許可範囲のIPアドレスのいずれかであるか、`ALLOW_ORIGINS` に完全一致する場合のみ許可する。それ以外、および解釈できない値は 403。
3. **Referer:** ヘッダがあれば、2 と同様に判定する(`ALLOW_ORIGINS` は前方一致)。不許可は 403。
4. **Host(Mirakurun にない追加):** Host ヘッダのホスト名が、2 と同じ条件(`localhost`・`ALLOWED_HOSTNAME`・許可範囲のIP)を満たさなければ 403。DNS Rebinding の標準的な対策であり、Host から M3U の URL を導出する(§4)ことの安全のためである。Mirakurun よりやや厳格だが、設定を誤ると 403 になる(§11)。

CORS ヘッダは出さない。Jellyfin は M3U・XMLTV・ストリームをサーバー側から取得するため、ブラウザのクロスオリジン許可は不要である(Mirakurun は `cors()` で `*` を返すが、本プロキシはより厳格にする)。`X-Content-Type-Options: nosniff` を付ける。

アクセスログ(送信元・メソッド・URL・ステータス・所要時間・User-Agent)を標準出力に出す(Mirakurun の `morgan` と同等)。拒否した場合は、理由(どの検査か)もログに出す。

### 12.3 その他の対策

- 公開エンドポイントは §3 の allowlist のみ。上流のホストとパスは固定で、Mirakurun の管理系 API(設定変更、チューナーコマンドなど任意コード実行につながりうるもの)には到達できない。
- `{id}` と `serviceId` の厳格な検証(§3)。シェルを介さない `exec`。クライアント入力が tsreadex に渡るのは、検証済みの `serviceId` だけ。
- 上流へのリクエストヘッダ非転送、リダイレクト非追従(§5)。上流のエラー本文は返さない(§7)。M3U 読み込みの上限(§4)。
- tsreadex 子プロセスの環境変数の最小化、コンテナの非 root 実行、compose サンプルでポートを公開しない(§5、§10)。
- 同時ストリーム数の上限(`MAX_STREAMS`)と出力開始待ちのタイムアウト(§5、§6)。

### 12.4 見送る対策

Mirakurun も持たない、または本プロジェクトの範囲を超えるものは見送る。必要になったら、この節を更新して別途設計する。

- 認証・認可、TLS、レート制限
- Web UI、PNA(`allowPNA`、実験的でありブラウザ UI を持たないため)、Unix ソケット
- 許可範囲のアドレスを持つインターフェースのみでの listen(Mirakurun はこれも行う)。送信元IP制限で同等の効果が得られ、Docker ではインターフェースがプライベートアドレスのみのため、見送る
- コンテナの egress 制限、`cap_drop`/seccomp による強化、子プロセスの別ネットワーク名前空間での実行
- tsreadex 取得のコミットSHA固定とハッシュ検証
- 出力アイドルタイムアウト(§11 のとおり、実機検証後に判断)

### 12.5 運用上の前提(README に明記する)

- LAN 内専用とし、インターネットに公開しない。
- **リバースプロキシの下に置かない。** Mirakurun のドキュメントは、リバースプロキシで HTTPS 化すると、ブラウザや Mirakurun の保護(保護されたコンテキストの扱いなど)を迂回しうるため、逆に危険度が増すとしている。外部から使う場合は、VPN や SSH トンネルを使う。
- リバースプロキシの下では、`RemoteAddr` がリバースプロキシのアドレスになり、送信元IP制限が意味を失う。このため、本プロキシは `X-Forwarded-For` を信頼しない。
- トンネルサービスを使う場合は、`ALLOW_*_CIDR_RANGES` の設定が必要になりうる。その場合は、第三者がアクセスできないよう慎重に設定する。
- tsreadex は C++ の TS パーサであり、上流(放送波由来)の入力を処理する。メモリ安全性の問題がありうることを認識し、最新版に追従する。
