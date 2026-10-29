# mirakurun-tsreadex-proxy への改名 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** プロジェクト名を `tsreadex-proxy` から `mirakurun-tsreadex-proxy` に、振る舞いを変えずに改める。

**Architecture:** 名前だけを変える機械的な置換を、ビルド連鎖(Go・Dockerfile・CI)、公開物(release・compose)、ドキュメントの 3 つのコミットに分ける。各コミットの時点で、ビルドとテストが通る状態を保つ。置換は Perl の否定後読みで、二重置換と既存 spec へのリンクの破壊を防ぐ。

**Tech Stack:** Go 1.26、Docker(multi-stage)、GitHub Actions、Perl(置換)

**Spec:** `docs/superpowers/specs/2026-10-29-rename-mirakurun-tsreadex-proxy-design.md`

## Global Constraints

- 新名称は `mirakurun-tsreadex-proxy`。モジュールパスは `github.com/onodai145/mirakurun-tsreadex-proxy`、イメージは `ghcr.io/onodai145/mirakurun-tsreadex-proxy`。
- 環境変数、フラグ、エンドポイント(`/playlist.m3u`、`/xmltv`、`/logo/{id}`、`/stream/{id}`)は変えない。
- 外部ツール tsreadex を指す記述(`/usr/local/bin/tsreadex`、`TSREADEX_*`、`ARG TSREADEX_REF`、tsreadex のリポジトリへのリンク)は変えない。置換は `tsreadex-proxy` という文字列に限る。
- `docs/superpowers/` 配下の既存ファイルは、ファイル名も本文も変えない。
- 旧イメージ `ghcr.io/onodai145/tsreadex-proxy` は削除しない。
- compose 例のイメージのタグは `0.2.0`。次のリリースは `v0.2.0`。
- GitHub のリポジトリ名の変更、タグの push、GHCR パッケージの可視性の変更は、この計画に含めない(利用者が行う)。
- 作業ブランチは `refactor/rename-mirakurun-tsreadex-proxy`(作成済み)。`main` では編集しない。
- コミットメッセージは Conventional Commits、説明は日本語、件名のみで本文は書かない。トレーラー `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` は `-m` を分けて付ける。`--no-verify` と `--no-gpg-sign` は使わない。`git commit` が失敗・タイムアウトしたら、再試行せずに利用者へ報告して止まる。
- コマンドは `gofmt -l .`(出力なし)、`go vet ./...`、`go test ./... -race`、`docker build` が基準。テストは tsreadex の代わりにシェルスクリプトのスタブを使う(Linux 専用)。

## Review Focus

仕様が暗黙に求めていて、どのタスクのテストにも現れない失敗の候補。最も起きやすい順。

1. **外部ツール tsreadex の記述を巻き込む**: 置換が広すぎて `/usr/local/bin/tsreadex` が壊れると、スモークテストの前に Dockerfile が壊れる。→ Task 1 のステップ 7 で、変わってはいけない行が残っていることを確認する。
2. **二重置換**(`mirakurun-mirakurun-tsreadex-proxy`): 置換を再実行したときに起きる。→ Task 1 のステップ 6 と Task 4 で `grep` する。
3. **既存 spec へのリンクが壊れる**: README と AGENTS.md のリンクのパスは旧名のまま残す必要がある。→ Task 3 のステップ 5 でリンク先の存在を確認する。
4. **compose のサービス名と `ALLOWED_HOSTNAME` の食い違い**: 食い違うと Host 検証で 403 になり、利用者が最初につまずく。→ Task 2 のステップ 3 で一致を確認する。
5. **`gofmt` の import 順序の崩れ、旧名のビルド成果物の混入**: モジュールパスの変更で import の並びが変わりうる。`.gitignore` の更新漏れで、ビルドしたバイナリが `git status` に出る。→ Task 1 のステップ 4 と 5 で確認する。

---

### Task 1: ビルド連鎖(Go・Dockerfile・CI・ignore)の改名

**Files:**
- Move: `cmd/tsreadex-proxy/` → `cmd/mirakurun-tsreadex-proxy/`
- Modify: `go.mod`、`Dockerfile`、`.gitignore`、`.dockerignore`、`.github/workflows/ci.yml`
- Modify: `cmd/mirakurun-tsreadex-proxy/main.go`、`internal/config/config.go`、`internal/server/server.go`、`internal/stream/stream.go`
- Modify(テストの import とホスト名の値): `internal/access/middleware_test.go`、`internal/access/policy_test.go`、`internal/config/config_test.go`、`internal/server/server_test.go`、`internal/stream/stream_test.go`

