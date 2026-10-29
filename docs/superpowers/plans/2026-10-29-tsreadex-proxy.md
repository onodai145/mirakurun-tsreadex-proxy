# tsreadex-proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mirakurun/mirakc のライブ視聴ストリームを tsreadex に通して返す HTTP プロキシを Go で作り、Jellyfin の Live TV(M3U チューナー + XMLTV)から使えるようにする。

**Architecture:** 接続ごとに tsreadex を 1 プロセス起動するパススルー。入口で Mirakurun 準拠のアクセス制御(送信元 IP、Origin、Referer、Host)をかけ、許可範囲のアドレスでのみ待ち受ける。上流の M3U はプロキシ宛に書き換えて返し、XMLTV とロゴは中継する。

**Tech Stack:** Go(標準ライブラリのみ)、tsreadex(`master-260428`)、Docker(マルチステージ、`scratch`)。

**Spec:** `docs/superpowers/specs/2026-10-29-tsreadex-proxy-design.md`(以下「スペック」)。実装者はスペックも読むこと。

## Global Constraints

スペックから、全タスクに暗黙で適用される制約。値はスペックのとおり。

- 言語は Go、標準ライブラリのみを使う(スペック §8)。モジュール名は `github.com/onodai145/tsreadex-proxy`。`go.mod` の `go` ディレクティブは `1.23`(`http.ServeMux` のメソッド・ワイルドカードパターン、`for` のループ変数の per-iteration を使うため)。
- 設定は起動時のみ。環境変数が主で、フラグは環境変数名を小文字のケバブケースにしたもの(例: `MIRAKURUN_URL` → `-mirakurun-url`)。既定値は次のとおり(スペック §6):
  `MIRAKURUN_URL`(必須)、`LISTEN_PORT=8080`、`PUBLIC_BASE_URL=`(空)、`TSREADEX_PATH=tsreadex`、`TSREADEX_ARGS=-x 18/38/39 -b 1 -c 1 -u 2 -d 13`、`MAX_STREAMS=8`、`FIRST_BYTE_TIMEOUT=15s`、`ALLOW_IPV4_CIDR_RANGES=10.0.0.0/8,127.0.0.0/8,172.16.0.0/12,192.168.0.0/16`、`ALLOW_IPV6_CIDR_RANGES=fc00::/7`、`ALLOW_ORIGINS=`(空)、`ALLOWED_HOSTNAME=`(空)。
- `TSREADEX_ARGS` に `-n`、`-z`、`-s`、`-r`、位置引数が含まれていたら起動時にエラー終了する(スペック §6)。`-n` はプロキシが `serviceId` で付与し、末尾の `src` は常に `-`(標準入力)。
- 公開エンドポイントは `/playlist.m3u`、`/xmltv`、`/logo/{id}`、`/stream/{id}`(`HEAD` 可)のみ。上流のホストとパスは固定し、クライアントが指定できるのは `{id}` とクエリ文字列だけ(スペック §3)。クライアントのリクエストヘッダは上流へ転送しない。リダイレクトは追従しない。
- `{id}` は 10 進数の数字列で 10 桁以内、`ParseUint` でき、`serviceId = id % 100000` が 1〜65535(スペック §3)。
- 上流のエラー本文と `MIRAKURUN_URL` はクライアントに返さない。上流が 4xx/5xx ならそのステータスのみ、それ以外の非 2xx は 502(スペック §5、§7)。
- tsreadex 子プロセスには環境変数 `PATH` のみを渡す。シェルを介さず `exec` する(スペック §5)。
- コミットは機能ブランチ `feat/tsreadex-proxy` で行う。**コミットメッセージは件名のみ(本文なし)**。Co-Authored-By トレーラーは `-m` を別に指定して付ける。`--no-verify` / `--no-gpg-sign` は使わない。`git commit` が失敗したら、リトライせずに止めて報告する。
- 各タスクの最後に `gofmt -l .` が何も出力せず、`go vet ./...` が通ること。`gofmt -l .` が何か出力した場合は `gofmt -w .` で整形して再確認する(計画のコード例は手で整形しているため、コメントや表の空白の揃えがずれることがある)。

## Review Focus

スペックが暗に求めるが、タスクのテストだけでは漏れやすい入力・障害。実際に使う人が踏みやすい順。各行のテストは、担当するタスクに追加してある。

1. `{id}` の境界値(`0`、`100000`、`65536`、`+1`、`007`、11 桁、空) → Task 7 の `TestParseID`、Task 8 の `TestRouting`。`100000` は `-n 0` になり、tsreadex のフィルタが無効になる点が特に危ない。
2. 上流 M3U の改行が CRLF、末尾改行なし、空、サイズ超過 → Task 5 の `TestRewrite_*`、Task 8 の `TestPlaylist_TooLarge`。
3. クライアントが最初の出力より前に切断した場合に、tsreadex が残らない → Task 7 の `TestServe_ClientDisconnectBeforeFirstByte`。
4. tsreadex が改行なしの巨大な stderr を出し続けても、パイプが詰まって止まらない → Task 7 の `TestServe_HugeStderrDoesNotStall`。
5. `Host` ヘッダが IPv6 リテラル(`[fd00::1]:8080`)、大文字、ポート付き、`Origin: null`、`localhost/evil` のような不正な形 → Task 2 の `TestPermittedHostHeader`、`TestPermittedOrigin`。
6. 出力開始後に tsreadex が異常終了した場合は、正常終了に見せずに接続を切る → Task 7 の `TestServe_TsreadexFailureAfterOutputAbortsConnection`。

---

## File Structure

```
go.mod
.gitignore
.dockerignore
Dockerfile
docker-compose.example.yml
README.md
cmd/tsreadex-proxy/main.go          設定読み込み → 組み立て → 起動・終了処理
internal/config/config.go           環境変数・フラグの読み込みと検証、TSREADEX_ARGS の検証
internal/config/config_test.go
internal/access/policy.go           送信元IP・Origin・Referer・Host の許可判定(純粋)
internal/access/policy_test.go
internal/access/middleware.go       アクセス制御ミドルウェア、アクセスログ
internal/access/middleware_test.go
internal/access/listen.go           待ち受けアドレスの選択と listen
internal/access/listen_test.go
internal/playlist/playlist.go       M3U の書き換え、サイズ制限付き読み込み
internal/playlist/playlist_test.go
internal/upstream/upstream.go       上流 HTTP クライアント、URL 組み立て、ステータス変換
internal/upstream/upstream_test.go
internal/stream/id.go               {id} の検証と serviceId の計算
internal/stream/id_test.go
internal/stream/stream.go           上流取得 + tsreadex 実行 + 配信
internal/stream/stream_test.go
internal/server/server.go           ルーティングとハンドラ
internal/server/server_test.go
internal/testutil/testutil.go       テスト共通: 送信元アドレスを偽装するサーバー、スタブスクリプト
```

依存の向き: `main` → `server`、`stream`、`access`、`config`。`server` → `stream`、`playlist`、`upstream`。`stream` → `upstream`。`access`、`config`、`playlist`、`upstream`、`testutil` は他の内部パッケージに依存しない。

---

### Task 1: モジュールと設定(`internal/config`)

**Files:**
- Create: `go.mod`, `.gitignore`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `type Config struct { MirakurunURL *url.URL; ListenPort int; PublicBaseURL string; TsreadexPath string; TsreadexArgs []string; MaxStreams int; FirstByteTimeout time.Duration; AllowIPv4, AllowIPv6, AllowOrigins []string; AllowedHostname string }`
  - `func Load(args []string, getenv func(string) string) (*Config, error)`
  - `func ParseTsreadexArgs(s string) ([]string, error)`

- [ ] **Step 1: 機能ブランチを作り、モジュールを初期化する**

```bash
cd /home/onodai145/repos/github.com/onodai145/tsreadex-proxy
git switch -c feat/tsreadex-proxy
git status --short   # 何も出ないこと
```

`go.mod`:

```
module github.com/onodai145/tsreadex-proxy

go 1.23
```

`.gitignore`:

```
/tsreadex-proxy
/bin/
*.test
*.out
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/config/config_test.go`:

```go
package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(nil, env(map[string]string{"MIRAKURUN_URL": "http://mirakurun:40772"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MirakurunURL.String(); got != "http://mirakurun:40772" {
		t.Errorf("MirakurunURL = %q", got)
	}
	if c.ListenPort != 8080 {
		t.Errorf("ListenPort = %d", c.ListenPort)
	}
	if c.PublicBaseURL != "" {
		t.Errorf("PublicBaseURL = %q", c.PublicBaseURL)
	}
	if c.TsreadexPath != "tsreadex" {
		t.Errorf("TsreadexPath = %q", c.TsreadexPath)
	}
	wantArgs := []string{"-x", "18/38/39", "-b", "1", "-c", "1", "-u", "2", "-d", "13"}
	if !reflect.DeepEqual(c.TsreadexArgs, wantArgs) {
		t.Errorf("TsreadexArgs = %v", c.TsreadexArgs)
	}
	if c.MaxStreams != 8 {
		t.Errorf("MaxStreams = %d", c.MaxStreams)
	}
	if c.FirstByteTimeout != 15*time.Second {
		t.Errorf("FirstByteTimeout = %v", c.FirstByteTimeout)
	}
	wantV4 := []string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	if !reflect.DeepEqual(c.AllowIPv4, wantV4) {
		t.Errorf("AllowIPv4 = %v", c.AllowIPv4)
	}
	if !reflect.DeepEqual(c.AllowIPv6, []string{"fc00::/7"}) {
		t.Errorf("AllowIPv6 = %v", c.AllowIPv6)
	}
	if len(c.AllowOrigins) != 0 || c.AllowedHostname != "" {
		t.Errorf("AllowOrigins = %v, AllowedHostname = %q", c.AllowOrigins, c.AllowedHostname)
	}
}

func TestLoadOverridesAndNormalizes(t *testing.T) {
	c, err := Load(nil, env(map[string]string{
		"MIRAKURUN_URL":          "http://m:40772/",
		"LISTEN_PORT":            "9000",
		"PUBLIC_BASE_URL":        "http://proxy.lan:9000/",
		"TSREADEX_PATH":          "/usr/local/bin/tsreadex",
		"TSREADEX_ARGS":          "-m 2 -t 10",
		"MAX_STREAMS":            "3",
		"FIRST_BYTE_TIMEOUT":     "500ms",
		"ALLOW_IPV4_CIDR_RANGES": "192.168.0.0/16, 10.0.0.0/8",
		"ALLOW_ORIGINS":          "https://a.example,https://b.example",
		"ALLOWED_HOSTNAME":       "tsreadex-proxy",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MirakurunURL.String(); got != "http://m:40772" {
		t.Errorf("MirakurunURL = %q (trailing slash must be trimmed)", got)
	}
	if c.PublicBaseURL != "http://proxy.lan:9000" {
		t.Errorf("PublicBaseURL = %q", c.PublicBaseURL)
	}
	if c.ListenPort != 9000 || c.MaxStreams != 3 || c.FirstByteTimeout != 500*time.Millisecond {
		t.Errorf("got %+v", c)
	}
	if !reflect.DeepEqual(c.TsreadexArgs, []string{"-m", "2", "-t", "10"}) {
		t.Errorf("TsreadexArgs = %v", c.TsreadexArgs)
	}
	if !reflect.DeepEqual(c.AllowIPv4, []string{"192.168.0.0/16", "10.0.0.0/8"}) {
		t.Errorf("AllowIPv4 = %v", c.AllowIPv4)
	}
	if !reflect.DeepEqual(c.AllowOrigins, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("AllowOrigins = %v", c.AllowOrigins)
	}
	if c.AllowedHostname != "tsreadex-proxy" {
		t.Errorf("AllowedHostname = %q", c.AllowedHostname)
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	c, err := Load([]string{"-listen-port", "9100", "-mirakurun-url", "http://flag:1"}, env(map[string]string{
		"MIRAKURUN_URL": "http://env:2",
		"LISTEN_PORT":   "8081",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenPort != 9100 || c.MirakurunURL.Host != "flag:1" {
		t.Errorf("got port=%d url=%s", c.ListenPort, c.MirakurunURL)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	base := map[string]string{"MIRAKURUN_URL": "http://m:40772"}
	cases := []struct {
		name string
		set  map[string]string
		args []string
	}{
		{"missing MIRAKURUN_URL", map[string]string{}, nil},
		{"ftp scheme", map[string]string{"MIRAKURUN_URL": "ftp://m"}, nil},
		{"no host", map[string]string{"MIRAKURUN_URL": "http://"}, nil},
		{"query in url", map[string]string{"MIRAKURUN_URL": "http://m/?a=1"}, nil},
		{"not a url", map[string]string{"PUBLIC_BASE_URL": "not a url"}, nil},
		{"port 0", map[string]string{"LISTEN_PORT": "0"}, nil},
		{"port too large", map[string]string{"LISTEN_PORT": "70000"}, nil},
		{"port not a number", map[string]string{"LISTEN_PORT": "abc"}, nil},
		{"max streams 0", map[string]string{"MAX_STREAMS": "0"}, nil},
		{"bad timeout", map[string]string{"FIRST_BYTE_TIMEOUT": "soon"}, nil},
		{"zero timeout", map[string]string{"FIRST_BYTE_TIMEOUT": "0s"}, nil},
		{"forbidden tsreadex arg", map[string]string{"TSREADEX_ARGS": "-n 1"}, nil},
		{"positional argument", nil, []string{"extra"}},
		{"unknown flag", nil, []string{"-nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			for k, v := range tc.set {
				m[k] = v
			}
			if tc.name == "missing MIRAKURUN_URL" {
				delete(m, "MIRAKURUN_URL")
			}
			if _, err := Load(tc.args, env(m)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseTsreadexArgs(t *testing.T) {
	valid := []string{
		"",
		"-x 18/38/39 -b 1 -c 1 -u 2 -d 13",
		"-m 2 -t 10",
		"  -b   1  ",
	}
	for _, s := range valid {
		if _, err := ParseTsreadexArgs(s); err != nil {
			t.Errorf("ParseTsreadexArgs(%q) = %v, want ok", s, err)
		}
	}

	invalid := map[string]string{
		"-n 1":          "proxy-owned -n",
		"-z x":          "proxy-owned -z",
		"-s 0":          "proxy-owned -s",
		"-r -":          "proxy-owned -r",
		"-n1":           "proxy-owned, combined form",
		"-b 1 -n 5":     "proxy-owned after valid option",
		"-b1":           "combined form is unsupported",
		"foo":           "positional",
		"-":             "stdin marker is added by the proxy",
		"-b":            "missing value",
		"-b -c":         "value looks like an option",
		"-q 1":          "unknown option",
		"-b 1 src.m2ts": "positional after options",
	}
	for s, why := range invalid {
		if _, err := ParseTsreadexArgs(s); err == nil {
			t.Errorf("ParseTsreadexArgs(%q) = ok, want error (%s)", s, why)
		}
	}
}

func TestParseTsreadexArgsErrorMentionsOption(t *testing.T) {
	_, err := ParseTsreadexArgs("-n 1")
	if err == nil || !strings.Contains(err.Error(), "-n") {
		t.Errorf("error should name the option, got %v", err)
	}
}
```

