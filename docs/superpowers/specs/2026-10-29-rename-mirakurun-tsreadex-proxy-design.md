# mirakurun-tsreadex-proxy への改名 設計

作成日: 2026-10-29 / 状態: ドラフト(ユーザーレビュー待ち)

## 1. 目的

プロジェクト名を `tsreadex-proxy` から `mirakurun-tsreadex-proxy` に改める。

現在の名前には次の問題がある。

- tsreadex は実装の詳細で、利用者が得るもの(Jellyfin から Mirakurun / mirakc のライブ視聴を安定して使えること)を表していない。
- tsreadex 自体は HTTP サーバーではなく stdin から stdout への TS フィルタである。「tsreadex の proxy」と読むと、役割の向きが逆に読める。実際は Mirakurun の前に立ち、ストリームごとに tsreadex を通すサービスである。
- Mirakurun / mirakc が名前に出ない。

新しい名前は「Mirakurun の前に置き、tsreadex を通す proxy」という実態を表す。mirakc は Mirakurun 互換 API として使えるため、名前には含めず、README で明記する。

### 成功基準

- `gofmt -l .` が出力なし。`go vet ./...`、`go test ./... -race`、`docker build` が通る。
- リポジトリ内の旧名 `tsreadex-proxy` の参照が、§4 の「意図して残すもの」だけになる。
- 振る舞い(環境変数、フラグ、エンドポイント、ログの内容)が名前以外変わらない。

### 非目的(YAGNI)

- 機能の追加・変更、設定項目の変更。
- 旧イメージ `ghcr.io/onodai145/tsreadex-proxy` の削除や、旧名から新名への自動転送。
- 既存の spec と plan(`docs/superpowers/` 配下)の書き換え。

## 2. 決定事項

| 項目 | 決定 | 理由 |
|---|---|---|
| 新名称 | `mirakurun-tsreadex-proxy` | 上流と手段の両方が名前から読める |
| 範囲 | リポジトリ、Go モジュール、`cmd/`、バイナリ、イメージを一括で改名する(1 つの PR) | 一部だけ改名すると、リポジトリ名とモジュールパスが食い違い、将来 `go install` で混乱する |
| 旧イメージ | 削除せず残す。README で新イメージへ案内する | 既存の利用者の `docker pull` を壊さない |
| バージョン | `v0.2.0` | イメージ名が変わる破壊的変更。0.x なので minor を上げる |
| 既存の spec / plan | ファイル名も本文も変えない。注記も入れない | 当時の設計記録として正確なまま残す。改名の経緯はこの文書に残す |
| compose 例のサービス名 | 新名称に揃える | 例とイメージ名を一致させる |
| GitHub のリポジトリ名の変更 | 利用者が行う | 外向きで戻しにくい操作のため |

## 3. 変更対象

### 改名する

| 対象 | 変更 |
|---|---|
| `go.mod` | `module github.com/onodai145/mirakurun-tsreadex-proxy` |
| `cmd/`、`internal/` の全 import | 新しいモジュールパスに置換する |
| `cmd/tsreadex-proxy/` | `git mv` で `cmd/mirakurun-tsreadex-proxy/` |
| `cmd/.../main.go` | package コメントとエラープレフィックス |
| `internal/config/config.go` | `flag.NewFlagSet` の名前 |
| `Dockerfile` | ビルド出力のパス、`/usr/local/bin/` のバイナリ名、ライセンスのディレクトリ、`ENTRYPOINT` |
| `.gitignore`、`.dockerignore` | ビルド成果物のパス |
| `.github/workflows/ci.yml` | `docker build` のイメージタグ(`tsreadex-proxy:ci`) |
| `.github/workflows/release.yml` | `REGISTRY_IMAGE` |
| `docker-compose.example.yml` | サービス名、`ALLOWED_HOSTNAME`、イメージ名、タグを `0.2.0` に |
| `README.md` | タイトル、図、URL の例、イメージ名、ビルドのコマンド例 |
| `AGENTS.md` | 概要、コマンド、構成の `cmd/` |

### 変えない

- 環境変数、フラグ、エンドポイント(`/playlist.m3u`、`/xmltv`、`/logo/{id}`、`/stream/{id}`)。
- 外部ツール tsreadex を指す記述。`/usr/local/bin/tsreadex`、`TSREADEX_*`、`ARG TSREADEX_REF`、tsreadex のリポジトリへのリンクなど。
- `docs/superpowers/` 配下の既存ファイル。

## 4. 意図して残す旧名

`grep -r "tsreadex-proxy"` で次だけが残る。

- `docs/superpowers/` 配下の既存の spec と plan(ファイル名と本文)、およびこの文書。
- README と AGENTS.md にある、既存 spec へのリンク(パスが旧名のため)。
- README の「旧名称からの移行」節にある旧イメージ名。

## 5. README に追加する文章

- 冒頭の説明に、mirakc も Mirakurun 互換 API として使えることを一行加える。
- 「旧名称からの移行」節を設ける。
  - 旧イメージ `ghcr.io/onodai145/tsreadex-proxy` は `v0.1.x` までで、今後は更新されない。
  - 新しいイメージ `ghcr.io/onodai145/mirakurun-tsreadex-proxy` のタグ `0.2.0` 以降に切り替える。
  - compose のサービス名を変えた場合は、`ALLOWED_HOSTNAME` と Jellyfin に登録した URL も合わせる。

## 6. 検証

- 自動: `gofmt -l .`(出力なし)、`go vet ./...`、`go test ./... -race`、`go build -o mirakurun-tsreadex-proxy ./cmd/mirakurun-tsreadex-proxy`、`docker build -t mirakurun-tsreadex-proxy .`。CI の `docker build` とスモークテストも通ること。
- `grep -rIn "tsreadex-proxy" . --exclude-dir=.git` の結果が、§4 の範囲に収まること。
- 手動: README の「実機での確認手順」(自動化できないため、タグを打つ前に人が実施する)。

## 7. リリースと手動作業

順序は次のとおり。

1. 利用者が GitHub のリポジトリ名を `mirakurun-tsreadex-proxy` に変更する(旧 URL はリダイレクトされる)。
2. PR をマージする。
3. 実機確認をする。
4. `git tag v0.2.0 && git push origin v0.2.0`。`release.yml` が新イメージを公開し、GitHub のリリースページを作る。
5. 新しい GHCR パッケージは非公開で始まるため、パッケージの設定で可視性を Public にする。

注意:

- タグを打つまで、compose 例のイメージ `0.2.0` は存在しない。
- 手順 1 より前に PR をマージすると、モジュールパスとリポジトリ名が一時的に食い違う。ビルドとテストには影響しないが、順序は守る。

## 8. リスク

| リスク | 対処 |
|---|---|
| 置換漏れ | §6 の `grep` で確認する。CI の `docker build` とスモークテストで、バイナリ名の不整合を検出する |
| 外部ツール tsreadex の記述を誤って置換する | 置換は `tsreadex-proxy` という文字列に限る。`tsreadex` 単体は触らない |
| 既存の利用者が旧イメージで止まる | 旧イメージは残す。README の移行節で案内する |
| 新パッケージが非公開のまま | §7 の手順 5 で明示する |