**Interfaces:**
- Consumes: なし
- Produces: モジュールパス `github.com/onodai145/mirakurun-tsreadex-proxy`、バイナリ `/usr/local/bin/mirakurun-tsreadex-proxy`、CI 用イメージタグ `mirakurun-tsreadex-proxy:ci`。Task 2 と Task 3 はこれらの名前に依存する。

- [ ] **Step 1: 基準(改名前)で、ビルドとテストが通ることを確認する**

Run: `go vet ./... && go test ./... -race`
Expected: すべて `ok`。失敗するなら、改名の前に原因を調べて利用者へ報告する。

- [ ] **Step 2: 置換の対象ファイルを確認する(まだ変更しない)**

Run:
```bash
grep -rIl "tsreadex-proxy" . --exclude-dir=.git --exclude-dir=docs --exclude=README.md --exclude=AGENTS.md --exclude=release.yml --exclude=docker-compose.example.yml | sort
```
Expected(次の 14 件。`cmd` はまだ旧パス):
```
./.dockerignore
./.github/workflows/ci.yml
./.gitignore
./cmd/tsreadex-proxy/main.go
./Dockerfile
./go.mod
./internal/access/middleware_test.go
./internal/access/policy_test.go
./internal/config/config.go
./internal/config/config_test.go
./internal/server/server.go
./internal/server/server_test.go
./internal/stream/stream.go
./internal/stream/stream_test.go
```
違う一覧が出たら、置換に進まず、利用者へ報告する。

- [ ] **Step 3: ディレクトリを移動して、文字列を置換する**

Run:
```bash
git mv cmd/tsreadex-proxy cmd/mirakurun-tsreadex-proxy
grep -rIlZ "tsreadex-proxy" . --exclude-dir=.git --exclude-dir=docs --exclude=README.md --exclude=AGENTS.md --exclude=release.yml --exclude=docker-compose.example.yml \
  | xargs -0 perl -pi -e 's/(?<!mirakurun-)tsreadex-proxy/mirakurun-tsreadex-proxy/g'
```
Expected: 出力なし。`git status --short` に、リネーム(`R`)と、上の 14 ファイル相当の変更が出る。

- [ ] **Step 4: `gofmt` で import の順序を確認する**

Run: `gofmt -l .`
Expected: 出力なし。ファイル名が出たら `gofmt -w <そのファイル>` で整えて、もう一度 `gofmt -l .` を実行する。

- [ ] **Step 5: vet・テスト・ビルドを実行して、成果物が無視されることを確認する**

Run:
```bash
go vet ./... && go test ./... -race
go build -o mirakurun-tsreadex-proxy ./cmd/mirakurun-tsreadex-proxy
git status --short | grep -F 'mirakurun-tsreadex-proxy' | grep -v '^R' || echo "binary ignored"
rm -f mirakurun-tsreadex-proxy
```
Expected: テストはすべて `ok`。最後に `binary ignored` が表示される(`git status` に、バイナリが未追跡として出ない)。

- [ ] **Step 6: 二重置換が無いことを確認する**

Run: `grep -rIn "mirakurun-mirakurun" . --exclude-dir=.git || echo "no double replacement"`
Expected: `no double replacement`

- [ ] **Step 7: 外部ツール tsreadex の記述が変わっていないことを確認する**

Run:
```bash
grep -nE '/usr/local/bin/tsreadex($| )' Dockerfile .github/workflows/ci.yml
grep -n 'TSREADEX_REF' Dockerfile
```
Expected:
- `Dockerfile` に `COPY --from=tsreadex /src/tsreadex /usr/local/bin/tsreadex` がある。
- `ci.yml` に `--entrypoint /usr/local/bin/tsreadex mirakurun-tsreadex-proxy:ci` がある(イメージタグだけが新名称)。
- `Dockerfile` に `ARG TSREADEX_REF=master-260428` と `ARG TSREADEX_REF` が残っている。

- [ ] **Step 8: イメージをビルドして、スモークテストを実行する**

Docker とネットワーク(tsreadex を GitHub から clone する)が必要。使えない環境では、このステップを飛ばして、その旨を利用者へ報告する(CI が同じ検査をする)。

Run:
```bash
docker build -t mirakurun-tsreadex-proxy:ci .
out="$(docker run --rm --entrypoint /usr/local/bin/tsreadex mirakurun-tsreadex-proxy:ci 2>&1 || true)"; echo "$out"; grep -q 'not enough arguments' <<< "$out" && echo "tsreadex OK"
out="$(docker run --rm mirakurun-tsreadex-proxy:ci 2>&1 || true)"; echo "$out"; grep -q 'MIRAKURUN_URL is required' <<< "$out" && echo "proxy OK"
```
Expected: `tsreadex OK` と `proxy OK` の両方が表示される。エラーメッセージの先頭は `mirakurun-tsreadex-proxy:` になる。