- [ ] **Step 3: テストが失敗することを確認する**

Run: `go test ./internal/config/ -v`
Expected: FAIL(`undefined: Load` などのコンパイルエラー)

- [ ] **Step 4: 最小の実装を書く**

`internal/config/config.go`:

```go
// Package config loads and validates the proxy's startup configuration.
package config

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultIPv4Ranges   = "10.0.0.0/8,127.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
	defaultIPv6Ranges   = "fc00::/7"
	defaultTsreadexArgs = "-x 18/38/39 -b 1 -c 1 -u 2 -d 13"
)

// Config is the validated startup configuration.
type Config struct {
	MirakurunURL     *url.URL
	ListenPort       int
	PublicBaseURL    string
	TsreadexPath     string
	TsreadexArgs     []string
	MaxStreams       int
	FirstByteTimeout time.Duration
	AllowIPv4        []string
	AllowIPv6        []string
	AllowOrigins     []string
	AllowedHostname  string
}

// Load reads the configuration. Each setting comes from a flag (the
// lower-case, hyphenated form of the variable name), then the environment
// variable, then the default.
func Load(args []string, getenv func(string) string) (*Config, error) {
	fs := flag.NewFlagSet("tsreadex-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	str := func(name, envName, def string) *string {
		v := def
		if e := getenv(envName); e != "" {
			v = e
		}
		return fs.String(name, v, "env: "+envName)
	}
	mirakurunURL := str("mirakurun-url", "MIRAKURUN_URL", "")
	listenPort := str("listen-port", "LISTEN_PORT", "8080")
	publicBase := str("public-base-url", "PUBLIC_BASE_URL", "")
	tsPath := str("tsreadex-path", "TSREADEX_PATH", "tsreadex")
	tsArgs := str("tsreadex-args", "TSREADEX_ARGS", defaultTsreadexArgs)
	maxStreams := str("max-streams", "MAX_STREAMS", "8")
	firstByte := str("first-byte-timeout", "FIRST_BYTE_TIMEOUT", "15s")
	v4 := str("allow-ipv4-cidr-ranges", "ALLOW_IPV4_CIDR_RANGES", defaultIPv4Ranges)
	v6 := str("allow-ipv6-cidr-ranges", "ALLOW_IPV6_CIDR_RANGES", defaultIPv6Ranges)
	origins := str("allow-origins", "ALLOW_ORIGINS", "")
	hostname := str("allowed-hostname", "ALLOWED_HOSTNAME", "")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	c := &Config{
		TsreadexPath:    *tsPath,
		AllowIPv4:       splitCSV(*v4),
		AllowIPv6:       splitCSV(*v6),
		AllowOrigins:    splitCSV(*origins),
		AllowedHostname: *hostname,
	}

	var err error
	if c.MirakurunURL, err = parseBaseURL("MIRAKURUN_URL", *mirakurunURL, true); err != nil {
		return nil, err
	}
	if *publicBase != "" {
		u, err := parseBaseURL("PUBLIC_BASE_URL", *publicBase, false)
		if err != nil {
			return nil, err
		}
		c.PublicBaseURL = u.String()
	}
	if c.ListenPort, err = parseInt("LISTEN_PORT", *listenPort, 1, 65535); err != nil {
		return nil, err
	}
	if c.MaxStreams, err = parseInt("MAX_STREAMS", *maxStreams, 1, 1<<20); err != nil {
		return nil, err
	}
	if c.FirstByteTimeout, err = time.ParseDuration(*firstByte); err != nil || c.FirstByteTimeout <= 0 {
		return nil, fmt.Errorf("FIRST_BYTE_TIMEOUT: %q must be a positive duration such as 15s", *firstByte)
	}
	if c.TsreadexArgs, err = ParseTsreadexArgs(*tsArgs); err != nil {
		return nil, err
	}
	return c, nil
}

func parseInt(name, s string, min, max int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s: %q must be an integer between %d and %d", name, s, min, max)
	}
	return n, nil
}

func parseBaseURL(name, raw string, required bool) (*url.URL, error) {
	if raw == "" {
		if required {
			return nil, fmt.Errorf("%s is required", name)
		}
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s: %q must be an http(s) URL with a host", name, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%s: %q must not contain a query or fragment", name, raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Options that take a value and may be set through TSREADEX_ARGS.
var valueOptions = map[byte]bool{'l': true, 't': true, 'm': true, 'x': true, 'a': true, 'b': true, 'c': true, 'u': true, 'd': true}

// Options the proxy controls itself: -n is derived from the requested
// service, -z/-s/-r are not meaningful for a live pipe.
var proxyOwned = map[byte]bool{'n': true, 'z': true, 's': true, 'r': true}

// ParseTsreadexArgs splits and validates the extra tsreadex arguments.
// Only "-X value" pairs of known options are accepted: combined forms such as
// "-b1" are rejected so that a proxy-owned option can never slip through.
func ParseTsreadexArgs(s string) ([]string, error) {
	tokens := strings.Fields(s)
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if len(t) < 2 || t[0] != '-' {
			return nil, fmt.Errorf("TSREADEX_ARGS: positional argument %q is not allowed", t)
		}
		opt := t[1]
		if proxyOwned[opt] {
			return nil, fmt.Errorf("TSREADEX_ARGS: option -%c is controlled by the proxy", opt)
		}
		if !valueOptions[opt] || len(t) != 2 {
			return nil, fmt.Errorf("TSREADEX_ARGS: unsupported option %q", t)
		}
		i++
		if i >= len(tokens) || strings.HasPrefix(tokens[i], "-") {
			return nil, fmt.Errorf("TSREADEX_ARGS: option %s needs a value", t)
		}
	}
	return tokens, nil
}
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `go test ./internal/config/ -v && gofmt -l . && go vet ./...`
Expected: すべて PASS、`gofmt -l .` は何も出力しない

- [ ] **Step 6: コミットする**

```bash
git add go.mod .gitignore internal/config
git commit -m "feat: 設定の読み込みと検証を追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 2: アクセス制御の許可判定(`internal/access/policy.go`)

**Files:**
- Create: `internal/access/policy.go`
- Test: `internal/access/policy_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `type Policy struct{ /* unexported */ }`
  - `func NewPolicy(v4, v6 []string, hostname string, origins []string) (*Policy, error)`
  - `func (p *Policy) PermittedIP(a netip.Addr) bool`
  - `func (p *Policy) PermittedOrigin(raw string) bool`
  - `func (p *Policy) PermittedReferer(raw string) bool`
  - `func (p *Policy) PermittedHostHeader(host string) bool`

- [ ] **Step 1: 失敗するテストを書く**

`internal/access/policy_test.go`:

```go
package access

import (
	"net/netip"
	"testing"
)

var (
	defaultV4 = []string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	defaultV6 = []string{"fc00::/7"}
)

