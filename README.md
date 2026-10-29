# tsreadex-proxy

[tsreadex](https://github.com/xtne6f/tsreadex) を HTTP プロキシとして包み、Jellyfin の Live TV から Mirakurun / mirakc のライブ視聴を、副音声・字幕が安定化された状態で見られるようにします。

```
Jellyfin ──▶ tsreadex-proxy ──▶ Mirakurun / mirakc
              (tsreadex を通す)
```

- 上流の `/api/iptv/playlist` を取得し、URL をこのプロキシ宛に書き換えて返します(`/playlist.m3u`)。
- 番組表(`/xmltv`)とロゴ(`/logo/{id}`)は中継します。
- ストリーム(`/stream/{id}`)は、接続ごとに `tsreadex -n <serviceId> ...` を 1 プロセス起動して返します。

設計の詳細は [`docs/superpowers/specs/2026-10-29-tsreadex-proxy-design.md`](docs/superpowers/specs/2026-10-29-tsreadex-proxy-design.md) を参照してください。

## 使い方(Docker)

1. `docker-compose.example.yml` を参考に、Jellyfin と Mirakurun が参加しているネットワークにプロキシを追加します。
2. `MIRAKURUN_URL` と `ALLOWED_HOSTNAME`(Jellyfin から見たこのコンテナのサービス名)を設定します。
3. Jellyfin の Live TV 設定で、次の 2 つを登録します(画面の名称は Jellyfin のバージョンで異なります)。
   - チューナー: 種類は **M3U Tuner**、URL は `http://tsreadex-proxy:8080/playlist.m3u`
   - 番組表: 種類は **XMLTV**、URL は `http://tsreadex-proxy:8080/xmltv`

## 設定

環境変数が主です。フラグは環境変数名を小文字のケバブケースにしたものです(例: `MIRAKURUN_URL` → `-mirakurun-url`)。フラグは環境変数より優先されます。

環境変数に空文字列を設定した場合は未設定として扱われ、既定値が使われます。空の値を指定したいときは、フラグでのみ指定できます(例: `-allow-origins ""`)。

| 環境変数 | 既定値 | 説明 |
|---|---|---|
| `MIRAKURUN_URL` | (必須) | 例: `http://mirakurun:40772`。mirakc も Mirakurun 互換 API なので同じ変数で指定します。`http://` のみ使えます(Docker イメージには CA 証明書が入っていないため、`https://` の上流は証明書の検証に失敗します) |
| `LISTEN_PORT` | `8080` | 待ち受けポート。待ち受けアドレスは設定できず、許可範囲のアドレスから自動で決まります |
| `PUBLIC_BASE_URL` | (空) | M3U に書き込むこのプロキシの URL。空ならリクエストの Host から導出します |
| `TSREADEX_PATH` | `tsreadex` | tsreadex の実行ファイル名またはパス。起動時に実行できるかを確認し、できなければ終了します(Docker イメージでは `/usr/local/bin/tsreadex`) |
| `TSREADEX_ARGS` | `-x 18/38/39 -b 1 -c 1 -u 2 -d 13` | 空白区切り。`-n`、`-z`、`-s`、`-r` と位置引数は指定できません(`-n` はプロキシが付与します) |
| `MAX_STREAMS` | `8` | 同時ストリーム数の上限(超えると 503) |
| `FIRST_BYTE_TIMEOUT` | `15s` | tsreadex の出力開始待ち(超えると 504) |
| `ALLOW_IPV4_CIDR_RANGES` | `10.0.0.0/8,127.0.0.0/8,172.16.0.0/12,192.168.0.0/16` | 許可する送信元 IPv4(Mirakurun の既定と同じ) |
| `ALLOW_IPV6_CIDR_RANGES` | `fc00::/7` | 許可する送信元 IPv6(Mirakurun の既定と同じ) |
| `ALLOW_ORIGINS` | (空) | `Origin` / `Referer` を追加で許可するオリジン(カンマ区切り)。`Origin` は完全一致、`Referer` はオリジンの境界での前方一致です(`https://app.example` は `https://app.example/x` には一致しますが、`https://app.example.evil.com/` には一致しません) |
| `ALLOWED_HOSTNAME` | (空) | クライアントがこのプロキシに到達する際のホスト名。`localhost` と許可範囲の IP は常に許可されます |

## セキュリティ

セキュリティ水準は Mirakurun と同等です。認証と TLS はなく、**信頼された LAN 内専用**です。

- 許可範囲外の送信元は、応答せずに接続を閉じます。許可範囲のアドレスでのみ待ち受け、`0.0.0.0` では待ち受けません。パブリック IP を持つサーバーで動かしても、そのアドレスでは待ち受けません。`ALLOW_*_CIDR_RANGES` にパブリック IP の範囲を入れないでください。
- `Origin`、`Referer`、`Host` が許可されていなければ 403 です(DNS Rebinding / CSRF 対策)。`ALLOWED_HOSTNAME` を設定し忘れると、Jellyfin がサービス名でアクセスしたときに 403 になります。ログに拒否理由が出ます。
- 公開するのは 4 つのエンドポイントだけです。Mirakurun の管理 API には到達できません。
- **リバースプロキシの下に置かないでください。** Mirakurun のドキュメントのとおり、リバースプロキシ(特に HTTPS 化)はブラウザや Mirakurun の保護を迂回する可能性があり、危険度が増します。リバースプロキシの下では送信元アドレスがプロキシのものになり、IP 制限も意味を失います。外部から使う場合は、VPN や SSH トンネルを使ってください。
- トンネルサービスを使う場合は、送信元アドレスがトンネルのものになるため、`ALLOW_*_CIDR_RANGES` の設定が必要になることがあります。その場合は、第三者がアクセスできないよう、慎重に設定してください。
- **このプロキシは、Jellyfin と共有するネットワークの内側に置き、ポートをホストや LAN に公開しないでください**(`ports:` / `-p` / `-P`)。`docker-compose.example.yml` に `ports` がないのはこのためです。Mirakurun の公式 compose もホストネットワークモード(`network_mode: host`)でポートを公開しない構成で、ブリッジ + ポート公開にした Mirakurun にも同じ注意が当てはまりえます。ポートを公開すると、環境(userland proxy、IPv6、rootless、Docker Desktop、localhost 経由のアクセスなど)によっては、プロキシから見える送信元アドレスが Docker のブリッジのゲートウェイ(例: `172.17.0.1`)に書き換えられることがあります。このアドレスは既定の許可範囲(`172.16.0.0/12`)の内側なので、送信元 IP の制限が意味を失います。どうしてもポートを公開する場合は、コンテナの外側(ホストのファイアウォールなど)で到達できる相手を制限し、`ALLOW_*_CIDR_RANGES` もそれに合わせて絞ってください。
- Mirakurun 側の `allowIPv4CidrRanges` を既定から絞っている場合は、このプロキシの送信元アドレスも許可に含めてください。
- tsreadex は C++ の TS パーサで、上流(放送波由来)の入力を処理します。メモリ安全性の問題がありうるため、最新版に追従してください。

## トラブルシューティング

- **起動しても待ち受けない**(ログに `no permitted non-loopback IPv4 address yet; retrying` が繰り返し出る): 許可範囲(`ALLOW_IPV4_CIDR_RANGES`)に入る、ループバック以外の IPv4 アドレスが、このホスト(コンテナ)にありません。Docker のネットワークのサブネットが許可範囲(既定は `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`)の外にあると起こります。そのサブネットを `ALLOW_IPV4_CIDR_RANGES` に加えてください。パブリック IP の範囲は加えないでください。

## ビルド

```bash
go build -o tsreadex-proxy ./cmd/tsreadex-proxy
go test ./... -race
docker build -t tsreadex-proxy .   # tsreadex は ARG TSREADEX_REF のタグからビルドされます
```

テストは tsreadex の代わりにシェルスクリプトのスタブを使うため、実際の tsreadex は不要です。ただし Linux 専用です(`/bin/sh`、`cat`、`sleep`、`head`、`tr`、`syscall.Kill`、ループバックの `127.0.0.2` を使います)。

## 実機での確認手順

自動テストでは確認できない項目です。初回の導入時に確認してください。

- [ ] Jellyfin でチャンネルを選局して、映像と音声が出る。選局から再生開始までの時間を記録する(Jellyfin のプローブがタイムアウトしないか)。
- [ ] 副音声のある番組で音声が切り替えられる。字幕のある番組で字幕が出る(`-d 13` が Jellyfin 同梱の ffmpeg で動くか)。
- [ ] チャンネルロゴが Jellyfin に表示される(M3U の `tvg-logo` に従うか)。
- [ ] 視聴中にクライアントを閉じると、`ps` から tsreadex プロセスが消え、Mirakurun のチューナーが解放される。
- [ ] 視聴を止めたあと、上流が止まる・tsreadex が出力しなくなる場合にプロセスが残らないか確認する。残る場合は `TSREADEX_ARGS` に `-m 2 -t 10` を加える効果を確認し、必要なら出力アイドルタイムアウトの追加を検討する(スペック §11)。
- [ ] tsreadex が入力終了で終了コード 0 になること(非 0 だと、ストリームの正常な終了が接続断として扱われます)。
- [ ] `ALLOWED_HOSTNAME` を外すと、Jellyfin からのアクセスが 403 になり、ログに `reason=host` が出る。
- [ ] 許可範囲外のホストから接続すると、応答なしで切断される(許可範囲内のホストからは応答する)。
- [ ] (ポートを公開する構成を試す場合のみ。非推奨)ポートを公開した状態で、許可範囲外のホストと許可範囲内のホストからそれぞれ接続し、プロキシのログの `remote=` に実際のクライアントのアドレスが出るか比べる。`remote=` がブリッジのゲートウェイのアドレスになっていれば、その構成では送信元 IP の制限は効いていない(スペック §11)。
- [ ] 許可範囲外のアドレスで待ち受けていないこと(`ss -ltn` で待ち受けアドレスを確認する)。