- [ ] **Step 9: コミットする**

Run:
```bash
git add -A cmd go.mod Dockerfile .gitignore .dockerignore .github/workflows/ci.yml internal
git commit -m "refactor: Go モジュールとビルド連鎖を mirakurun-tsreadex-proxy に改名する" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```
Expected: コミットが作られる。

---

### Task 2: 公開物(release ワークフロー・compose 例)の改名

**Files:**
- Modify: `.github/workflows/release.yml:14`
- Modify: `docker-compose.example.yml`

**Interfaces:**
- Consumes: Task 1 が決めたイメージ名 `mirakurun-tsreadex-proxy`
- Produces: `REGISTRY_IMAGE=ghcr.io/onodai145/mirakurun-tsreadex-proxy`、compose のサービス名 `mirakurun-tsreadex-proxy`。Task 3 の README の URL の例がこのサービス名を使う。

- [ ] **Step 1: 置換して、タグを `0.2.0` に更新する**

Run:
```bash
perl -pi -e 's/(?<!mirakurun-)tsreadex-proxy/mirakurun-tsreadex-proxy/g' .github/workflows/release.yml docker-compose.example.yml
perl -pi -e 's/:0\.1\.0$/:0.2.0/; s/0\.1\.0、0\.1、latest/0.2.0、0.2、latest/' docker-compose.example.yml
```
Expected: 出力なし。

- [ ] **Step 2: 差分が意図どおりであることを確認する**

Run: `git diff -U0 .github/workflows/release.yml docker-compose.example.yml`
Expected(変更行はこの 5 行だけ):
- `release.yml`: `REGISTRY_IMAGE: ghcr.io/onodai145/mirakurun-tsreadex-proxy`
- `docker-compose.example.yml`: サービス名 `  mirakurun-tsreadex-proxy:`
- 同: コメントの `タグは 0.2.0、0.2、latest の形式`
- 同: `image: ghcr.io/onodai145/mirakurun-tsreadex-proxy:0.2.0`
- 同: `ALLOWED_HOSTNAME: mirakurun-tsreadex-proxy`

- [ ] **Step 3: サービス名と `ALLOWED_HOSTNAME` が一致していることを確認する**

Run: `grep -nE '^  mirakurun-tsreadex-proxy:$|ALLOWED_HOSTNAME: mirakurun-tsreadex-proxy$' docker-compose.example.yml`
Expected: ちょうど 2 行が出る(サービス名の行と `ALLOWED_HOSTNAME` の行)。

- [ ] **Step 4: (任意)compose の構文を確認する**

Run: `docker compose -f docker-compose.example.yml config -q && echo "compose OK"`
Expected: `compose OK`。`docker compose` が無い環境では飛ばす。

- [ ] **Step 5: コミットする**

Run:
```bash
git add .github/workflows/release.yml docker-compose.example.yml
git commit -m "feat!: コンテナイメージ名を mirakurun-tsreadex-proxy に改める" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```
Expected: コミットが作られる。

---

### Task 3: README と AGENTS.md の更新

**Files:**
- Modify: `README.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: Task 1 の `cmd/mirakurun-tsreadex-proxy`、Task 2 のサービス名とイメージ名
- Produces: なし(最終成果物)

- [ ] **Step 1: 新名称に置換する(既存 spec へのリンクは除く)**

Run:
```bash
perl -pi -e 's/(?<!2026-10-29-)(?<!mirakurun-)tsreadex-proxy/mirakurun-tsreadex-proxy/g' README.md AGENTS.md
git diff -U0 README.md AGENTS.md
```
Expected:
- 変わる行: README の見出し、図、手順 3 の 2 つの URL、イメージ名の説明、ビルドのコマンド例(2 行)。AGENTS.md のコマンド(2 行)と、構成の `cmd/mirakurun-tsreadex-proxy`。
- 変わらない行: README と AGENTS.md の、`docs/superpowers/specs/2026-10-29-tsreadex-proxy-design.md` へのリンク。

AGENTS.md の概要の文には、リンク以外にプロジェクト名が出てこない。リンク以外の変更はしない。

- [ ] **Step 2: README の冒頭に mirakc の一文を加える**

`README.md` の次の箇所を、`Edit` で置き換える。

old:
````
見られるようにします。

```
Jellyfin ──▶
````
new:
````
見られるようにします。mirakc も Mirakurun 互換 API として、同じように使えます。