// newPolicy builds a policy with Mirakurun's default ranges.
func newPolicy(t *testing.T, hostname string, origins ...string) *Policy {
	t.Helper()
	p, err := NewPolicy(defaultV4, defaultV6, hostname, origins)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPolicyRejectsBadRanges(t *testing.T) {
	cases := []struct {
		name   string
		v4, v6 []string
	}{
		{"prefix too long", []string{"10.0.0.0/33"}, nil},
		{"not a cidr", []string{"abc"}, nil},
		{"ipv6 in the ipv4 list", []string{"fc00::/7"}, nil},
		{"ipv4 in the ipv6 list", nil, []string{"10.0.0.0/8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPolicy(tc.v4, tc.v6, "", nil); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestPermittedIP(t *testing.T) {
	p := newPolicy(t, "")
	cases := map[string]bool{
		"192.168.1.5":          true,
		"10.1.2.3":             true,
		"172.16.0.1":           true,
		"172.31.255.255":       true,
		"172.32.0.1":           false,
		"8.8.8.8":              false,
		"127.0.0.1":            true,
		"::1":                  false, // not inside fc00::/7
		"fd00::1":              true,
		"2001:db8::1":          false,
		"::ffff:192.168.1.5":   true, // IPv4-mapped IPv6 is judged as IPv4
		"::ffff:8.8.8.8":       false,
		"fe80::1%eth0":         false,
		"fd00::1%eth0":         true, // the zone is ignored
		"203.0.113.9":          false,
		"192.169.0.1":          false,
		"100.64.0.1":           false, // CGNAT is not private
		"0.0.0.0":              false,
		"255.255.255.255":      false,
		"::":                   false,
		"fdff:ffff:ffff::1234": true,
	}
	for s, want := range cases {
		if got := p.PermittedIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("PermittedIP(%s) = %v, want %v", s, got, want)
		}
	}
	if p.PermittedIP(netip.Addr{}) {
		t.Error("the zero Addr must not be permitted")
	}
}

func TestPermittedOrigin(t *testing.T) {
	p := newPolicy(t, "tsreadex-proxy", "https://app.example")
	cases := map[string]bool{
		"http://localhost:3000":      true,
		"http://LOCALHOST":           true,
		"http://192.168.1.10:8080":   true,
		"http://tsreadex-proxy:8080": true,
		"http://TSREADEX-PROXY":      true,
		"http://[fd00::1]:80":        true,
		"http://evil.example":        false,
		"http://8.8.8.8":             false,
		"http://[::1]:8080":          false,
		"null":                       false,
		"":                           false,
		"://":                        false,
		"https://app.example":        true, // exact match with ALLOW_ORIGINS
		"https://app.example/":       false,
		"https://app.example.evil":   false,
	}
	for s, want := range cases {
		if got := p.PermittedOrigin(s); got != want {
			t.Errorf("PermittedOrigin(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestPermittedReferer(t *testing.T) {
	p := newPolicy(t, "", "https://app.example")
	cases := map[string]bool{
		"http://localhost/page":                true,
		"http://192.168.1.10:8080/a?b=c":       true,
		"http://evil.example/x":                false,
		"":                                     false,
		"https://app.example":                  true,
		"https://app.example/path":             true,
		"https://app.example?x=1":              true,
		"https://app.example#frag":             true,
		"https://app.example.evil.com/":        false, // must stop at the origin boundary
		"https://app.examplevil/":              false,
		"https://other.example/https://app.example": false,
	}
	for s, want := range cases {
		if got := p.PermittedReferer(s); got != want {
			t.Errorf("PermittedReferer(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestPermittedHostHeader(t *testing.T) {
	p := newPolicy(t, "")
	withName := newPolicy(t, "tsreadex-proxy")
	cases := []struct {
		host string
		p    *Policy
		want bool
	}{
		{"localhost", p, true},
		{"localhost:8080", p, true},
		{"LOCALHOST:8080", p, true},
		{"127.0.0.1:8080", p, true},
		{"192.168.1.2", p, true},
		{"[fd00::1]:8080", p, true},
		{"[fd00::1]", p, true},
		{"[::1]:8080", p, false},
		{"8.8.8.8", p, false},
		{"evil.example", p, false},
		{"evil.example:80", p, false},
		{"tsreadex-proxy:8080", p, false}, // not configured
		{"tsreadex-proxy:8080", withName, true},
		{"TSREADEX-PROXY", withName, true},
		{"", p, false},
		{"localhost/evil", p, false},
		{"localhost?x", p, false},
		{"user@localhost", p, false},
		{"local host", p, false},
		{"localhost\\evil", p, false},
	}
	for _, tc := range cases {
		if got := tc.p.PermittedHostHeader(tc.host); got != tc.want {
			t.Errorf("PermittedHostHeader(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/access/ -run 'TestNewPolicy|TestPermitted' -v`
Expected: FAIL(`undefined: NewPolicy` などのコンパイルエラー)

- [ ] **Step 3: 最小の実装を書く**

`internal/access/policy.go`:

```go
// Package access implements the access control that Mirakurun applies to
// its own API (source IP ranges, Origin/Referer checks against DNS rebinding
// and CSRF), plus the Host check and the listen-address selection.
package access

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// Policy decides which clients, origins and hostnames are permitted.
type Policy struct {
	v4, v6   []netip.Prefix
	hostname string
	origins  []string
}

// NewPolicy parses the CIDR ranges. hostname is the extra hostname clients
// use to reach the proxy; origins are extra permitted Origin/Referer values.
func NewPolicy(v4, v6 []string, hostname string, origins []string) (*Policy, error) {
	p := &Policy{hostname: strings.ToLower(hostname), origins: origins}
	for _, s := range v4 {
		pf, err := netip.ParsePrefix(s)
		if err != nil || !pf.Addr().Is4() {
			return nil, fmt.Errorf("invalid IPv4 CIDR range %q", s)
		}
		p.v4 = append(p.v4, pf.Masked())
	}
	for _, s := range v6 {
		pf, err := netip.ParsePrefix(s)
		if err != nil || !pf.Addr().Is6() {
			return nil, fmt.Errorf("invalid IPv6 CIDR range %q", s)
		}
		p.v6 = append(p.v6, pf.Masked())
	}
	return p, nil
}

// PermittedIP reports whether a is inside one of the permitted ranges.
// IPv4-mapped IPv6 addresses are judged as IPv4; a zone is ignored.
func (p *Policy) PermittedIP(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	prefixes := p.v6
	if a.Is4() {
		prefixes = p.v4
	}
	for _, pf := range prefixes {
		if pf.Contains(a) {
			return true
		}
	}
	return false
}

// hostnameAllowed mirrors Mirakurun's isPermittedHost: localhost, the
// configured hostname, or an IP address inside the permitted ranges.
func (p *Policy) hostnameAllowed(h string) bool {
	if h == "" {
		return false
	}
	h = strings.ToLower(h)
	if h == "localhost" || (p.hostname != "" && h == p.hostname) {
		return true
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return p.PermittedIP(a)
	}
	return false
}

// PermittedOrigin checks an Origin header value.
func (p *Policy) PermittedOrigin(raw string) bool {
	if u, err := url.Parse(raw); err == nil && u.Host != "" && p.hostnameAllowed(u.Hostname()) {
		return true
	}
	for _, o := range p.origins {
		if raw == o {
			return true
		}
	}
	return false
}

// PermittedReferer checks a Referer header value. Extra origins match as a
// prefix, but only at the origin boundary, so that "https://a.example" does
// not match "https://a.example.evil.com/".
func (p *Policy) PermittedReferer(raw string) bool {
	if u, err := url.Parse(raw); err == nil && u.Host != "" && p.hostnameAllowed(u.Hostname()) {
		return true
	}
	for _, o := range p.origins {
		if raw == o {
			return true
		}
		if strings.HasPrefix(raw, o) && strings.ContainsRune("/?#", rune(raw[len(o)])) {
			return true
		}
	}
	return false
}

// PermittedHostHeader checks the Host request header ("name", "name:port",
// "[v6]:port"). This is the proxy's addition to Mirakurun's checks.
func (p *Policy) PermittedHostHeader(host string) bool {
	if host == "" || strings.ContainsAny(host, "/?#@\\ ") {
		return false
	}
	u, err := url.Parse("//" + host)
	if err != nil {
		return false
	}
	return p.hostnameAllowed(u.Hostname())
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/access/ -run 'TestNewPolicy|TestPermitted' -v && gofmt -l . && go vet ./...`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/access/policy.go internal/access/policy_test.go
git commit -m "feat: アクセス制御のポリシー判定を追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 3: アクセス制御ミドルウェアとテスト共通部(`internal/access/middleware.go`、`internal/testutil`)

**Files:**
- Create: `internal/testutil/testutil.go`
- Create: `internal/access/middleware.go`
- Test: `internal/access/middleware_test.go`

**Interfaces:**
- Consumes: Task 2 の `Policy` と `Permitted*` 各メソッド。
- Produces:
  - `func (p *Policy) Middleware(log *slog.Logger, next http.Handler) http.Handler`
  - `func testutil.NewServer(t *testing.T, h http.Handler, remote string) *httptest.Server`(接続元アドレスが `remote`(`ip:port`)に見えるサーバーを起動し、`t.Cleanup` で閉じる)

- [ ] **Step 1: テスト共通部を作る**

`internal/testutil/testutil.go`:

```go
// Package testutil holds helpers shared by the tests of this module.
package testutil

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

type remoteListener struct {
	net.Listener
	remote net.Addr
}

func (l remoteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return remoteConn{Conn: c, remote: l.remote}, nil
}

type remoteConn struct {
	net.Conn
	remote net.Addr
}

func (c remoteConn) RemoteAddr() net.Addr { return c.remote }

// NewServer starts an HTTP server whose connections appear to come from
// remote ("ip:port"), so that source-IP checks can be tested over a real
// connection. The server is closed when the test ends.
func NewServer(t *testing.T, h http.Handler, remote string) *httptest.Server {
	t.Helper()
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		t.Fatalf("bad remote address %q: %v", remote, err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = remoteListener{Listener: srv.Listener, remote: net.TCPAddrFromAddrPort(ap)}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/access/middleware_test.go`:

```go
package access

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/testutil"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// start serves the middleware from the given (fake) remote address and
// returns the server and a counter of how often the inner handler ran.
func start(t *testing.T, p *Policy, remote string, log *slog.Logger) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, "ok")
	})
	return testutil.NewServer(t, p.Middleware(log, inner), remote), &calls
}

func newClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
}

func get(t *testing.T, url string, header http.Header, host string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if host != "" {
		req.Host = host
	}
	return newClient().Do(req)
}

func TestMiddlewareAllowsPermittedRequest(t *testing.T) {
	srv, calls := start(t, newPolicy(t, ""), "192.168.1.5:5555", discard)
	resp, err := get(t, srv.URL, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" || calls.Load() != 1 {
		t.Fatalf("status=%d body=%q calls=%d", resp.StatusCode, body, calls.Load())
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	for k := range resp.Header {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("unexpected CORS header %s", k)
		}
	}
}

func TestMiddlewareDropsConnectionFromOutsideRange(t *testing.T) {
	srv, calls := start(t, newPolicy(t, ""), "8.8.8.8:1234", discard)
	resp, err := get(t, srv.URL, nil, "")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected the connection to be dropped, got status %d", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Error("the inner handler must not run")
	}
}

func TestMiddlewareIgnoresForwardedFor(t *testing.T) {
	srv, calls := start(t, newPolicy(t, ""), "8.8.8.8:1234", discard)
	h := http.Header{"X-Forwarded-For": {"192.168.1.1"}, "X-Real-Ip": {"192.168.1.1"}}
	if resp, err := get(t, srv.URL, h, ""); err == nil {
		resp.Body.Close()
		t.Fatal("a spoofed X-Forwarded-For must not unlock a disallowed source")
	}
	if calls.Load() != 0 {
		t.Error("the inner handler must not run")
	}
}

func TestMiddlewareHeaderChecks(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		host   string
		want   int
	}{
		{"allowed origin", http.Header{"Origin": {"http://localhost:3000"}}, "", 200},
		{"evil origin", http.Header{"Origin": {"http://evil.example"}}, "", 403},
		{"null origin", http.Header{"Origin": {"null"}}, "", 403},
		{"empty origin", http.Header{"Origin": {""}}, "", 403},
		{"allowed referer", http.Header{"Referer": {"http://192.168.1.10/x"}}, "", 200},
		{"evil referer", http.Header{"Referer": {"http://evil.example/x"}}, "", 403},
		{"evil host (DNS rebinding)", nil, "evil.example", 403},
		{"configured hostname", nil, "tsreadex-proxy:8080", 200},
		{"ipv6 literal host", nil, "[fd00::1]:8080", 200},
		{"unconfigured name", nil, "other-name:8080", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := start(t, newPolicy(t, "tsreadex-proxy"), "192.168.1.5:5555", discard)
			resp, err := get(t, srv.URL, tc.header, tc.host)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == 403 && calls.Load() != 0 {
				t.Error("the inner handler must not run on 403")
			}
		})
	}
}

func TestSourceAllowed(t *testing.T) {
	p := newPolicy(t, "")
	cases := map[string]bool{
		"192.168.1.5:80":          true,
		"[::ffff:192.168.1.5]:80": true, // IPv4-mapped
		"[::ffff:8.8.8.8]:80":     false,
		"[fd00::1]:80":            true,
		"8.8.8.8:80":              false,
		"not-an-address":          false,
		"":                        false,
	}
	for remote, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if got := p.sourceAllowed(r); got != want {
			t.Errorf("sourceAllowed(%q) = %v, want %v", remote, got, want)
		}
	}
}

func TestMiddlewareAccessLog(t *testing.T) {
	var buf syncBuffer
	srv, _ := start(t, newPolicy(t, ""), "192.168.1.5:5555", slog.New(slog.NewTextHandler(&buf, nil)))
	resp, err := get(t, srv.URL+"/hello?x=1", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The log line is written after the handler returns, so wait for it.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "access") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	for _, want := range []string{"method=GET", "status=200", "/hello?x=1", "remote=192.168.1.5:5555"} {
		if !strings.Contains(out, want) {
			t.Errorf("access log missing %q:\n%s", want, out)
		}
	}
}

func TestMiddlewareLogsDenialReason(t *testing.T) {
	var buf syncBuffer
	srv, _ := start(t, newPolicy(t, ""), "192.168.1.5:5555", slog.New(slog.NewTextHandler(&buf, nil)))
	resp, err := get(t, srv.URL, nil, "evil.example")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if out := buf.String(); !strings.Contains(out, "denied") || !strings.Contains(out, "reason=host") {
		t.Errorf("denial log should name the failed check:\n%s", out)
	}
}
```

- [ ] **Step 3: テストが失敗することを確認する**

Run: `go test ./internal/access/ -run 'TestMiddleware|TestSourceAllowed' -v`
Expected: FAIL(`p.Middleware undefined` などのコンパイルエラー)

- [ ] **Step 4: 最小の実装を書く**

`internal/access/middleware.go`:

```go
package access

import (
	"log/slog"
	"net/http"
	"net/netip"
	"time"
)

// Middleware applies the access checks in Mirakurun's order (source IP,
// Origin, Referer) plus the Host check, then logs the request. A request from
// outside the permitted ranges is dropped without any response.
func (p *Policy) Middleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.sourceAllowed(r) {
			log.Warn("denied", "reason", "source-ip", "remote", r.RemoteAddr)
			dropConnection(w)
			return
		}
		if reason := p.headerViolation(r); reason != "" {
			log.Warn("denied", "reason", reason, "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		w.Header().Set("X-Content-Type-Options", "nosniff")
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		// Logged from a defer so that an aborted response (panic with
		// http.ErrAbortHandler) is still recorded.
		defer func() {
			log.Info("access",
				"remote", r.RemoteAddr,
				"method", r.Method,
				"url", r.URL.RequestURI(),
				"status", rec.status,
				"duration", time.Since(start),
				"user_agent", r.UserAgent())
		}()
		next.ServeHTTP(rec, r)
	})
}

// sourceAllowed judges the connection's own address; forwarding headers such
// as X-Forwarded-For are never consulted.
func (p *Policy) sourceAllowed(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return p.PermittedIP(ap.Addr())
}

// headerViolation returns the name of the first failed header check, or "".
func (p *Policy) headerViolation(r *http.Request) string {
	if v, ok := r.Header["Origin"]; ok && (len(v) == 0 || !p.PermittedOrigin(v[0])) {
		return "origin"
	}
	if v, ok := r.Header["Referer"]; ok && (len(v) == 0 || !p.PermittedReferer(v[0])) {
		return "referer"
	}
	if !p.PermittedHostHeader(r.Host) {
		return "host"
	}
	return ""
}

// dropConnection closes the connection without writing a response, like
// Mirakurun's req.socket.end().
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusForbidden)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.NewResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `go test ./internal/access/ -v && gofmt -l . && go vet ./...`
Expected: PASS

- [ ] **Step 6: コミットする**

```bash
git add internal/testutil internal/access/middleware.go internal/access/middleware_test.go
git commit -m "feat: アクセス制御ミドルウェアを追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 4: 許可範囲のアドレスのみで待ち受ける(`internal/access/listen.go`)

**Files:**
- Create: `internal/access/listen.go`
- Test: `internal/access/listen_test.go`

**Interfaces:**
- Consumes: Task 2 の `Policy.PermittedIP`、Task 2 のテスト用 `newPolicy`、Task 3 のテスト用 `discard`。
- Produces:
  - `type IfaceAddr struct{ Addr netip.Addr; Loopback bool }`
  - `func SelectListenAddrs(addrs []IfaceAddr, p *Policy) (selected []netip.Addr, haveLAN4 bool)`
  - `func SystemAddrs() ([]IfaceAddr, error)`
  - `func Listen(ctx context.Context, p *Policy, port int, log *slog.Logger, enumerate func() ([]IfaceAddr, error), retry time.Duration) ([]net.Listener, error)`

- [ ] **Step 1: 失敗するテストを書く**

`internal/access/listen_test.go`:

```go
package access

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func ifaceAddr(s string, loopback bool) IfaceAddr {
	return IfaceAddr{Addr: netip.MustParseAddr(s), Loopback: loopback}
}

func addrStrings(addrs []netip.Addr) []string {
	var out []string
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

func TestSelectListenAddrs(t *testing.T) {
	all := []IfaceAddr{
		ifaceAddr("203.0.113.5", false), // public
		ifaceAddr("192.168.1.10", false),
		ifaceAddr("127.0.0.1", true),
		ifaceAddr("fd00::1", false),
		ifaceAddr("::1", true),
		ifaceAddr("2001:db8::1", false), // public
		ifaceAddr("fe80::1%eth0", false),
	}

	t.Run("public and unpermitted addresses are excluded", func(t *testing.T) {
		got, lan4 := SelectListenAddrs(all, newPolicy(t, ""))
		want := []string{"192.168.1.10", "127.0.0.1", "fd00::1"}
		if strings.Join(addrStrings(got), ",") != strings.Join(want, ",") {
			t.Errorf("selected = %v, want %v", addrStrings(got), want)
		}
		if !lan4 {
			t.Error("a non-loopback permitted IPv4 address exists")
		}
	})

	t.Run("loopback only does not count as a LAN address", func(t *testing.T) {
		got, lan4 := SelectListenAddrs([]IfaceAddr{ifaceAddr("127.0.0.1", true)}, newPolicy(t, ""))
		if len(got) != 1 || lan4 {
			t.Errorf("selected=%v lan4=%v", got, lan4)
		}
	})

	t.Run("only public addresses select nothing", func(t *testing.T) {
		got, lan4 := SelectListenAddrs([]IfaceAddr{ifaceAddr("203.0.113.5", false)}, newPolicy(t, ""))
		if len(got) != 0 || lan4 {
			t.Errorf("selected=%v lan4=%v", got, lan4)
		}
	})

	t.Run("a scoped address keeps its zone when its range is permitted", func(t *testing.T) {
		p, err := NewPolicy(defaultV4, []string{"fc00::/7", "fe80::/10"}, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := SelectListenAddrs(all, p)
		found := false
		for _, a := range got {
			if a.String() == "fe80::1%eth0" {
				found = true
			}
		}
		if !found {
			t.Errorf("selected = %v, want fe80::1%%eth0 included", addrStrings(got))
		}
	})

	t.Run("::1 is not listened on by default", func(t *testing.T) {
		got, _ := SelectListenAddrs(all, newPolicy(t, ""))
		for _, a := range got {
			if a.String() == "::1" {
				t.Error("::1 is outside fc00::/7 and must not be listened on")
			}
		}
	})
}

func TestListenBindsOnlyPermittedAddresses(t *testing.T) {
	enumerate := func() ([]IfaceAddr, error) {
		return []IfaceAddr{
			ifaceAddr("127.0.0.2", false), // stands in for a LAN address
			ifaceAddr("127.0.0.1", true),
			ifaceAddr("8.8.8.8", false), // public: must never be bound
		}, nil
	}
	lns, err := Listen(context.Background(), newPolicy(t, ""), 0, discard, enumerate, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range lns {
			l.Close()
		}
	}()
	if len(lns) != 2 {
		t.Fatalf("got %d listeners, want 2", len(lns))
	}
	for _, l := range lns {
		host, _, _ := net.SplitHostPort(l.Addr().String())
		if host != "127.0.0.1" && host != "127.0.0.2" {
			t.Errorf("unexpected listen address %s", l.Addr())
		}
	}
}

func TestListenWaitsForANetworkAddress(t *testing.T) {
	calls := 0
	enumerate := func() ([]IfaceAddr, error) {
		calls++
		if calls < 3 {
			return []IfaceAddr{ifaceAddr("127.0.0.1", true)}, nil // loopback only: keep waiting
		}
		return []IfaceAddr{ifaceAddr("127.0.0.2", false)}, nil
	}
	lns, err := Listen(context.Background(), newPolicy(t, ""), 0, discard, enumerate, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lns {
		l.Close()
	}
	if calls != 3 {
		t.Errorf("enumerate called %d times, want 3", calls)
	}
}

func TestListenStopsWaitingWhenCancelled(t *testing.T) {
	enumerate := func() ([]IfaceAddr, error) { return []IfaceAddr{ifaceAddr("127.0.0.1", true)}, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Listen(ctx, newPolicy(t, ""), 0, discard, enumerate, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestListenFailsWhenAnIPv4AddressCannotBeBound(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	enumerate := func() ([]IfaceAddr, error) { return []IfaceAddr{ifaceAddr("127.0.0.2", false)}, nil }
	if _, err := Listen(context.Background(), newPolicy(t, ""), port, discard, enumerate, 10*time.Millisecond); err == nil {
		t.Fatal("expected an error when the IPv4 port is taken")
	}
}

func TestListenSkipsAnIPv6AddressThatCannotBeBound(t *testing.T) {
	enumerate := func() ([]IfaceAddr, error) {
		return []IfaceAddr{
			ifaceAddr("127.0.0.2", false),
			ifaceAddr("fd00::1234", false), // not assigned to this machine
		}, nil
	}
	lns, err := Listen(context.Background(), newPolicy(t, ""), 0, discard, enumerate, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("an unbindable IPv6 address must not be fatal: %v", err)
	}
	for _, l := range lns {
		l.Close()
	}
	if len(lns) != 1 {
		t.Errorf("got %d listeners, want 1", len(lns))
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/access/ -run 'TestSelectListenAddrs|TestListen' -v`
Expected: FAIL(`undefined: IfaceAddr` などのコンパイルエラー)

- [ ] **Step 3: 最小の実装を書く**

`internal/access/listen.go`:

```go
package access

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// IfaceAddr is one address of a network interface. IPv6 link-local
// addresses carry the interface name as their zone.
type IfaceAddr struct {
	Addr     netip.Addr
	Loopback bool
}

// SelectListenAddrs keeps the addresses inside the permitted ranges.
// haveLAN4 reports whether any of them is a non-loopback IPv4 address.
func SelectListenAddrs(addrs []IfaceAddr, p *Policy) (selected []netip.Addr, haveLAN4 bool) {
	for _, a := range addrs {
		if !p.PermittedIP(a.Addr) {
			continue
		}
		selected = append(selected, a.Addr)
		if a.Addr.Is4() && !a.Loopback {
			haveLAN4 = true
		}
	}
	return selected, haveLAN4
}

// SystemAddrs lists the addresses of all interfaces that are up.
func SystemAddrs() ([]IfaceAddr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []IfaceAddr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if addr.Is6() && (addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()) {
				addr = addr.WithZone(ifc.Name)
			}
			out = append(out, IfaceAddr{Addr: addr, Loopback: addr.IsLoopback()})
		}
	}
	return out, nil
}

// Listen binds one listener per permitted interface address. It never binds a
// wildcard address. Like Mirakurun it retries until a permitted non-loopback
// IPv4 address exists (the network may not be up yet at boot), and it does not
// follow address changes afterwards. A failure to bind an IPv4 address is
// fatal; an IPv6 address that cannot be bound is skipped with a warning.
func Listen(ctx context.Context, p *Policy, port int, log *slog.Logger, enumerate func() ([]IfaceAddr, error), retry time.Duration) ([]net.Listener, error) {
	var selected []netip.Addr
	for {
		addrs, err := enumerate()
		if err != nil {
			log.Warn("cannot enumerate network interfaces", "error", err)
		} else {
			var haveLAN4 bool
			selected, haveLAN4 = SelectListenAddrs(addrs, p)
			if haveLAN4 {
				break
			}
			log.Warn("no permitted non-loopback IPv4 address yet; retrying", "retry_in", retry)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retry):
		}
	}

	var (
		lc        net.ListenConfig
		listeners []net.Listener
	)
	for _, a := range selected {
		hostport := net.JoinHostPort(a.String(), strconv.Itoa(port))
		ln, err := lc.Listen(ctx, "tcp", hostport)
		if err != nil {
			if a.Is4() {
				for _, l := range listeners {
					l.Close()
				}
				return nil, fmt.Errorf("listen %s: %w", hostport, err)
			}
			log.Warn("cannot listen on IPv6 address; skipping", "addr", hostport, "error", err)
			continue
		}
		log.Info("listening", "addr", ln.Addr().String())
		listeners = append(listeners, ln)
	}
	return listeners, nil
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/access/ -v && gofmt -l . && go vet ./...`
Expected: PASS(`127.0.0.2` はループバックの `127.0.0.0/8` なので Linux では bind できる)

- [ ] **Step 5: コミットする**

```bash
git add internal/access/listen.go internal/access/listen_test.go
git commit -m "feat: 許可範囲のアドレスのみで待ち受ける処理を追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 5: M3U の書き換え(`internal/playlist`)

**Files:**
- Create: `internal/playlist/playlist.go`
- Test: `internal/playlist/playlist_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `var ErrTooLarge error`
  - `func ReadLimited(r io.Reader, max int64) (string, error)`
  - `func Rewrite(src, publicBase string) string`

- [ ] **Step 1: 失敗するテストを書く**

`internal/playlist/playlist_test.go`:

```go
package playlist

import (
	"errors"
	"strings"
	"testing"
)

const mirakurunPlaylist = `#EXTM3U url-tvg="http://mirakurun:40772/api/iptv/xmltv"
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://mirakurun:40772/api/services/3273601024/logo" group-title="GR",NHK総合
http://mirakurun:40772/api/services/3273601024/stream
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273701032" group-title="GR",NHK Eテレ
http://mirakurun:40772/api/services/3273701032/stream
`

const mirakurunWant = `#EXTM3U url-tvg="http://proxy:8080/xmltv"
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://proxy:8080/logo/3273601024" group-title="GR",NHK総合
http://proxy:8080/stream/3273601024
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273701032" group-title="GR",NHK Eテレ
http://proxy:8080/stream/3273701032
`

func TestRewriteMirakurunPlaylist(t *testing.T) {
	if got := Rewrite(mirakurunPlaylist, "http://proxy:8080"); got != mirakurunWant {
		t.Errorf("got:\n%s\nwant:\n%s", got, mirakurunWant)
	}
}

func TestRewriteMirakcPlaylist(t *testing.T) {
	// mirakc has no url-tvg attribute and appends a query to the stream URL.
	src := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://mirakc:40772/api/services/3273601024/logo" group-title="GR",NHK` + "\n" +
		"http://mirakc:40772/api/services/3273601024/stream?decode=1&x=a%20b\n"
	want := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://proxy:8080/logo/3273601024" group-title="GR",NHK` + "\n" +
		"http://proxy:8080/stream/3273601024?decode=1&x=a%20b\n"
	if got := Rewrite(src, "http://proxy:8080"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewrite_CRLF(t *testing.T) {
	src := "#EXTM3U url-tvg=\"http://m:1/api/iptv/xmltv\"\r\n#EXTINF:-1 tvg-id=\"1\",A\r\nhttp://m:1/api/services/1/stream\r\n"
	want := "#EXTM3U url-tvg=\"http://p/xmltv\"\r\n#EXTINF:-1 tvg-id=\"1\",A\r\nhttp://p/stream/1\r\n"
	if got := Rewrite(src, "http://p"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewrite_NoTrailingNewlineAndEmpty(t *testing.T) {
	if got := Rewrite("http://m:1/api/services/7/stream", "http://p"); got != "http://p/stream/7" {
		t.Errorf("got %q", got)
	}
	if got := Rewrite("", "http://p"); got != "" {
		t.Errorf("got %q", got)
	}
	if got := Rewrite("\n\n", "http://p"); got != "\n\n" {
		t.Errorf("got %q", got)
	}
}

func TestRewrite_LeavesOtherLinesAlone(t *testing.T) {
	src := "#EXTM3U\n" +
		"#EXTINF:-1,Other\n" +
		"http://example.com/other.ts\n" +
		"http://m:1/api/services/abc/stream\n" + // non-numeric id
		"http://m:1/api/services/1/stream/extra\n" + // different path
		"#COMMENT http://m:1/api/services/1/stream\n"
	if got := Rewrite(src, "http://p"); got != src {
		t.Errorf("lines that do not match must be unchanged:\n%s", got)
	}
}

func TestRewrite_TrimsTrailingSlashOfBase(t *testing.T) {
	got := Rewrite("http://m:1/api/services/1/stream\n", "http://p:8080/")
	if got != "http://p:8080/stream/1\n" {
		t.Errorf("got %q", got)
	}
}

func TestRewrite_BaseWithSpecialCharsIsLiteral(t *testing.T) {
	got := Rewrite("#EXTM3U url-tvg=\"x\"\nhttp://m:1/api/services/1/stream\n", `http://p/$1\0`)
	if !strings.Contains(got, `url-tvg="http://p/$1\0/xmltv"`) || !strings.Contains(got, `http://p/$1\0/stream/1`) {
		t.Errorf("the base must be inserted literally, got:\n%s", got)
	}
}

func TestReadLimited(t *testing.T) {
	if got, err := ReadLimited(strings.NewReader("12345"), 5); err != nil || got != "12345" {
		t.Errorf("exactly at the limit: got %q, %v", got, err)
	}
	if _, err := ReadLimited(strings.NewReader("123456"), 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("one byte over the limit: err = %v, want ErrTooLarge", err)
	}
	if got, err := ReadLimited(strings.NewReader(""), 5); err != nil || got != "" {
		t.Errorf("empty: got %q, %v", got, err)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/playlist/ -v`
Expected: FAIL(`undefined: Rewrite` などのコンパイルエラー)

- [ ] **Step 3: 最小の実装を書く**

`internal/playlist/playlist.go`:

```go
// Package playlist rewrites the IPTV playlist of Mirakurun/mirakc so that
// every URL points at this proxy.
package playlist

import (
	"errors"
	"io"
	"regexp"
	"strings"
)

// ErrTooLarge is returned by ReadLimited when the input exceeds the limit.
var ErrTooLarge = errors.New("playlist exceeds the size limit")

var (
	reURLTvg = regexp.MustCompile(`url-tvg="[^"]*"`)
	reLogo   = regexp.MustCompile(`tvg-logo="[^"]*/api/services/(\d+)/logo"`)
	reStream = regexp.MustCompile(`^https?://[^/\s]+/api/services/(\d+)/stream(\?\S*)?$`)
)

// ReadLimited reads all of r, failing with ErrTooLarge beyond max bytes.
func ReadLimited(r io.Reader, max int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > max {
		return "", ErrTooLarge
	}
	return string(b), nil
}

// Rewrite points the XMLTV URL, the logo URLs and the stream URLs of src at
// publicBase. Lines that do not match are passed through unchanged, as are
// the line endings.
func Rewrite(src, publicBase string) string {
	base := strings.TrimRight(publicBase, "/")
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		body := strings.TrimRight(line, "\r")
		eol := line[len(body):]
		lines[i] = rewriteLine(body, base) + eol
	}
	return strings.Join(lines, "\n")
}

func rewriteLine(line, base string) string {
	switch {
	case strings.HasPrefix(line, "#EXTM3U"):
		return reURLTvg.ReplaceAllLiteralString(line, `url-tvg="`+base+`/xmltv"`)
	case strings.HasPrefix(line, "#EXTINF"):
		return reLogo.ReplaceAllStringFunc(line, func(m string) string {
			return `tvg-logo="` + base + "/logo/" + reLogo.FindStringSubmatch(m)[1] + `"`
		})
	case strings.HasPrefix(line, "#"):
		return line
	}
	if m := reStream.FindStringSubmatch(line); m != nil {
		return base + "/stream/" + m[1] + m[2]
	}
	return line
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/playlist/ -v && gofmt -l . && go vet ./...`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/playlist
git commit -m "feat: M3Uの書き換えを追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 6: 上流アクセスの共通処理(`internal/upstream`)

**Files:**
- Create: `internal/upstream/upstream.go`
- Test: `internal/upstream/upstream_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `func NewClient() *http.Client`(リダイレクト非追従、環境のプロキシ設定を使わない、圧縮を無効化)
  - `func URL(base *url.URL, rawQuery string, elem ...string) string`(`base` のパスの後ろに `elem` を連結する)
  - `func ClientStatus(code int) int`(4xx/5xx はそのまま、それ以外は 502)

- [ ] **Step 1: 失敗するテストを書く**

`internal/upstream/upstream_test.go`:

```go
package upstream

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestURL(t *testing.T) {
	cases := []struct {
		base, query string
		elem        []string
		want        string
	}{
		{"http://m:40772", "", []string{"api", "iptv", "playlist"}, "http://m:40772/api/iptv/playlist"},
		{"http://m:40772", "decode=1", []string{"api", "services", "1024", "stream"}, "http://m:40772/api/services/1024/stream?decode=1"},
		{"http://m:40772/prefix", "", []string{"api", "x"}, "http://m:40772/prefix/api/x"},
		{"https://m", "a=b&c=%20", []string{"api", "x"}, "https://m/api/x?a=b&c=%20"},
	}
	for _, tc := range cases {
		base, err := url.Parse(tc.base)
		if err != nil {
			t.Fatal(err)
		}
		if got := URL(base, tc.query, tc.elem...); got != tc.want {
			t.Errorf("URL(%s, %q, %v) = %q, want %q", tc.base, tc.query, tc.elem, got, tc.want)
		}
	}
}

func TestURLDoesNotMutateBase(t *testing.T) {
	base, _ := url.Parse("http://m:40772")
	URL(base, "q=1", "api", "x")
	if base.Path != "" || base.RawQuery != "" {
		t.Errorf("base was modified: %+v", base)
	}
}

func TestClientStatus(t *testing.T) {
	cases := map[int]int{
		200: 502, 204: 502, 301: 502, 302: 502, 304: 502,
		400: 400, 404: 404, 429: 429, 500: 500, 503: 503, 599: 599,
		100: 502, 600: 502,
	}
	for in, want := range cases {
		if got := ClientStatus(in); got != want {
			t.Errorf("ClientStatus(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := NewClient().Get(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the redirect response itself (302)", resp.StatusCode)
	}
	if hits.Load() != 0 {
		t.Error("the redirect target must not be requested")
	}
}

func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	// httptest servers are on 127.0.0.1, which Go exempts from proxying, so
	// check the transport setting directly.
	tr, ok := NewClient().Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Error("the upstream client must not use proxy settings from the environment")
	}
	if !tr.DisableCompression {
		t.Error("compression must be disabled for an MPEG-TS stream")
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/upstream/ -v`
Expected: FAIL(`undefined: URL` などのコンパイルエラー)

- [ ] **Step 3: 最小の実装を書く**

`internal/upstream/upstream.go`:

```go
// Package upstream holds what every request to Mirakurun/mirakc has in common.
package upstream

import (
	"net/http"
	"net/url"
	"path"
)

// NewClient returns the HTTP client for upstream requests: no redirect
// following, no proxy from the environment, no transparent compression. It
// has no overall timeout because stream responses are long-lived; callers
// bound requests with a context.
func NewClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DisableCompression = true
	return &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// URL appends elem to the path of base and sets the raw query. Only the
// caller-validated elements are placed in the path, and base is not modified.
func URL(base *url.URL, rawQuery string, elem ...string) string {
	u := *base
	u.Path = path.Join(append([]string{"/", base.Path}, elem...)...)
	u.RawPath = ""
	u.RawQuery = rawQuery
	u.Fragment = ""
	return u.String()
}

// ClientStatus is the status returned to the client for a non-2xx upstream
// response: 4xx/5xx are passed through, anything else (such as a redirect)
// becomes 502.
func ClientStatus(code int) int {
	if code >= 400 && code <= 599 {
		return code
	}
	return http.StatusBadGateway
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/upstream/ -v && gofmt -l . && go vet ./...`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/upstream
git commit -m "feat: 上流アクセスの共通処理を追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 7: tsreadex を介したストリーム配信(`internal/stream`)

**Files:**
- Create: `internal/stream/id.go`, `internal/stream/stream.go`
- Modify: `internal/testutil/testutil.go`(`StubScript` を追加)
- Test: `internal/stream/id_test.go`, `internal/stream/stream_test.go`

**Interfaces:**
- Consumes: Task 6 の `upstream.NewClient`、`upstream.URL`、`upstream.ClientStatus`。
- Produces:
  - `var ErrInvalidID error`
  - `func ParseID(s string) (id uint64, serviceID int, err error)`
  - `type Config struct { Upstream *url.URL; Tsreadex string; Args []string; MaxStreams int; FirstByteTimeout time.Duration }`
  - `func New(cfg Config, log *slog.Logger) (*Streamer, error)`(`cfg.Tsreadex` を `exec.LookPath` で解決し、実行できなければエラー)
  - `func (s *Streamer) Serve(w http.ResponseWriter, r *http.Request, id uint64, serviceID int)`
  - `func testutil.StubScript(t *testing.T, body string) string`(実行可能なシェルスクリプトを作ってパスを返す)

- [ ] **Step 1: `{id}` の検証の失敗するテストを書く**

`internal/stream/id_test.go`:

```go
package stream

import "testing"

func TestParseID(t *testing.T) {
	valid := []struct {
		in        string
		id        uint64
		serviceID int
	}{
		{"3273601024", 3273601024, 1024},
		{"1", 1, 1},
		{"007", 7, 7}, // leading zeros are fine; the canonical id is used upstream
		{"65535", 65535, 65535},
		{"165535", 165535, 65535},
		{"100001", 100001, 1},
	}
	for _, tc := range valid {
		id, sid, err := ParseID(tc.in)
		if err != nil || id != tc.id || sid != tc.serviceID {
			t.Errorf("ParseID(%q) = (%d, %d, %v), want (%d, %d, nil)", tc.in, id, sid, err, tc.id, tc.serviceID)
		}
	}

	invalid := []string{
		"",
		"0",
		"100000", // serviceId 0 would turn tsreadex's service filter off (-n 0)
		"200000",
		"65536",
		"99999",
		"9999999999",
		"+1",
		"-1",
		"1a",
		" 1",
		"1 ",
		"1.5",
		"0x10",
		"12345678901", // 11 digits
		"٣", // non-ASCII digit
	}
	for _, in := range invalid {
		if _, _, err := ParseID(in); err == nil {
			t.Errorf("ParseID(%q) = ok, want an error", in)
		}
	}
}
```

- [ ] **Step 2: 失敗することを確認し、実装する**

Run: `go test ./internal/stream/ -run TestParseID -v`
Expected: FAIL(`undefined: ParseID`)

`internal/stream/id.go`:

```go
package stream

import (
	"errors"
	"strconv"
)

// Mirakurun service ids are networkId*100000 + serviceId.
const idModulus = 100000

// ErrInvalidID is returned for a malformed or out-of-range service id.
var ErrInvalidID = errors.New("invalid service id")

// ParseID validates the {id} of a request path. It returns the Mirakurun
// service id and the 16-bit serviceId that tsreadex's -n option takes.
// A serviceId of 0 is rejected: "-n 0" would disable tsreadex's service
// filter and silently turn the other options off.
func ParseID(s string) (id uint64, serviceID int, err error) {
	if len(s) == 0 || len(s) > 10 {
		return 0, 0, ErrInvalidID
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, 0, ErrInvalidID
		}
	}
	id, err = strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, 0, ErrInvalidID
	}
	serviceID = int(id % idModulus)
	if serviceID < 1 || serviceID > 65535 {
		return 0, 0, ErrInvalidID
	}
	return id, serviceID, nil
}
```

Run: `go test ./internal/stream/ -run TestParseID -v`
Expected: PASS

- [ ] **Step 3: テスト用のスタブスクリプトを追加する**

`internal/testutil/testutil.go` に追記(import に `os`、`path/filepath` を足す):

```go
// StubScript writes an executable /bin/sh script (a stand-in for tsreadex)
// into a temporary directory and returns its path.
func StubScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tsreadex-stub")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
```

- [ ] **Step 4: ストリーム処理の失敗するテストを書く**

`internal/stream/stream_test.go`:

```go
package stream

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/testutil"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// startProxy serves Streamer.Serve at /stream/{id} the way the real router does.
func startProxy(t *testing.T, upstreamURL, exe string, args []string, maxStreams int, firstByte time.Duration) *httptest.Server {
	t.Helper()
	base, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Upstream: base, Tsreadex: exe, Args: args, MaxStreams: maxStreams, FirstByteTimeout: firstByte}, discard)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, sid, err := ParseID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		s.Serve(w, r, id, sid)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// bodyUpstream answers every request with a fixed body.
func bodyUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// endlessUpstream streams TS-sized chunks until the request is cancelled, and
// closes the returned channel when its handler has returned.
func endlessUpstream(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	var once atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if once.CompareAndSwap(false, true) {
			defer close(done)
		}
		fl := w.(http.Flusher)
		chunk := bytes.Repeat([]byte{0x47}, 188)
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, done
}

// waitGone waits for the stub to write its pid, then for that process to be
// gone. A zombie still answers kill(pid, 0), so this also proves it was reaped.
func waitGone(t *testing.T, pidFile string) {
	t.Helper()
	pid := 0
	deadline := time.Now().Add(3 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pid = p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the stub never wrote its pid")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive or not reaped", pid)
}

func TestServe_PassesBytesThroughAndHidesClientHeaders(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotCookie string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		io.WriteString(w, "hello-ts-bytes")
	}))
	defer up.Close()
	proxy := startProxy(t, up.URL, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)

	req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/stream/3273601024?decode=1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 || string(body) != "hello-ts-bytes" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/MP2T" {
		t.Errorf("Content-Type = %q", ct)
	}
	if gotPath != "/api/services/3273601024/stream" || gotQuery != "decode=1" {
		t.Errorf("upstream saw %q ? %q", gotPath, gotQuery)
	}
	if gotAuth != "" || gotCookie != "" {
		t.Errorf("client headers leaked upstream: Authorization=%q Cookie=%q", gotAuth, gotCookie)
	}
}

func TestServe_RunsTsreadexWithServiceIDAndConfiguredArgs(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	stub := testutil.StubScript(t, `echo "$@" > `+argFile+"\nexec cat")
	proxy := startProxy(t, bodyUpstream(t, "x").URL, stub, []string{"-x", "18/38/39", "-b", "1"}, 4, 5*time.Second)

	resp, err := http.Get(proxy.URL + "/stream/3273601024")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	b, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "-n 1024 -x 18/38/39 -b 1 -" {
		t.Errorf("tsreadex args = %q", got)
	}
}

func TestServe_UpstreamErrors(t *testing.T) {
	var redirectHits atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectHits.Add(1) }))
	defer redirectTarget.Close()

	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"404 passes through without its body", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "SECRET-internal-detail", http.StatusNotFound)
		}, 404},
		{"503 passes through", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}, 503},
		{"redirect becomes 502 and is not followed", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
		}, 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(tc.handler)
			defer up.Close()
			proxy := startProxy(t, up.URL, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)
			resp, err := http.Get(proxy.URL + "/stream/1")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if strings.Contains(string(body), "SECRET") {
				t.Errorf("the upstream body leaked: %q", body)
			}
		})
	}
	if redirectHits.Load() != 0 {
		t.Error("the redirect target must not be requested")
	}
}

func TestServe_UpstreamUnreachableIs502(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := up.URL
	up.Close() // now refuses connections
	proxy := startProxy(t, url, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(string(body), "127.0.0.1") {
		t.Errorf("the upstream address leaked to the client: %q", body)
	}
}

func TestServe_TsreadexExitsWithoutOutputIs502(t *testing.T) {
	proxy := startProxy(t, bodyUpstream(t, "data").URL, testutil.StubScript(t, "exit 3"), nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestServe_FirstByteTimeoutIs504AndKillsTsreadex(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stub := testutil.StubScript(t, "echo $$ > "+pidFile+"\nexec sleep 30")
	proxy := startProxy(t, bodyUpstream(t, "data").URL, stub, nil, 4, 300*time.Millisecond)

	start := time.Now()
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 504 {
		t.Errorf("status = %d, want 504", resp.StatusCode)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v, the timeout is 300ms", time.Since(start))
	}
	waitGone(t, pidFile)
}

func TestServe_ClientDisconnectBeforeFirstByte(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stub := testutil.StubScript(t, "echo $$ > "+pidFile+"\nexec sleep 30")
	up, done := endlessUpstream(t)
	proxy := startProxy(t, up.URL, stub, nil, 4, 30*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/1", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("expected the client to give up before any output")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream request was not cancelled")
	}
	waitGone(t, pidFile)
}

func TestServe_ClientDisconnectKillsTsreadexAndUpstream(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stub := testutil.StubScript(t, "echo $$ > "+pidFile+"\nexec cat")
	up, done := endlessUpstream(t)
	proxy := startProxy(t, up.URL, stub, nil, 4, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 188)); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream request was not cancelled")
	}
	waitGone(t, pidFile)
}

func TestServe_MaxStreams(t *testing.T) {
	up, _ := endlessUpstream(t)
	proxy := startProxy(t, up.URL, testutil.StubScript(t, "exec cat"), nil, 1, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/1", nil)
	first, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	if _, err := io.ReadFull(first.Body, make([]byte, 188)); err != nil {
		t.Fatal(err)
	}

	second, err := http.Get(proxy.URL + "/stream/2")
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != 503 {
		t.Errorf("second stream status = %d, want 503", second.StatusCode)
	}
}

func TestServe_HugeStderrDoesNotStall(t *testing.T) {
	// 1 MiB of stderr without a newline: if it were not drained, tsreadex
	// would block on the full pipe and the stream would never start.
	stub := testutil.StubScript(t, "head -c 1048576 /dev/zero | tr '\\0' 'x' >&2\nexec cat")
	proxy := startProxy(t, bodyUpstream(t, "payload").URL, stub, nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "payload" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestServe_TsreadexFailureAfterOutputAbortsConnection(t *testing.T) {
	proxy := startProxy(t, bodyUpstream(t, "ts-data").URL, testutil.StubScript(t, "cat\nexit 5"), nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("a tsreadex failure after output must cut the connection, not end the stream cleanly")
	}
}

func TestNewFailsWhenTsreadexIsMissing(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:1")
	_, err := New(Config{Upstream: base, Tsreadex: "definitely-not-installed-tsreadex", MaxStreams: 1, FirstByteTimeout: time.Second}, discard)
	if err == nil {
		t.Fatal("expected an error for a missing executable")
	}
}

func TestLineWriter(t *testing.T) {
	var buf bytes.Buffer
	lw := newLineWriter(slog.New(slog.NewTextHandler(&buf, nil)), 1024)

	lw.Write([]byte("first line\nsecond"))
	lw.Write([]byte(" line\n"))
	lw.Write([]byte(strings.Repeat("x", 100000))) // no newline: must be truncated, not grow without bound
	lw.Write([]byte("\npartial"))
	lw.Flush()

	out := buf.String()
	for _, want := range []string{"first line", "second line", "truncated", "partial"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if len(out) > 20000 {
		t.Errorf("log output is %d bytes, the long line must be truncated", len(out))
	}
}
```

- [ ] **Step 5: テストが失敗することを確認する**

Run: `go test ./internal/stream/ -v`
Expected: FAIL(`undefined: New` などのコンパイルエラー)

- [ ] **Step 6: 実装する**

`internal/stream/stream.go`:

```go
// Package stream pipes an upstream MPEG-TS stream through tsreadex and
// serves the result, one tsreadex process per client connection.
package stream

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/upstream"
)

// Config configures a Streamer.
type Config struct {
	Upstream         *url.URL      // Mirakurun/mirakc base URL
	Tsreadex         string        // executable name or path
	Args             []string      // validated extra tsreadex arguments (no -n, no src)
	MaxStreams       int           // concurrent stream limit
	FirstByteTimeout time.Duration // how long to wait for the first output byte
}

// Streamer serves /stream/{id} requests.
type Streamer struct {
	cfg    Config
	exe    string
	client *http.Client
	slots  chan struct{}
	log    *slog.Logger
}

// New resolves the tsreadex executable (failing fast if it cannot be run).
func New(cfg Config, log *slog.Logger) (*Streamer, error) {
	exe, err := exec.LookPath(cfg.Tsreadex)
	if err != nil {
		return nil, fmt.Errorf("tsreadex executable is not usable: %w", err)
	}
	return &Streamer{
		cfg:    cfg,
		exe:    exe,
		client: upstream.NewClient(),
		slots:  make(chan struct{}, cfg.MaxStreams),
		log:    log,
	}, nil
}

// Serve streams the service id through tsreadex -n serviceID. The first-byte
// timeout covers everything up to tsreadex's first output byte, including
// opening the upstream stream.
func (s *Streamer) Serve(w http.ResponseWriter, r *http.Request, id uint64, serviceID int) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "too many streams", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(s.cfg.FirstByteTimeout, func() {
		timedOut.Store(true)
		cancel()
	})
	defer timer.Stop()

	target := upstream.URL(s.cfg.Upstream, r.URL.RawQuery, "api", "services", strconv.FormatUint(id, 10), "stream")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.fail(w, r, &timedOut, "upstream request failed", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		code := upstream.ClientStatus(resp.StatusCode)
		s.log.Warn("upstream returned an error status", "status", resp.StatusCode, "service_id", serviceID)
		http.Error(w, http.StatusText(code), code)
		return
	}

	args := append([]string{"-n", strconv.Itoa(serviceID)}, s.cfg.Args...)
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, s.exe, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = resp.Body
	stderr := newLineWriter(s.log, serviceID)
	cmd.Stderr = stderr
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.fail(w, r, &timedOut, "cannot create the tsreadex pipe", err)
		return
	}
	if err := cmd.Start(); err != nil {
		s.fail(w, r, &timedOut, "cannot start tsreadex", err)
		return
	}

	// Wait exactly once. Cancelling first guarantees the stdin copier and the
	// process cannot outlive this request.
	var (
		waited  bool
		waitErr error
	)
	wait := func() error {
		if !waited {
			waited = true
			waitErr = cmd.Wait()
			stderr.Flush()
		}
		return waitErr
	}
	defer func() {
		cancel()
		_ = wait()
	}()

	buf := make([]byte, 64<<10)
	var (
		n    int
		rerr error
	)
	for n == 0 && rerr == nil {
		n, rerr = stdout.Read(buf)
	}
	if n == 0 {
		s.fail(w, r, &timedOut, "tsreadex produced no output", rerr)
		return
	}
	timer.Stop()

	w.Header().Set("Content-Type", "video/MP2T")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if _, err := w.Write(buf[:n]); err != nil {
		return
	}
	_ = rc.Flush()
	for rerr == nil {
		n, rerr = stdout.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return // the client went away
			}
			_ = rc.Flush()
		}
	}

	// The output ended. A clean exit is a normal end of stream; a failure that
	// we did not cause must not look like one, so cut the connection.
	if err := wait(); err != nil && ctx.Err() == nil {
		s.log.Warn("tsreadex failed after producing output", "service_id", serviceID, "error", err)
		panic(http.ErrAbortHandler)
	}
}

// fail answers a failure that happened before any output was sent.
func (s *Streamer) fail(w http.ResponseWriter, r *http.Request, timedOut *atomic.Bool, msg string, err error) {
	if r.Context().Err() != nil {
		return // the client is already gone
	}
	if timedOut.Load() {
		s.log.Warn(msg+": first byte timeout", "error", err)
		http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		return
	}
	s.log.Warn(msg, "error", err)
	http.Error(w, "bad gateway", http.StatusBadGateway)
}

const maxLogLine = 8 << 10

// lineWriter logs tsreadex's stderr line by line. It never fails and never
// blocks, and it truncates lines longer than maxLogLine, so a noisy tsreadex
// can neither stall on a full pipe nor exhaust memory.
type lineWriter struct {
	log       *slog.Logger
	serviceID int
	buf       []byte
	truncated bool
}

func newLineWriter(log *slog.Logger, serviceID int) *lineWriter {
	return &lineWriter{log: log, serviceID: serviceID}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			w.Flush()
			continue
		}
		if len(w.buf) >= maxLogLine {
			w.truncated = true
			continue
		}
		w.buf = append(w.buf, b)
	}
	return len(p), nil
}

// Flush logs a pending partial line.
func (w *lineWriter) Flush() {
	if len(w.buf) == 0 {
		return
	}
	line := string(w.buf)
	if w.truncated {
		line += " ...(truncated)"
	}
	w.log.Info("tsreadex", "service_id", w.serviceID, "stderr", line)
	w.buf = w.buf[:0]
	w.truncated = false
}
```

注意: `TestLineWriter` は `newLineWriter(log, 1024)` を呼ぶ。第 2 引数は `serviceID`(int)。

- [ ] **Step 7: テストが通ることを確認する**

Run: `go test ./internal/stream/ -race -v && gofmt -l . && go vet ./...`
Expected: PASS(`-race` で警告が出ないこと)

- [ ] **Step 8: コミットする**

```bash
git add internal/stream internal/testutil/testutil.go
git commit -m "feat: tsreadexを介したストリーム配信を追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 8: HTTP ハンドラとルーティング(`internal/server`)

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: Task 5 `playlist.ReadLimited` / `playlist.Rewrite`、Task 6 `upstream.NewClient` / `upstream.URL` / `upstream.ClientStatus`、Task 7 `stream.ParseID` / `stream.Streamer`、Task 2–3 `access.NewPolicy` / `Policy.Middleware`(統合テストのみ)、Task 3 `testutil.NewServer`、Task 7 `testutil.StubScript`。
- Produces:
  - `type Deps struct { Upstream *url.URL; PublicBaseURL string; Streamer *stream.Streamer; Log *slog.Logger; MaxPlaylistBytes int64 }`(`MaxPlaylistBytes` が 0 なら 8 MiB)
  - `func New(d Deps) http.Handler`(アクセス制御は含まない。`main` が `Middleware` で包む)

- [ ] **Step 1: 失敗するテストを書く**

`internal/server/server_test.go`:

```go
package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/access"
	"github.com/onodai145/tsreadex-proxy/internal/stream"
	"github.com/onodai145/tsreadex-proxy/internal/testutil"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

const upstreamPlaylist = `#EXTM3U url-tvg="http://mirakurun:40772/api/iptv/xmltv"
#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://mirakurun:40772/api/services/3273601024/logo" group-title="GR",NHK
http://mirakurun:40772/api/services/3273601024/stream
`

// fakeUpstream imitates the Mirakurun endpoints the proxy uses and counts requests.
func fakeUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/iptv/playlist", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/x-mpegURL; charset=utf-8")
		io.WriteString(w, upstreamPlaylist)
	})
	mux.HandleFunc("/api/iptv/xmltv", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, "<tv></tv>")
	})
	mux.HandleFunc("/api/services/{id}/logo", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "PNGDATA")
	})
	mux.HandleFunc("/api/services/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "ts-bytes")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

type env struct {
	proxy *httptest.Server
	hits  *atomic.Int32
}

// newEnv wires the real router behind the real access middleware, the way
// main does, and serves it from the given (fake) remote address.
func newEnv(t *testing.T, remote, publicBase string, maxPlaylist int64) env {
	t.Helper()
	up, hits := fakeUpstream(t)
	base, _ := url.Parse(up.URL)
	streamer, err := stream.New(stream.Config{
		Upstream:         base,
		Tsreadex:         testutil.StubScript(t, "exec cat"),
		MaxStreams:       4,
		FirstByteTimeout: 5 * time.Second,
	}, discard)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := access.NewPolicy(
		[]string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
		[]string{"fc00::/7"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Upstream: base, PublicBaseURL: publicBase, Streamer: streamer, Log: discard, MaxPlaylistBytes: maxPlaylist})
	return env{proxy: testutil.NewServer(t, policy.Middleware(discard, h), remote), hits: hits}
}

func do(t *testing.T, method, url string, header http.Header, host string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if host != "" {
		req.Host = host
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const lan = "192.168.1.5:5555"

func TestPlaylistIsRewrittenToTheRequestHost(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/playlist.m3u", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	host := strings.TrimPrefix(e.proxy.URL, "http://")
	for _, want := range []string{
		`url-tvg="http://` + host + `/xmltv"`,
		`tvg-logo="http://` + host + `/logo/3273601024"`,
		"http://" + host + "/stream/3273601024",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("playlist missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "mirakurun:40772") {
		t.Errorf("an upstream URL leaked into the playlist:\n%s", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-mpegURL") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestPlaylistUsesPublicBaseURL(t *testing.T) {
	e := newEnv(t, lan, "http://tsreadex-proxy:8080", 0)
	_, body := do(t, http.MethodGet, e.proxy.URL+"/playlist.m3u", nil, "")
	if !strings.Contains(body, "http://tsreadex-proxy:8080/stream/3273601024") {
		t.Errorf("PUBLIC_BASE_URL was not used:\n%s", body)
	}
}

func TestPlaylistTooLargeIs502(t *testing.T) {
	e := newEnv(t, lan, "", 16) // far smaller than the upstream playlist
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/playlist.m3u", nil, "")
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(body, "EXTM3U") {
		t.Errorf("a truncated playlist must not be served: %q", body)
	}
}

func TestXMLTVAndLogoPassThrough(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/xmltv", nil, "")
	if resp.StatusCode != 200 || body != "<tv></tv>" || resp.Header.Get("Content-Type") != "application/xml" {
		t.Errorf("xmltv: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	resp, body = do(t, http.MethodGet, e.proxy.URL+"/logo/3273601024", nil, "")
	if resp.StatusCode != 200 || body != "PNGDATA" || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("logo: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
}

func TestStreamGetAndHead(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/stream/3273601024", nil, "")
	if resp.StatusCode != 200 || body != "ts-bytes" || resp.Header.Get("Content-Type") != "video/MP2T" {
		t.Errorf("GET: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}

	before := e.hits.Load()
	resp, body = do(t, http.MethodHead, e.proxy.URL+"/stream/3273601024", nil, "")
	if resp.StatusCode != 200 || body != "" || resp.Header.Get("Content-Type") != "video/MP2T" {
		t.Errorf("HEAD: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if e.hits.Load() != before {
		t.Error("HEAD must not contact the upstream or start tsreadex")
	}
}

func TestRouting(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/config", 404}, // Mirakurun's own management API is not exposed
		{"GET", "/api/tuners", 404},
		{"GET", "/", 404},
		{"GET", "/playlist", 404},
		{"POST", "/playlist.m3u", 405},
		{"DELETE", "/xmltv", 405},
		{"POST", "/stream/1", 405},
		{"PUT", "/logo/1", 405},
		{"GET", "/stream/abc", 400},
		{"GET", "/stream/0", 400},
		{"GET", "/stream/100000", 400},
		{"GET", "/stream/65536", 400},
		{"GET", "/stream/+1", 400},
		{"GET", "/stream/12345678901", 400},
		{"HEAD", "/stream/100000", 400},
		{"GET", "/logo/abc", 400},
		{"GET", "/stream/1/extra", 404},
	}
	for _, tc := range cases {
		resp, _ := do(t, tc.method, e.proxy.URL+tc.path, nil, "")
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
	if e.hits.Load() != 0 {
		t.Errorf("rejected requests reached the upstream %d times", e.hits.Load())
	}
}

func TestMethodNotAllowedAdvertisesAllow(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, _ := do(t, http.MethodPost, e.proxy.URL+"/playlist.m3u", nil, "")
	if resp.Header.Get("Allow") != "GET" {
		t.Errorf("Allow = %q", resp.Header.Get("Allow"))
	}
}

func TestAccessControlCoversEveryEndpoint(t *testing.T) {
	endpoints := []struct{ method, path string }{
		{"GET", "/playlist.m3u"},
		{"GET", "/xmltv"},
		{"GET", "/logo/3273601024"},
		{"GET", "/stream/3273601024"},
		{"HEAD", "/stream/3273601024"},
		{"GET", "/api/config"}, // even paths that do not exist are guarded
	}

	t.Run("source outside the permitted ranges is dropped", func(t *testing.T) {
		e := newEnv(t, "8.8.8.8:1234", "", 0)
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
		for _, ep := range endpoints {
			req, _ := http.NewRequest(ep.method, e.proxy.URL+ep.path, nil)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
				t.Errorf("%s %s answered %d instead of dropping the connection", ep.method, ep.path, resp.StatusCode)
			}
		}
		if e.hits.Load() != 0 {
			t.Error("the upstream must not be contacted")
		}
	})

	t.Run("forbidden Origin, Referer and Host get 403", func(t *testing.T) {
		e := newEnv(t, lan, "", 0)
		bad := []struct {
			name   string
			header http.Header
			host   string
		}{
			{"origin", http.Header{"Origin": {"http://evil.example"}}, ""},
			{"referer", http.Header{"Referer": {"http://evil.example/"}}, ""},
			{"host", nil, "evil.example"},
		}
		for _, ep := range endpoints {
			for _, b := range bad {
				resp, _ := do(t, ep.method, e.proxy.URL+ep.path, b.header, b.host)
				if resp.StatusCode != 403 {
					t.Errorf("%s %s with bad %s = %d, want 403", ep.method, ep.path, b.name, resp.StatusCode)
				}
			}
		}
		if e.hits.Load() != 0 {
			t.Error("the upstream must not be contacted")
		}
	})
}

func TestNoCORSHeaders(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	for _, path := range []string{"/playlist.m3u", "/xmltv", "/logo/1", "/stream/1"} {
		resp, _ := do(t, http.MethodGet, e.proxy.URL+path, http.Header{"Origin": {"http://localhost:3000"}}, "")
		for k := range resp.Header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: unexpected CORS header %s", path, k)
			}
		}
	}
}

func TestUpstreamErrorStatusPassesThroughWithoutBody(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "SECRET-internal-detail", http.StatusInternalServerError)
	}))
	defer failing.Close()
	base, _ := url.Parse(failing.URL)
	streamer, err := stream.New(stream.Config{Upstream: base, Tsreadex: testutil.StubScript(t, "exec cat"), MaxStreams: 1, FirstByteTimeout: time.Second}, discard)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Deps{Upstream: base, Streamer: streamer, Log: discard}))
	defer srv.Close()

	for _, path := range []string{"/playlist.m3u", "/xmltv", "/logo/1"} {
		resp, body := do(t, http.MethodGet, srv.URL+path, nil, "")
		if resp.StatusCode != 500 {
			t.Errorf("%s: status = %d, want 500", path, resp.StatusCode)
		}
		if strings.Contains(body, "SECRET") || strings.Contains(body, base.Host) {
			t.Errorf("%s: the upstream detail leaked: %q", path, body)
		}
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/server/ -v`
Expected: FAIL(`undefined: New` などのコンパイルエラー)

- [ ] **Step 3: 最小の実装を書く**

`internal/server/server.go`:

```go
// Package server routes the proxy's four endpoints. Access control is applied
// around it by the caller (see access.Policy.Middleware).
package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/onodai145/tsreadex-proxy/internal/playlist"
	"github.com/onodai145/tsreadex-proxy/internal/stream"
	"github.com/onodai145/tsreadex-proxy/internal/upstream"
)

const defaultMaxPlaylistBytes = 8 << 20

// Deps are the collaborators of the router.
type Deps struct {
	Upstream         *url.URL
	PublicBaseURL    string // empty: derive from the (validated) request Host
	Streamer         *stream.Streamer
	Log              *slog.Logger
	MaxPlaylistBytes int64 // 0: 8 MiB
}

type handler struct {
	Deps
	client *http.Client
}

// New returns the router. Only the paths below exist; everything else is 404.
func New(d Deps) http.Handler {
	if d.MaxPlaylistBytes == 0 {
		d.MaxPlaylistBytes = defaultMaxPlaylistBytes
	}
	h := &handler{Deps: d, client: upstream.NewClient()}
	mux := http.NewServeMux()
	mux.HandleFunc("/playlist.m3u", getOnly(h.playlist))
	mux.HandleFunc("/xmltv", getOnly(h.xmltv))
	mux.HandleFunc("/logo/{id}", getOnly(h.logo))
	mux.HandleFunc("/stream/{id}", h.stream)
	return mux
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// fetch requests an upstream path. It sends no client headers. On failure it
// has already answered the client and returns nil.
func (h *handler) fetch(w http.ResponseWriter, r *http.Request, elem ...string) *http.Response {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream.URL(h.Upstream, "", elem...), nil)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return nil
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			h.Log.Warn("upstream request failed", "error", err)
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		h.Log.Warn("upstream returned an error status", "status", resp.StatusCode, "path", req.URL.Path)
		code := upstream.ClientStatus(resp.StatusCode)
		http.Error(w, http.StatusText(code), code)
		return nil
	}
	return resp
}

func (h *handler) playlist(w http.ResponseWriter, r *http.Request) {
	resp := h.fetch(w, r, "api", "iptv", "playlist")
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	src, err := playlist.ReadLimited(resp.Body, h.MaxPlaylistBytes)
	if err != nil {
		h.Log.Warn("cannot read the upstream playlist", "error", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/x-mpegURL; charset=utf-8")
	io.WriteString(w, playlist.Rewrite(src, h.publicBase(r)))
}

// publicBase is PUBLIC_BASE_URL, or the scheme and Host of this request. The
// Host has already passed the access middleware; X-Forwarded-* is ignored.
func (h *handler) publicBase(r *http.Request) string {
	if h.PublicBaseURL != "" {
		return h.PublicBaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *handler) xmltv(w http.ResponseWriter, r *http.Request) {
	h.passThrough(w, r, "application/xml", "api", "iptv", "xmltv")
}

func (h *handler) logo(w http.ResponseWriter, r *http.Request) {
	id, _, err := stream.ParseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h.passThrough(w, r, "application/octet-stream", "api", "services", itoa(id), "logo")
}

// passThrough relays an upstream response body with its Content-Type.
func (h *handler) passThrough(w http.ResponseWriter, r *http.Request, defaultType string, elem ...string) {
	resp := h.fetch(w, r, elem...)
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = defaultType
	}
	w.Header().Set("Content-Type", ct)
	io.Copy(w, resp.Body)
}

func (h *handler) stream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, serviceID, err := stream.ParseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/MP2T")
		return
	}
	h.Streamer.Serve(w, r, id, serviceID)
}

func itoa(id uint64) string { return strconv.FormatUint(id, 10) }
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/server/ -race -v && gofmt -l . && go vet ./...`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/server
git commit -m "feat: HTTPハンドラとルーティングを追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 9: エントリポイント(`cmd/tsreadex-proxy`)

**Files:**
- Create: `cmd/tsreadex-proxy/main.go`

**Interfaces:**
- Consumes: Task 1 `config.Load`、Task 2–4 `access.NewPolicy` / `Middleware` / `Listen` / `SystemAddrs`、Task 7 `stream.New`、Task 8 `server.New`。
- Produces: 実行ファイル `tsreadex-proxy`。

- [ ] **Step 1: 実装を書く**

`cmd/tsreadex-proxy/main.go`:

```go
// Command tsreadex-proxy serves Mirakurun/mirakc live streams through
// tsreadex over HTTP, for use as a Jellyfin Live TV tuner.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/access"
	"github.com/onodai145/tsreadex-proxy/internal/config"
	"github.com/onodai145/tsreadex-proxy/internal/server"
	"github.com/onodai145/tsreadex-proxy/internal/stream"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tsreadex-proxy:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	policy, err := access.NewPolicy(cfg.AllowIPv4, cfg.AllowIPv6, cfg.AllowedHostname, cfg.AllowOrigins)
	if err != nil {
		return err
	}
	streamer, err := stream.New(stream.Config{
		Upstream:         cfg.MirakurunURL,
		Tsreadex:         cfg.TsreadexPath,
		Args:             cfg.TsreadexArgs,
		MaxStreams:       cfg.MaxStreams,
		FirstByteTimeout: cfg.FirstByteTimeout,
	}, log)
	if err != nil {
		return err
	}
	handler := policy.Middleware(log, server.New(server.Deps{
		Upstream:      cfg.MirakurunURL,
		PublicBaseURL: cfg.PublicBaseURL,
		Streamer:      streamer,
		Log:           log,
	}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listeners, err := access.Listen(ctx, policy, cfg.ListenPort, log, access.SystemAddrs, 5*time.Second)
	if err != nil {
		return err
	}
	if len(listeners) == 0 {
		return errors.New("no listener could be started")
	}

	// No write timeout: streams are long-lived.
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func() { errc <- srv.Serve(ln) }()
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			srv.Close()
			return err
		}
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close() // streams that outlive the grace period are cut
	}
	return nil
}
```

- [ ] **Step 2: ビルドと静的チェックを通す**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./... -race`
Expected: すべて成功、`gofmt -l .` は何も出力しない

- [ ] **Step 3: 起動の動作確認(手動)**

```bash
BIN="$(mktemp -d)/tsreadex-proxy"
go build -o "$BIN" ./cmd/tsreadex-proxy

# 1) 必須設定がないと、メッセージを出して終了コード 1 で終わる
"$BIN"; echo "exit=$?"
# 期待: "tsreadex-proxy: MIRAKURUN_URL is required" と exit=1

# 2) tsreadex が実行できないと、起動時に失敗する
MIRAKURUN_URL=http://127.0.0.1:1 TSREADEX_PATH=/nonexistent "$BIN"; echo "exit=$?"
# 期待: "tsreadex executable is not usable" と exit=1

# 3) 起動して、許可範囲外の Host が 403 になること、不正な id が 400 になること、
#    上流に届かない場合に 502 になることを確認する(cat を tsreadex の代わりに使う)
MIRAKURUN_URL=http://127.0.0.1:1 TSREADEX_PATH=/bin/cat LISTEN_PORT=18080 "$BIN" &
PID=$!
sleep 1
curl -s -o /dev/null -w 'playlist=%{http_code}\n' http://127.0.0.1:18080/playlist.m3u              # 期待: 502
curl -s -o /dev/null -w 'bad-id=%{http_code}\n'   http://127.0.0.1:18080/stream/100000            # 期待: 400
curl -s -o /dev/null -w 'evil-host=%{http_code}\n' -H 'Host: evil.example' http://127.0.0.1:18080/playlist.m3u  # 期待: 403
curl -s -o /dev/null -w 'unknown=%{http_code}\n'  http://127.0.0.1:18080/api/config               # 期待: 404
kill -TERM $PID; wait $PID; echo "exit=$?"                                                          # 期待: exit=0
```

Expected: コメントの「期待」のとおり。3) で起動しない場合は、ログに `no permitted non-loopback IPv4 address yet; retrying` が出ていないか確認する(許可範囲の LAN アドレスがない環境では、仕様どおり待機する)。

- [ ] **Step 4: コミットする**

```bash
git add cmd
git commit -m "feat: エントリポイントを追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Docker、compose サンプル、README

**Files:**
- Create: `Dockerfile`, `.dockerignore`, `docker-compose.example.yml`, `README.md`

**Interfaces:**
- Consumes: Task 9 のバイナリ。
- Produces: コンテナイメージ、compose サンプル、利用者向けドキュメント(実機検証の手順を含む)。

- [ ] **Step 1: Dockerfile と .dockerignore を書く**

`Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1
ARG GO_VERSION=1.23
ARG TSREADEX_REF=master-260428

# tsreadex は依存のない C++11。静的リンクして scratch に載せる。
FROM alpine:3.20 AS tsreadex
ARG TSREADEX_REF
RUN apk add --no-cache build-base git
RUN git clone --depth 1 --branch "${TSREADEX_REF}" https://github.com/xtne6f/tsreadex.git /src
WORKDIR /src
RUN make LDFLAGS="-static -s"

FROM golang:${GO_VERSION}-alpine AS proxy
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tsreadex-proxy ./cmd/tsreadex-proxy

FROM scratch
COPY --from=tsreadex /src/tsreadex /usr/local/bin/tsreadex
COPY --from=proxy /out/tsreadex-proxy /usr/local/bin/tsreadex-proxy
ENV PATH=/usr/local/bin
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tsreadex-proxy"]
```

`.dockerignore`:

```
.git
docs
*.md
/tsreadex-proxy
/bin
```

- [ ] **Step 2: イメージをビルドして確認する**

```bash
docker build -t tsreadex-proxy:dev .
docker run --rm tsreadex-proxy:dev; echo "exit=$?"
# 期待: "tsreadex-proxy: MIRAKURUN_URL is required" と exit=1
docker run --rm --entrypoint /usr/local/bin/tsreadex tsreadex-proxy:dev 2>&1 | head -3
# 期待: tsreadex の使用法、または引数不足のメッセージ(実行できること自体が確認項目)
docker run --rm --entrypoint /usr/local/bin/tsreadex tsreadex-proxy:dev --help 2>&1 | head -1; true
docker image inspect tsreadex-proxy:dev --format '{{.Config.User}}'
# 期待: 65532:65532
```

Expected: 上記の「期待」のとおり。ネットワークに到達できず `docker build` が失敗する環境では、その旨を報告し、このステップは「未検証」として README の検証手順に残す(失敗を成功と報告しないこと)。

- [ ] **Step 3: compose サンプルを書く**

`docker-compose.example.yml`:

```yaml
# Jellyfin と Mirakurun/mirakc が参加している Docker ネットワークに、
# このプロキシを参加させる例。プロキシの ports は公開しない(LAN 内専用)。
services:
  tsreadex-proxy:
    build: .
    image: tsreadex-proxy:latest
    restart: unless-stopped
    environment:
      # Mirakurun/mirakc の URL(同じネットワーク上のサービス名)。
      MIRAKURUN_URL: http://mirakurun:40772
      # Jellyfin が到達する際のホスト名(このサービス名)。Host 検証に必要。
      ALLOWED_HOSTNAME: tsreadex-proxy
      # 必要なら調整(既定値は README の表を参照)。
      # TSREADEX_ARGS: "-x 18/38/39 -b 1 -c 1 -u 2 -d 13"
      # MAX_STREAMS: "8"
    networks:
      - tv

networks:
  tv:
    # Jellyfin と Mirakurun が参加している既存のネットワーク名に置き換える。
    external: true
    name: tv
```

- [ ] **Step 4: README を書く**

`README.md`:

````markdown
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

| 環境変数 | 既定値 | 説明 |
|---|---|---|
| `MIRAKURUN_URL` | (必須) | 例: `http://mirakurun:40772`。mirakc も Mirakurun 互換 API なので同じ変数で指定します |
| `LISTEN_PORT` | `8080` | 待ち受けポート。待ち受けアドレスは設定できず、許可範囲のアドレスから自動で決まります |
| `PUBLIC_BASE_URL` | (空) | M3U に書き込むこのプロキシの URL。空ならリクエストの Host から導出します |
| `TSREADEX_PATH` | `tsreadex` | |
| `TSREADEX_ARGS` | `-x 18/38/39 -b 1 -c 1 -u 2 -d 13` | 空白区切り。`-n`、`-z`、`-s`、`-r` と位置引数は指定できません(`-n` はプロキシが付与します) |
| `MAX_STREAMS` | `8` | 同時ストリーム数の上限(超えると 503) |
| `FIRST_BYTE_TIMEOUT` | `15s` | tsreadex の出力開始待ち(超えると 504) |
| `ALLOW_IPV4_CIDR_RANGES` | `10.0.0.0/8,127.0.0.0/8,172.16.0.0/12,192.168.0.0/16` | 許可する送信元 IPv4(Mirakurun の既定と同じ) |
| `ALLOW_IPV6_CIDR_RANGES` | `fc00::/7` | 許可する送信元 IPv6(Mirakurun の既定と同じ) |
| `ALLOW_ORIGINS` | (空) | `Origin` / `Referer` を追加で許可するオリジン(カンマ区切り) |
| `ALLOWED_HOSTNAME` | (空) | クライアントがこのプロキシに到達する際のホスト名。`localhost` と許可範囲の IP は常に許可されます |

## セキュリティ

セキュリティ水準は Mirakurun と同等です。認証と TLS はなく、**信頼された LAN 内専用**です。

- 許可範囲外の送信元は、応答せずに接続を閉じます。許可範囲のアドレスでのみ待ち受け、`0.0.0.0` では待ち受けません。パブリック IP を持つサーバーで動かしても、そのアドレスでは待ち受けません。`ALLOW_*_CIDR_RANGES` にパブリック IP の範囲を入れないでください。
- `Origin`、`Referer`、`Host` が許可されていなければ 403 です(DNS Rebinding / CSRF 対策)。`ALLOWED_HOSTNAME` を設定し忘れると、Jellyfin がサービス名でアクセスしたときに 403 になります。ログに拒否理由が出ます。
- 公開するのは 4 つのエンドポイントだけです。Mirakurun の管理 API には到達できません。
- **リバースプロキシの下に置かないでください。** Mirakurun のドキュメントのとおり、リバースプロキシ(特に HTTPS 化)はブラウザや Mirakurun の保護を迂回する可能性があり、危険度が増します。リバースプロキシの下では送信元アドレスがプロキシのものになり、IP 制限も意味を失います。外部から使う場合は、VPN や SSH トンネルを使ってください。
- Mirakurun 側の `allowIPv4CidrRanges` を既定から絞っている場合は、このプロキシの送信元アドレスも許可に含めてください。
- tsreadex は C++ の TS パーサで、上流(放送波由来)の入力を処理します。メモリ安全性の問題がありうるため、最新版に追従してください。

## ビルド

```bash
go build -o tsreadex-proxy ./cmd/tsreadex-proxy
go test ./... -race
docker build -t tsreadex-proxy .   # tsreadex は ARG TSREADEX_REF のタグからビルドされます
```

テストは tsreadex の代わりにシェルスクリプトのスタブを使うため、実際の tsreadex は不要です(`/bin/sh` が必要です)。

## 実機での確認手順

自動テストでは確認できない項目です。初回の導入時に確認してください。

- [ ] Jellyfin でチャンネルを選局して、映像と音声が出る。選局から再生開始までの時間を記録する(Jellyfin のプローブがタイムアウトしないか)。
- [ ] 副音声のある番組で音声が切り替えられる。字幕のある番組で字幕が出る(`-d 13` が Jellyfin 同梱の ffmpeg で動くか)。
- [ ] チャンネルロゴが Jellyfin に表示される(M3U の `tvg-logo` に従うか)。
- [ ] 視聴中にクライアントを閉じると、`ps` から tsreadex プロセスが消え、Mirakurun のチューナーが解放される。
- [ ] 視聴を止めたあと、上流が止まる・tsreadex が出力しなくなる場合にプロセスが残らないか確認する。残る場合は `TSREADEX_ARGS` に `-m 2 -t 10` を加える効果を確認し、必要なら出力アイドルタイムアウトの追加を検討する(スペック §11)。
- [ ] tsreadex が入力終了で終了コード 0 になること(非 0 だと、ストリームの正常な終了が接続断として扱われます)。
- [ ] `ALLOWED_HOSTNAME` を外すと、Jellyfin からのアクセスが 403 になり、ログに `reason=host` が出る。
- [ ] 許可範囲外のホストから接続すると、応答なしで切断される。Docker のポートを公開した構成でも、`RemoteAddr` が実際のクライアントのアドレスになっているか(スペック §11)。
- [ ] 許可範囲外のアドレスで待ち受けていないこと(`ss -ltn` で待ち受けアドレスを確認する)。
````

- [ ] **Step 5: 全体の最終確認をする**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./... -race -count=1`
Expected: すべて成功、`gofmt -l .` は何も出力しない

- [ ] **Step 6: コミットする**

```bash
git add Dockerfile .dockerignore docker-compose.example.yml README.md
git commit -m "feat: Dockerfileとドキュメントを追加" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

---

## Self-Review(計画の作成者が実施)

**1. スペックの網羅**

| スペック | タスク |
|---|---|
| §1 成功基準(M3U/XMLTV の登録、選局、切断時のプロセス解放) | 8、7、10(実機手順) |
| §3 エンドポイント、`{id}` 検証、HEAD | 7(`ParseID`)、8 |
| §4 M3U 書き換え、8MiB 上限、`PUBLIC_BASE` の導出 | 5、8 |
| §5 ストリーム処理(1〜6) | 7 |
| §6 設定、禁止オプション、起動時の実行可否確認 | 1、7(`New`)、9 |
| §7 エラー処理(表の全行) | 7、8、3、4 |
| §8 構成 | File Structure |
| §9 テスト(単体・統合) | 各タスク。実機は 10 の README |
| §10 配布(マルチステージ、非 root、ports 非公開、`ALLOWED_HOSTNAME`) | 10 |
| §11 リスク(Docker の送信元 IP、`-m 2`、stall、`ALLOWED_HOSTNAME` 忘れ) | 10 の実機手順、3(拒否理由のログ) |
| §12.2 待ち受けアドレス | 4 |
| §12.3 アクセス制御ミドルウェア(1〜4、CORS、`nosniff`、アクセスログ) | 2、3 |
| §12.4 その他の対策(PATH のみ、ヘッダ非転送、リダイレクト非追従、本文非返却) | 6、7、8 |
| §12.7 対応表のテスト | 3、4、8(`TestAccessControlCoversEveryEndpoint`、`TestRouting`、`TestNoCORSHeaders`) |

ギャップ: なし。スペックの「同名のフラグ」は、環境変数名の小文字ケバブケースと解釈し、Global Constraints と README に明記した。

**2. プレースホルダのスキャン:** TBD / TODO / 「同様に」などの記述は残っていない。すべてのコードステップに実際のコードを載せた。

**3. 型・シグネチャの整合:**
- `stream.New(cfg Config, log)`、`Serve(w, r, id uint64, serviceID int)`、`ParseID(s) (uint64, int, error)`: Task 7 の定義と Task 8 の使用が一致。
- `access.NewPolicy(v4, v6 []string, hostname string, origins []string)`: Task 2、8、9 で一致。
- `Policy.Middleware(log, next)`: Task 3、8、9 で一致。
- `upstream.URL(base, rawQuery, elem...)`: Task 6、7、8 で一致。
- `newLineWriter(log, serviceID int)`: Task 7 のテストと実装で一致。
- `testutil.NewServer(t, h, remote)` と `StubScript(t, body)`: Task 3、7、8 で一致。

**4. Review Focus:** 上の 6 項目すべてに、担当タスクのテストを入れた(各行の末尾に名前を記載)。

## 実行に関する注意

- Task 7 のテストは `/bin/sh`、`head`、`tr`、`sleep`、`cat` と Linux の `syscall.Kill` を使う。macOS / Windows では動かない。
- Task 4 のテストは、`127.0.0.2` が bind できる(Linux の標準)ことを前提とする。
- Task 10 の `docker build` は、ネットワーク(Docker Hub と GitHub)が必要。
