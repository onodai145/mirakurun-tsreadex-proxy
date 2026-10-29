# AGENTS.md

tsreadex を HTTP プロキシとして包み、Jellyfin の Live TV から Mirakurun / mirakc のライブ視聴を安定化する Go 製サービスです。概要・設定・セキュリティの前提は [`README.md`](README.md)、設計は [`docs/superpowers/specs/2026-10-29-tsreadex-proxy-design.md`](docs/superpowers/specs/2026-10-29-tsreadex-proxy-design.md) を参照してください。

## コマンド

```bash
gofmt -l .                                      # 出力があれば未整形(CI で検査される)
go vet ./...
go test ./... -race
go build -o mirakurun-tsreadex-proxy ./cmd/mirakurun-tsreadex-proxy
docker build -t mirakurun-tsreadex-proxy .
```

- テストは tsreadex の代わりにシェルスクリプトのスタブを使うため、実際の tsreadex は不要です。Linux 専用です。
- 実機での確認項目は README の「実機での確認手順」にあります。自動化できないため、リリースのタグを打つ前に人が実施します。

## 構成

- `cmd/mirakurun-tsreadex-proxy`: エントリポイント
- `internal/config`: 環境変数とフラグの解釈
- `internal/access`: 送信元 IP と `Origin` / `Referer` / `Host` の検証、待ち受けアドレスの決定
- `internal/server`: HTTP ハンドラ(`/playlist.m3u`、`/xmltv`、`/logo/{id}`、`/stream/{id}`)
- `internal/playlist`: 上流 M3U の URL 書き換え
- `internal/stream`: tsreadex プロセスの起動と管理、サービス ID の検証(`ParseID`。`/logo/{id}` でも使う)
- `internal/upstream`: Mirakurun / mirakc へのリクエストに共通する処理(HTTP クライアントの生成、URL の組み立て、上流ステータスの変換)
- `internal/tsprobe`: debug のときだけ、上流の TS を覗いて ADTS 音声がデュアルモノかを推定する(結果はログに出す)
- `internal/testutil`: テスト用の共通処理

## 守ること

- 認証と TLS はなく、信頼された LAN 内専用というセキュリティ前提です。許可範囲外のアドレスで待ち受けない、公開するエンドポイントを増やさない、といった制約を緩める変更は、README の「セキュリティ」節と設計ドキュメントを読んだうえで行ってください。送信元の判定と公開 URL の導出で `X-Forwarded-*` を参照しないのも、意図した仕様です。
- 設定項目を追加・変更したら、README の設定表も更新します。
- コミットメッセージは [Conventional Commits](https://www.conventionalcommits.org/ja/v1.0.0/) に従います(`feat:` / `fix:` / `docs:` / `ci:` など)。説明は日本語で、関連する Issue があれば `(#14)` の形で番号を添えます。破壊的変更は `feat!:` のように `!` を付けるか、フッターに `BREAKING CHANGE:` を書きます。
- バージョンは [Semantic Versioning](https://semver.org/lang/ja/) に従い、`v1.2.3` の形のタグで管理します。リリースの手順は README の「CI とリリース」を参照してください。