```
Jellyfin ──▶
````

- [ ] **Step 3: 「旧名称からの移行」節を加える**

`README.md` の `## 設定` の直前に、次の節を挿入する(`Edit` で `## 設定\n` の前に追加)。

```markdown
## 旧名称からの移行

このプロジェクトは `v0.2.0` で、`tsreadex-proxy` から `mirakurun-tsreadex-proxy` に改名されました。

- 旧イメージ `ghcr.io/onodai145/tsreadex-proxy` は `0.1.1` までで、今後は更新されません。
- 新しいイメージ `ghcr.io/onodai145/mirakurun-tsreadex-proxy` の `0.2.0` 以降に切り替えてください。環境変数、フラグ、エンドポイントは変わりません。
- compose のサービス名を変える場合は、`ALLOWED_HOSTNAME` と、Jellyfin に登録した URL(`http://<サービス名>:8080/...`)も合わせてください。
```

- [ ] **Step 4: 差分を確認する**

Run: `git diff README.md AGENTS.md`
Expected: Step 1〜3 の変更だけが出る。ほかの行(設定表、セキュリティ、CI とリリース)は変わっていない。

- [ ] **Step 5: リンク先が存在することを確認する**

Run:
```bash
grep -ohE 'docs/superpowers/specs/[^`)]*\.md' README.md AGENTS.md | sort -u | while read -r f; do test -f "$f" && echo "ok $f" || echo "MISSING $f"; done
```
Expected: `ok docs/superpowers/specs/2026-10-29-tsreadex-proxy-design.md` のみ。`MISSING` が出たら、置換でリンクのパスが壊れている。

- [ ] **Step 6: コミットする**

Run:
```bash
git add README.md AGENTS.md
git commit -m "docs: README と AGENTS.md を新名称に合わせ、旧名称からの移行を追記する" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```
Expected: コミットが作られる。

---

### Task 4: 全体の検証と引き継ぎ

**Files:** なし(検証のみ。問題が見つかったときだけ、該当ファイルを直して追加コミットする)

**Interfaces:**
- Consumes: Task 1〜3 の成果物
- Produces: マージできる状態のブランチ

- [ ] **Step 1: 自動検査をすべて実行する**

Run:
```bash
gofmt -l .
go vet ./...
go test ./... -race
go build -o mirakurun-tsreadex-proxy ./cmd/mirakurun-tsreadex-proxy && rm -f mirakurun-tsreadex-proxy
```
Expected: `gofmt -l .` は出力なし。残りは成功する。

- [ ] **Step 2: 旧名の残りが、仕様 §4 の範囲に収まることを確認する**

Run: `grep -rInP '(?<!mirakurun-)tsreadex-proxy' . --exclude-dir=.git --exclude-dir=docs`
Expected: ヒットは次の場所だけ。
- `README.md`: 既存 spec へのリンク(`2026-10-29-tsreadex-proxy-design.md`)の行と、「旧名称からの移行」節の旧名(見出しの直後の文と、旧イメージの行)。
- `AGENTS.md`: 概要の文にある、既存 spec へのリンクの行。

これ以外のファイル・行が出たら、置換漏れとして直す。

- [ ] **Step 3: 二重置換が無いことを、リポジトリ全体で確認する**

Run: `grep -rIn "mirakurun-mirakurun" . --exclude-dir=.git || echo "no double replacement"`
Expected: `no double replacement`

- [ ] **Step 4: ブランチの状態を確認する**

Run: `git status --short && git log --oneline main..HEAD`
Expected: `git status` は出力なし。ログに 5 件(spec、plan、Task 1〜3 のコミット)が、次の順で並ぶ。
```
docs: README と AGENTS.md を新名称に合わせ、旧名称からの移行を追記する
feat!: コンテナイメージ名を mirakurun-tsreadex-proxy に改める
refactor: Go モジュールとビルド連鎖を mirakurun-tsreadex-proxy に改名する
docs: mirakurun-tsreadex-proxy への改名の実装計画を追加する
docs: mirakurun-tsreadex-proxy への改名の設計を追加する
```

- [ ] **Step 5: 利用者に引き継ぐ**

利用者へ、次の手動作業を伝える(この計画では実行しない)。

1. GitHub のリポジトリ名を `mirakurun-tsreadex-proxy` に変更する。
2. PR をマージする。
3. README の「実機での確認手順」を実施する。
4. `git tag v0.2.0 && git push origin v0.2.0`。
5. 新しい GHCR パッケージの可視性を Public にする。

ブランチの統合(PR の作成)は、`superpowers:finishing-a-development-branch` で決める。
