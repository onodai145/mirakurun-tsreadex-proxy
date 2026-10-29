# デュアルモノ検出のデバッグログ Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `LOG_LEVEL=debug` のとき、視聴中のストリームの音声がデュアルモノかどうかをログに出す。

**Architecture:** 上流の `resp.Body` を `io.TeeReader` で分け、新パッケージ `internal/tsprobe` の `Probe`(`io.Writer`)にその場で渡す。`Probe` は PAT → PMT → 音声 PES の先頭の ADTS ヘッダだけを読み、音声 ES ごとに 1 回結果を返して止まる。debug 以外では分岐を付けず、経路は今のまま。`LOG_LEVEL` は `internal/config` と `main.go` に新設する。

**Tech Stack:** Go(標準ライブラリのみ)、`log/slog`、`testing`。

**Spec:** [`docs/superpowers/specs/2026-10-29-log-dual-mono-design.md`](../specs/2026-10-29-log-dual-mono-design.md)

## Global Constraints

- ブランチは `feat/log-dual-mono`(作成済み)。`main` には直接コミットしない。
- 外部依存を足さない。`internal/tsprobe` は標準ライブラリだけで書く(仕様書 §4)。
- 解析の上限は累計 8 MiB。止まったあとの `Write` は即座に戻る(仕様書 §4)。
- `Probe.Write` は決してエラーを返さず、panic を外へ出さない(仕様書 §5)。
- `LOG_LEVEL` は `debug` / `info` / `warn` / `error`(大文字小文字を区別しない)、既定は `info`、それ以外は起動時のエラー(仕様書 §4)。
- 判定は「channel_configuration=0、samplingFrequencyIndex が 3〜5、先頭の id_syn_ele が SCE」。SCE の個数は数えない(仕様書 §3)。
- 公開するエンドポイントを増やさない。認証・TLS に関わる前提を変えない(AGENTS.md)。
- 設定項目を足したら README の設定表も更新する(AGENTS.md)。
- コミットメッセージは Conventional Commits、説明は日本語で、**件名のみ**(本文なし)。セッションの指示にある `Co-Authored-By` のトレーラーは別に付く。`--no-verify` / `--no-gpg-sign` は使わない。`git commit` が失敗または時間切れになったら、再試行せずに報告する。
- 各タスクの終わりに `gofmt -l .`(出力なし)、`go vet ./...`、`go test ./... -race` が通ること。Linux 専用。

## Review Focus

仕様書が暗に求めているが、タスクのテストが自然には踏まない入力を、起きやすい順に並べる。各行のテストは、担当タスクに入れてある。

1. **パケット境界の途中から始まる入力や、1 バイトずつ届く入力。** 同じ結果になること。(Task 3 `TestProbe_SameResultForAnyChunking`)
2. **音声が来ない、番組が PAT にない、同期できない、上限に達する。** 黙って終わらず、理由がログに出ること。(Task 3 `TestProbe_GivesUp`、Task 4 `TestServe_DebugLogsWhyTheProbeGaveUp`)
3. **壊れた入力や途切れた入力。** パニックしないこと。コールバックが panic しても配信が止まらないこと。(Task 3 `TestProbe_WriteNeverPanics`)
4. **info 以上のとき、配信のバイト列もログも今までと変わらないこと。** (Task 4 `TestServe_InfoLevelDoesNotProbe`)
5. **不正な `LOG_LEVEL`。** 起動時にエラーになり、フラグが環境変数より優先されること。(Task 1 `TestLoadLogLevel`)

---

### Task 1: `LOG_LEVEL` を追加する

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`(末尾に追加。`import` に `"log/slog"` を足す)
- Modify: `cmd/mirakurun-tsreadex-proxy/main.go:38`
- Modify: `README.md`(設定表に 1 行)

**Interfaces:**
- Consumes: なし。
- Produces: `config.Config.LogLevel slog.Level`(`Load` が設定する)。Task 4 は `slog.Logger` の `Enabled(ctx, slog.LevelDebug)` で間接的にこれを使う。

- [ ] **Step 1: 失敗するテストを書く**

`internal/config/config_test.go` の `import` に `"log/slog"` を足し(`"reflect"` の前)、ファイルの末尾に次を追加する。

```go
func TestLoadLogLevel(t *testing.T) {
	base := map[string]string{"MIRAKURUN_URL": "http://m:40772"}
	with := func(level string) map[string]string {
		m := map[string]string{"LOG_LEVEL": level}
		for k, v := range base {
			m[k] = v
		}
		return m
	}

	c, err := Load(nil, env(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != slog.LevelInfo {
		t.Errorf("default LogLevel = %v, want INFO", c.LogLevel)
	}

	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError, "DEBUG": slog.LevelDebug,
	} {
		c, err := Load(nil, env(with(in)))
		if err != nil {
			t.Errorf("LOG_LEVEL=%s: %v", in, err)
			continue
		}
		if c.LogLevel != want {
			t.Errorf("LOG_LEVEL=%s: LogLevel = %v, want %v", in, c.LogLevel, want)
		}
	}

	c, err = Load([]string{"-log-level", "warn"}, env(with("debug")))
	if err != nil || c.LogLevel != slog.LevelWarn {
		t.Errorf("the flag must win over the environment: %v, %v", c, err)
	}

	for _, bad := range []string{"trace", "verbose", "1"} {
		if _, err := Load(nil, env(with(bad))); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
			t.Errorf("LOG_LEVEL=%s: err = %v, want a LOG_LEVEL error", bad, err)
		}
	}
}
```

- [ ] **Step 2: 失敗することを確かめる**

Run: `go test ./internal/config -run TestLoadLogLevel -count=1`
Expected: FAIL(コンパイルエラー: `c.LogLevel undefined`)

- [ ] **Step 3: 実装する**

`internal/config/config.go` を次のとおり直す。

(a) `import` に `"log/slog"` を足す(`"io"` の次)。

(b) `Config` 構造体の最後に追加する。

```go
	AllowedHostname  string
	LogLevel         slog.Level
}
```

(c) `Load` の `hostname := ...` の次の行に追加する。

```go
	logLevel := str("log-level", "LOG_LEVEL", "info")
```

(d) `Load` の末尾の `return c, nil` の直前に追加する。

```go
	if c.LogLevel, err = parseLogLevel(*logLevel); err != nil {
		return nil, err
	}
```

(e) `Load` の次に関数を追加する。

```go
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL: %q must be one of debug, info, warn, error", s)
}
```

`cmd/mirakurun-tsreadex-proxy/main.go` の 38 行目を次に変える。

```go
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
```

`README.md` の設定表の `ALLOWED_HOSTNAME` の行の次に、次の行を足す。

```markdown
| `LOG_LEVEL` | `info` | ログレベル(`debug` / `info` / `warn` / `error`)。`warn` 以上にすると、tsreadex の stderr などの Info ログも出なくなります。`debug` で何が出るかは「デバッグログ」を参照 |
```

(「デバッグログ」の節は Task 4 で足す。)

- [ ] **Step 4: 通ることを確かめる**

Run: `gofmt -l . && go vet ./... && go test ./... -race -count=1`
Expected: `gofmt` の出力なし、すべて `ok`

- [ ] **Step 5: コミットする**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/mirakurun-tsreadex-proxy/main.go README.md
git commit -m "feat: ログレベルを LOG_LEVEL で設定できるようにする"
```

---

### Task 2: テスト用の TS ビルダーと ADTS の判定

**Files:**
- Create: `internal/testutil/ts.go`
- Create: `internal/tsprobe/adts.go`
- Test: `internal/tsprobe/adts_test.go`

**Interfaces:**
- Consumes: なし。
- Produces:
  - `testutil.ADTSFrame(channelConfig, samplingIndex int, protectionAbsent bool, firstElement int) []byte`
  - `testutil.AudioES{PID uint16; StreamType byte; Frame []byte}`(`StreamType` が 0 なら 0x0F、`Frame` が nil なら PMT に載せるだけで PES は送らない)
  - `testutil.TSOptions{ServiceID int; PMTPID uint16; ExtraPMTBytes int; Audio []AudioES}`
  - `testutil.BuildTS(TSOptions) []byte`(PAT、PMT、各音声の PES を並べる。CRC は 0 のまま)
  - `testutil.NullPackets(n int) []byte`
  - `tsprobe.parseADTS(b []byte) (adtsInfo, bool)`(パッケージ内。`adtsInfo{channelConfig int; dualMono bool}`)

テスト用の TS ビルダーは、`tsprobe` と `stream` の両方のテストが使うので、共通のテスト用パッケージ `internal/testutil` に置く。

- [ ] **Step 1: ビルダーを書く**

`internal/testutil/ts.go` を作る。

```go
package testutil

// ADTSFrame builds the start of an ADTS frame: the 7-byte header (9 bytes with
// the CRC field when protectionAbsent is false) followed by one byte whose top
// three bits are firstElement, the id_syn_ele of the first syntactic element
// (0 = SCE, 1 = CPE, 5 = PCE). The rest of the frame is not needed by the
// tests and is left out, so frame_length is only nominal.
func ADTSFrame(channelConfig, samplingIndex int, protectionAbsent bool, firstElement int) []byte {
	b := make([]byte, 7)
	b[0] = 0xFF
	b[1] = 0xF0 // syncword low nibble, MPEG-4, layer 0
	if protectionAbsent {
		b[1] |= 1
	}
	b[2] = 0x40 | byte(samplingIndex&0xF)<<2 | byte(channelConfig>>2)&1 // AAC-LC
	b[3] = byte(channelConfig&3) << 6
	b[6] = 0xFC
	if !protectionAbsent {
		b = append(b, 0, 0)
	}
	return append(b, byte(firstElement&7)<<5)
}

// AudioES is one elementary stream of a test transport stream.
type AudioES struct {
	PID        uint16
	StreamType byte   // 0 means 0x0F (ADTS)
	Frame      []byte // first PES payload; nil means "listed in the PMT, never sent"
}

// TSOptions describes the transport stream built by BuildTS.
type TSOptions struct {
	ServiceID     int
	PMTPID        uint16 // 0 means 0x1000
	ExtraPMTBytes int    // bytes of private descriptors, to make the PMT span packets
	Audio         []AudioES
}

// BuildTS builds a short MPEG-TS: a PAT, a PMT (as many packets as it needs),
// then one PES packet per audio stream that has a Frame. Table CRCs are left
// zero because the code under test does not check them.
func BuildTS(o TSOptions) []byte {
	pmtPID := o.PMTPID
	if pmtPID == 0 {
		pmtPID = 0x1000
	}

	pat := []byte{0x00, 0xB0, 0, 0x00, 0x01, 0xC1, 0x00, 0x00}
	pat = append(pat, byte(o.ServiceID>>8), byte(o.ServiceID), 0xE0|byte(pmtPID>>8), byte(pmtPID))
	pat = append(pat, 0, 0, 0, 0)
	setSectionLength(pat)

	var desc []byte
	for left := o.ExtraPMTBytes; left > 0; {
		n := min(left, 200)
		desc = append(desc, 0x80, byte(n))
		desc = append(desc, make([]byte, n)...)
		left -= n
	}
	pmt := []byte{0x02, 0xB0, 0, byte(o.ServiceID >> 8), byte(o.ServiceID), 0xC1, 0x00, 0x00, 0xE1, 0x00}
	pmt = append(pmt, 0xF0|byte(len(desc)>>8), byte(len(desc)))
	pmt = append(pmt, desc...)
	for _, a := range o.Audio {
		st := a.StreamType
		if st == 0 {
			st = 0x0F
		}
		pmt = append(pmt, st, 0xE0|byte(a.PID>>8), byte(a.PID), 0xF0, 0x00)
	}
	pmt = append(pmt, 0, 0, 0, 0)
	setSectionLength(pmt)

	var ts []byte
	ts = append(ts, psiPackets(0, pat)...)
	ts = append(ts, psiPackets(pmtPID, pmt)...)
	for _, a := range o.Audio {
		if a.Frame != nil {
			ts = append(ts, pesPacket(a.PID, a.Frame)...)
		}
	}
	return ts
}

// NullPackets returns n null packets (PID 0x1FFF).
func NullPackets(n int) []byte {
	ts := make([]byte, 0, n*188)
	for range n {
		ts = append(ts, packet(0x1FFF, false, nil)...)
	}
	return ts
}

func setSectionLength(sec []byte) {
	n := len(sec) - 3
	sec[1] = sec[1]&0xF0 | byte(n>>8)
	sec[2] = byte(n)
}

// psiPackets carries a section in as many packets as it needs.
func psiPackets(pid uint16, section []byte) []byte {
	payload := append([]byte{0}, section...) // pointer_field
	var out []byte
	for first := true; len(payload) > 0; first = false {
		n := min(len(payload), 184)
		chunk := append([]byte(nil), payload[:n]...)
		for len(chunk) < 184 {
			chunk = append(chunk, 0xFF)
		}
		out = append(out, packet(pid, first, chunk)...)
		payload = payload[n:]
	}
	return out
}

// pesPacket puts a PES header (with a PTS) and the payload in one packet.
func pesPacket(pid uint16, payload []byte) []byte {
	pes := []byte{0, 0, 1, 0xC0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}
	return packet(pid, true, append(pes, payload...))
}

// packet builds one 188-byte packet. A payload shorter than 184 bytes is
// padded with adaptation-field stuffing.
func packet(pid uint16, pusi bool, payload []byte) []byte {
	if len(payload) > 184 {
		panic("testutil: payload does not fit in one packet")
	}
	p := make([]byte, 0, 188)
	b1 := byte(pid >> 8)
	if pusi {
		b1 |= 0x40
	}
	need := 184 - len(payload)
	afc := byte(0x10)
	if need > 0 {
		afc = 0x30
	}
	p = append(p, 0x47, b1, byte(pid), afc)
	if need > 0 {
		p = append(p, byte(need-1))
		if need > 1 {
			p = append(p, 0x00)
			for range need - 2 {
				p = append(p, 0xFF)
			}
		}
	}
	return append(p, payload...)
}
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/tsprobe/adts_test.go` を作る。

```go
package tsprobe

import (
	"testing"

	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/testutil"
)

func TestParseADTS(t *testing.T) {
	const (
		sce = 0
		cpe = 1
		pce = 5
	)
	cases := []struct {
		name         string
		frame        []byte
		wantOK       bool
		wantConfig   int
		wantDualMono bool
	}{
		{"dual mono, 48 kHz", testutil.ADTSFrame(0, 3, true, sce), true, 0, true},
		{"dual mono, 44.1 kHz", testutil.ADTSFrame(0, 4, true, sce), true, 0, true},
		{"dual mono, 32 kHz", testutil.ADTSFrame(0, 5, true, sce), true, 0, true},
		{"dual mono with a CRC field", testutil.ADTSFrame(0, 3, false, sce), true, 0, true},
		{"stereo", testutil.ADTSFrame(2, 3, true, cpe), true, 2, false},
		{"mono", testutil.ADTSFrame(1, 3, true, sce), true, 1, false},
		{"5.1", testutil.ADTSFrame(6, 3, true, sce), true, 6, false},
		{"configuration 0 starting with a PCE", testutil.ADTSFrame(0, 3, true, pce), true, 0, false},
		{"configuration 0 starting with a CPE", testutil.ADTSFrame(0, 3, true, cpe), true, 0, false},
		{"configuration 0 at 24 kHz", testutil.ADTSFrame(0, 6, true, sce), true, 0, false},
		{"configuration 0 at 96 kHz", testutil.ADTSFrame(0, 0, true, sce), true, 0, false},
		{"empty", nil, false, 0, false},
		{"header only", testutil.ADTSFrame(0, 3, true, sce)[:7], false, 0, false},
		{"not ADTS", []byte{0x00, 0x00, 0x01, 0xC0, 0, 0, 0, 0, 0, 0}, false, 0, false},
		{"layer is not 0", append([]byte{0xFF, 0xF2}, testutil.ADTSFrame(0, 3, true, sce)[2:]...), false, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := parseADTS(tc.frame)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if info.channelConfig != tc.wantConfig || info.dualMono != tc.wantDualMono {
				t.Errorf("got config=%d dualMono=%v, want config=%d dualMono=%v",
					info.channelConfig, info.dualMono, tc.wantConfig, tc.wantDualMono)
			}
		})
	}
}

func TestParseADTS_MultipleRawBlocksWithCRCIsUndecided(t *testing.T) {
	f := testutil.ADTSFrame(0, 3, false, 0)
	f[6] = 0xFD // number_of_raw_data_blocks_in_frame = 1
	if _, ok := parseADTS(f); ok {
		t.Error("a frame whose blocks are preceded by a position table must be left undecided")
	}
}
```

- [ ] **Step 3: 失敗することを確かめる**

Run: `go test ./internal/tsprobe -count=1`
Expected: FAIL(コンパイルエラー: `undefined: parseADTS`)

- [ ] **Step 4: 実装する**

`internal/tsprobe/adts.go` を作る。

```go
// Package tsprobe looks into an MPEG-TS stream as it passes by and reports
// whether its ADTS audio is dual mono. It is a debugging aid: it checks a few
// bytes of the PSI and of the first audio frames, and stops.
package tsprobe

// adtsInfo is what the first ADTS frame of an audio stream says.
type adtsInfo struct {
	channelConfig int
	dualMono      bool
}

// parseADTS reads the ADTS header at the start of b and the id_syn_ele that
// follows it. ok is false when b does not start with a usable frame, in which
// case the caller should wait for the next one.
//
// ARIB STD-B32 signals dual mono as channel_configuration = 0 with two SCEs
// and no PCE. Counting the SCEs would mean decoding the spectral data, so a
// frame counts as dual mono when channel_configuration is 0, the sampling
// frequency is 48, 44.1 or 32 kHz (the only ones tsreadex splits) and the
// first element is an SCE (id_syn_ele 0).
func parseADTS(b []byte) (info adtsInfo, ok bool) {
	if len(b) < 7 || b[0] != 0xFF || b[1]&0xF6 != 0xF0 { // syncword, layer 00
		return adtsInfo{}, false
	}
	protectionAbsent := b[1]&1 == 1
	samplingIndex := int(b[2]>>2) & 0xF
	channelConfig := int(b[2]&1)<<2 | int(b[3]>>6)
	rawBlocks := int(b[6] & 3)

	headerLen := 7
	if !protectionAbsent {
		if rawBlocks != 0 {
			return adtsInfo{}, false // block positions precede the CRC; not worth parsing
		}
		headerLen = 9
	}
	if len(b) <= headerLen {
		return adtsInfo{}, false
	}
	firstElement := b[headerLen] >> 5
	return adtsInfo{
		channelConfig: channelConfig,
		dualMono:      channelConfig == 0 && samplingIndex >= 3 && samplingIndex <= 5 && firstElement == 0,
	}, true
}
```

- [ ] **Step 5: 通ることを確かめる**

Run: `gofmt -l . && go vet ./... && go test ./... -race -count=1`
Expected: `gofmt` の出力なし、すべて `ok`

- [ ] **Step 6: コミットする**

```bash
git add internal/testutil/ts.go internal/tsprobe/adts.go internal/tsprobe/adts_test.go
git commit -m "feat: ADTS ヘッダからデュアルモノを判定する"
```

---

### Task 3: `Probe`(TS を読んで音声ごとに結果を返す)

**Files:**
- Create: `internal/tsprobe/probe.go`
- Test: `internal/tsprobe/probe_test.go`
- Modify: `AGENTS.md`(「構成」に 1 行)

**Interfaces:**
- Consumes: Task 2 の `parseADTS`、`testutil.BuildTS` / `AudioES` / `TSOptions` / `ADTSFrame` / `NullPackets`。
- Produces:
  - `tsprobe.Result{PID uint16; ChannelConfig int; DualMono bool}`
  - `tsprobe.New(serviceID int, onResult func(Result), onGiveUp func(reason string)) *Probe`
  - `(*Probe).Write(b []byte) (n int, err error)`(`io.Writer`。常に `len(b), nil`)
  - `onGiveUp` の `reason` は次のいずれか: `"sync failed"`、`"sync lost"`、`"program not in PAT"`、`"no ADTS audio stream"`、`"limit reached"`、`"panic: ..."`

仕様どおり、PES は組み立てず、`payload_unit_start_indicator` が立った音声パケットの先頭だけを見る。PSI(PAT/PMT)は複数パケットにまたがるセクションを組み立てる。CRC は検査しない。

- [ ] **Step 1: 失敗するテストを書く**

`internal/tsprobe/probe_test.go` を作る。

```go
package tsprobe

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/testutil"
)

const (
	sce = 0
	cpe = 1
	pce = 5
)

type outcome struct {
	results []Result
	giveUps []string
	probe   *Probe
}

func newOutcome(serviceID int) *outcome {
	o := &outcome{}
	o.probe = New(serviceID,
		func(r Result) { o.results = append(o.results, r) },
		func(reason string) { o.giveUps = append(o.giveUps, reason) })
	return o
}

func (o *outcome) write(b []byte) {
	n, err := o.probe.Write(b)
	if n != len(b) || err != nil {
		panic("Probe.Write must report the whole slice and no error")
	}
}

func (o *outcome) writeChunks(b []byte, size int) {
	for len(b) > 0 {
		n := min(size, len(b))
		o.write(b[:n])
		b = b[n:]
	}
}

func audio(pid uint16, cfg, sfi, first int) testutil.AudioES {
	return testutil.AudioES{PID: pid, Frame: testutil.ADTSFrame(cfg, sfi, true, first)}
}

func TestProbe_DualMono(t *testing.T) {
	o := newOutcome(1024)
	o.write(testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, Audio: []testutil.AudioES{audio(0x110, 0, 3, sce)}}))
	want := []Result{{PID: 0x110, ChannelConfig: 0, DualMono: true}}
	if !reflect.DeepEqual(o.results, want) || len(o.giveUps) != 0 {
		t.Errorf("results = %+v, giveUps = %v", o.results, o.giveUps)
	}
}

func TestProbe_NotDualMono(t *testing.T) {
	cases := []struct {
		name string
		es   testutil.AudioES
		want Result
	}{
		{"stereo", audio(0x110, 2, 3, cpe), Result{PID: 0x110, ChannelConfig: 2}},
		{"configuration 0 starting with a PCE", audio(0x110, 0, 3, pce), Result{PID: 0x110, ChannelConfig: 0}},
		{"configuration 0 starting with a CPE", audio(0x110, 0, 3, cpe), Result{PID: 0x110, ChannelConfig: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOutcome(1024)
			o.write(testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, Audio: []testutil.AudioES{tc.es}}))
			if !reflect.DeepEqual(o.results, []Result{tc.want}) || len(o.giveUps) != 0 {
				t.Errorf("results = %+v, giveUps = %v", o.results, o.giveUps)
			}
		})
	}
}

func TestProbe_TwoAudioStreamsAreReportedOnceEach(t *testing.T) {
	o := newOutcome(1024)
	ts := testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, Audio: []testutil.AudioES{
		audio(0x110, 0, 3, sce),
		audio(0x111, 2, 3, cpe),
	}})
	o.write(ts)
	o.write(ts) // the same stream again: nothing more to report
	want := []Result{{PID: 0x110, DualMono: true}, {PID: 0x111, ChannelConfig: 2}}
	if !reflect.DeepEqual(o.results, want) || len(o.giveUps) != 0 {
		t.Errorf("results = %+v, giveUps = %v", o.results, o.giveUps)
	}
}

func TestProbe_PMTSpanningPackets(t *testing.T) {
	o := newOutcome(1024)
	o.write(testutil.BuildTS(testutil.TSOptions{
		ServiceID: 1024, ExtraPMTBytes: 300,
		Audio: []testutil.AudioES{audio(0x110, 0, 3, sce)},
	}))
	if len(o.results) != 1 || !o.results[0].DualMono || len(o.giveUps) != 0 {
		t.Errorf("results = %+v, giveUps = %v", o.results, o.giveUps)
	}
}

func TestProbe_SkipsAnAudioStreamOfAnotherType(t *testing.T) {
	o := newOutcome(1024)
	mp2 := testutil.AudioES{PID: 0x112, StreamType: 0x04, Frame: testutil.ADTSFrame(0, 3, true, sce)}
	o.write(testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, Audio: []testutil.AudioES{mp2, audio(0x110, 2, 3, cpe)}}))
	if len(o.results) != 1 || o.results[0].PID != 0x110 {
		t.Errorf("results = %+v", o.results)
	}
}

func TestProbe_GivesUp(t *testing.T) {
	declaredOnly := func(st byte) testutil.TSOptions {
		return testutil.TSOptions{ServiceID: 1024, Audio: []testutil.AudioES{{PID: 0x110, StreamType: st}}}
	}
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"program not in PAT", testutil.BuildTS(testutil.TSOptions{ServiceID: 9999, Audio: []testutil.AudioES{audio(0x110, 0, 3, sce)}}), "program not in PAT"},
		// A null packet follows the PAT and the PMT so that the three packets needed to sync are there.
		{"no ADTS audio", append(testutil.BuildTS(declaredOnly(0x04)), testutil.NullPackets(1)...), "no ADTS audio stream"},
		{"no sync", make([]byte, 2000), "sync failed"},
		{"sync lost", append(append(testutil.BuildTS(declaredOnly(0x0F)), testutil.NullPackets(1)...), make([]byte, 188)...), "sync lost"},
		{"limit reached", append(testutil.BuildTS(declaredOnly(0x0F)), testutil.NullPackets(maxBytes/188+1)...), "limit reached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOutcome(1024)
			o.write(tc.in)
			if len(o.results) != 0 || !reflect.DeepEqual(o.giveUps, []string{tc.want}) {
				t.Errorf("results = %+v, giveUps = %v, want one %q", o.results, o.giveUps, tc.want)
			}
		})
	}
}

func TestProbe_StopsAfterGivingUp(t *testing.T) {
	o := newOutcome(1024)
	o.write(make([]byte, 2000))
	o.write(testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, Audio: []testutil.AudioES{audio(0x110, 0, 3, sce)}}))
	if len(o.results) != 0 || len(o.giveUps) != 1 {
		t.Errorf("results = %+v, giveUps = %v", o.results, o.giveUps)
	}
}

func TestProbe_SameResultForAnyChunking(t *testing.T) {
	ts := testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, ExtraPMTBytes: 300, Audio: []testutil.AudioES{audio(0x110, 0, 3, sce)}})
	whole := newOutcome(1024)
	whole.write(ts)
	for _, size := range []int{1, 7, 100, 187, 188, 189, 1000} {
		o := newOutcome(1024)
		o.writeChunks(append(make([]byte, 33), ts...), size) // also starts mid-packet
		if !reflect.DeepEqual(o.results, whole.results) || len(o.giveUps) != 0 {
			t.Errorf("chunk size %d: results = %+v, giveUps = %v", size, o.results, o.giveUps)
		}
	}
}

func TestProbe_WriteNeverPanics(t *testing.T) {
	t.Run("a callback that panics", func(t *testing.T) {
		var reasons []string
		p := New(1024, func(Result) { panic("boom") }, func(r string) { reasons = append(reasons, r) })
		ts := testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, Audio: []testutil.AudioES{audio(0x110, 0, 3, sce)}})
		if n, err := p.Write(ts); n != len(ts) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if len(reasons) != 1 {
			t.Errorf("giveUps = %v, want one panic report", reasons)
		}
	})
	t.Run("corrupted streams", func(t *testing.T) {
		valid := testutil.BuildTS(testutil.TSOptions{ServiceID: 1024, ExtraPMTBytes: 300, Audio: []testutil.AudioES{audio(0x110, 0, 3, sce), audio(0x111, 2, 3, cpe)}})
		rng := rand.New(rand.NewSource(1))
		for i := 0; i < 2000; i++ {
			b := append([]byte(nil), valid...)
			for k := rng.Intn(8); k >= 0; k-- {
				b[rng.Intn(len(b))] = byte(rng.Intn(256))
			}
			if rng.Intn(4) == 0 {
				b = b[:rng.Intn(len(b))]
			}
			o := newOutcome(1024)
			o.writeChunks(b, 1+rng.Intn(400))
			for _, reason := range o.giveUps {
				if len(reason) >= 5 && reason[:5] == "panic" {
					t.Fatalf("case %d panicked inside the probe: %s", i, reason)
				}
			}
		}
	})
}
```

- [ ] **Step 2: 失敗することを確かめる**

Run: `go test ./internal/tsprobe -count=1`
Expected: FAIL(コンパイルエラー: `undefined: New`、`undefined: Result`、`undefined: maxBytes`)

- [ ] **Step 3: 実装する**

`internal/tsprobe/probe.go` を作る。

```go
package tsprobe

import "fmt"

const (
	packetSize     = 188
	maxBytes       = 8 << 20 // about four seconds of a broadcast stream
	streamTypeADTS = 0x0F
	syncPackets    = 3 // packets that must line up to accept a sync
)

// Result is the verdict for one audio stream.
type Result struct {
	PID           uint16
	ChannelConfig int // channel_configuration of the first ADTS frame
	DualMono      bool
}

// Probe is an io.Writer that watches the stream written to it. It reports
// each ADTS audio stream of the service once (onResult) and, if it stops
// without having decided all of them, says why once (onGiveUp). It never
// fails a Write and never panics, and after it has finished a Write costs
// nothing.
type Probe struct {
	serviceID int
	onResult  func(Result)
	onGiveUp  func(reason string)

	done   bool
	total  int
	buf    []byte // bytes not yet consumed as whole packets
	synced bool

	pat, pmt section
	patDone  bool
	pmtPID   uint16 // 0 until the PAT names it
	pmtDone  bool
	pending  map[uint16]bool // audio PIDs still undecided
}

// New returns a Probe for the given service (the program_number to follow).
func New(serviceID int, onResult func(Result), onGiveUp func(reason string)) *Probe {
	return &Probe{serviceID: serviceID, onResult: onResult, onGiveUp: onGiveUp}
}

// Write implements io.Writer.
func (p *Probe) Write(b []byte) (n int, err error) {
	n = len(b) // named, so that a recovered panic still reports the whole slice
	if p.done {
		return n, nil
	}
	defer func() {
		if r := recover(); r != nil {
			p.finish(fmt.Sprintf("panic: %v", r))
		}
	}()

	p.total += n
	p.buf = append(p.buf, b...)
	if !p.synced && !p.sync() {
		return n, nil
	}
	off := 0
	for !p.done && len(p.buf)-off >= packetSize {
		pkt := p.buf[off : off+packetSize]
		off += packetSize
		if pkt[0] != 0x47 {
			p.finish("sync lost")
			break
		}
		p.packet(pkt)
	}
	if p.done {
		return n, nil
	}
	p.buf = append(p.buf[:0], p.buf[off:]...)
	if p.total > maxBytes {
		p.finish("limit reached")
	}
	return n, nil
}

// sync drops the bytes before the first packet boundary. It reports whether
// the stream is synchronised; it finishes the probe if it cannot be.
func (p *Probe) sync() bool {
	// Enough bytes to test every offset in the first packet.
	if len(p.buf) < packetSize*syncPackets {
		return false
	}
	for i := 0; i < packetSize; i++ {
		ok := true
		for k := 0; k < syncPackets; k++ {
			if p.buf[i+k*packetSize] != 0x47 {
				ok = false
				break
			}
		}
		if ok {
			p.buf = p.buf[i:]
			p.synced = true
			return true
		}
	}
	p.finish("sync failed")
	return false
}

// finish stops the probe. A non-empty reason is reported once.
func (p *Probe) finish(reason string) {
	if p.done {
		return
	}
	p.done = true
	p.buf = nil
	if reason != "" && p.onGiveUp != nil {
		p.onGiveUp(reason)
	}
}

func (p *Probe) packet(pkt []byte) {
	if pkt[1]&0x80 != 0 { // transport_error_indicator
		return
	}
	pusi := pkt[1]&0x40 != 0
	pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
	afc := pkt[3] >> 4 & 3
	if afc&1 == 0 { // no payload
		return
	}
	off := 4
	if afc&2 != 0 {
		off += 1 + int(pkt[4])
	}
	if off >= packetSize {
		return
	}
	payload := pkt[off:]

	switch {
	case pid == 0 && !p.patDone:
		if sec := p.pat.add(pusi, payload); sec != nil {
			p.onPAT(sec)
		}
	case p.pmtPID != 0 && pid == p.pmtPID && !p.pmtDone:
		if sec := p.pmt.add(pusi, payload); sec != nil {
			p.onPMT(sec)
		}
	case pusi && p.pending[pid]:
		p.onAudio(pid, payload)
	}
}

func (p *Probe) onPAT(sec []byte) {
	if sec[0] != 0x00 || len(sec) < 12 {
		return
	}
	for i := 8; i+4 <= len(sec)-4; i += 4 { // up to the CRC
		program := int(sec[i])<<8 | int(sec[i+1])
		if program == p.serviceID {
			p.patDone = true
			p.pmtPID = uint16(sec[i+2]&0x1F)<<8 | uint16(sec[i+3])
			return
		}
	}
	p.finish("program not in PAT")
}

func (p *Probe) onPMT(sec []byte) {
	if sec[0] != 0x02 || len(sec) < 16 || int(sec[3])<<8|int(sec[4]) != p.serviceID {
		return
	}
	p.pmtDone = true
	i := 12 + (int(sec[10]&0x0F)<<8 | int(sec[11])) // after the program descriptors
	for i+5 <= len(sec)-4 {
		streamType := sec[i]
		pid := uint16(sec[i+1]&0x1F)<<8 | uint16(sec[i+2])
		if streamType == streamTypeADTS {
			if p.pending == nil {
				p.pending = map[uint16]bool{}
			}
			p.pending[pid] = true
		}
		i += 5 + (int(sec[i+3]&0x0F)<<8 | int(sec[i+4]))
	}
	if len(p.pending) == 0 {
		p.finish("no ADTS audio stream")
	}
}

// onAudio looks at the start of a PES packet of an audio stream. The first
// ADTS frame follows the PES header, within the same TS packet.
func (p *Probe) onAudio(pid uint16, payload []byte) {
	if len(payload) < 9 || payload[0] != 0 || payload[1] != 0 || payload[2] != 1 {
		return
	}
	start := 9 + int(payload[8])
	if start >= len(payload) {
		return
	}
	info, ok := parseADTS(payload[start:])
	if !ok {
		return
	}
	delete(p.pending, pid)
	if p.onResult != nil {
		p.onResult(Result{PID: pid, ChannelConfig: info.channelConfig, DualMono: info.dualMono})
	}
	if len(p.pending) == 0 {
		p.finish("")
	}
}

// section reassembles one PSI section from the payloads of a PID.
type section struct {
	buf  []byte
	want int // length of the whole section once its header is in; 0 before that
}

// add feeds one packet payload and returns the section if it is now complete.
func (s *section) add(pusi bool, payload []byte) []byte {
	if pusi {
		pointer := 1 + int(payload[0])
		if pointer > len(payload) {
			s.buf, s.want = nil, 0
			return nil
		}
		s.buf, s.want = append(s.buf[:0], payload[pointer:]...), 0
	} else if len(s.buf) == 0 {
		return nil // the start of this section was missed
	} else {
		s.buf = append(s.buf, payload...)
	}
	if len(s.buf) > 0 && s.buf[0] == 0xFF { // stuffing, not a section
		s.buf, s.want = nil, 0
		return nil
	}
	if s.want == 0 && len(s.buf) >= 3 {
		s.want = 3 + (int(s.buf[1]&0x0F)<<8 | int(s.buf[2]))
	}
	if s.want == 0 || len(s.buf) < s.want {
		return nil
	}
	sec := s.buf[:s.want]
	s.buf, s.want = nil, 0
	return sec
}
```

- [ ] **Step 4: 通ることを確かめる**

Run: `gofmt -l . && go vet ./... && go test ./internal/tsprobe -race -count=1 -v`
Expected: `gofmt` の出力なし、すべての `TestProbe_*` と `TestParseADTS*` が PASS

- [ ] **Step 5: AGENTS.md の「構成」に足す**

`internal/testutil` の行の前に、次の行を足す。

```markdown
- `internal/tsprobe`: debug のときだけ、上流の TS を覗いて ADTS 音声がデュアルモノかを推定する(結果はログに出す)
```

- [ ] **Step 6: 全体が通ることを確かめる**

Run: `go test ./... -race -count=1`
Expected: すべて `ok`

- [ ] **Step 7: コミットする**

```bash
git add internal/tsprobe/probe.go internal/tsprobe/probe_test.go AGENTS.md
git commit -m "feat: TS から音声ごとのデュアルモノ判定を返す Probe を追加する"
```

---

### Task 4: `stream.Serve` に Probe をつなぐ

**Files:**
- Modify: `internal/stream/stream.go`
- Modify: `internal/stream/stream_test.go`
- Modify: `README.md`(「デバッグログ」の節と、実機での確認項目)

**Interfaces:**
- Consumes: Task 3 の `tsprobe.New` / `tsprobe.Result`。Task 1 の `LOG_LEVEL`(`main.go` が渡す `slog.Logger` のレベル)。
- Produces: `(*Streamer).tap(ctx context.Context, body io.Reader, serviceID int) io.Reader`(パッケージ内)。debug が有効でなければ `body` をそのまま返す。ログの形:
  - `msg="audio stream probed" service_id=… pid=… channel_configuration=… dual_mono=…`
  - `msg="audio probe gave up" service_id=… reason=…`

- [ ] **Step 1: 失敗するテストを書く**

`internal/stream/stream_test.go` を次のとおり直す。

(a) `import` に `"sync"` を足す(`"strings"` の次、`"sync/atomic"` の前)。

(b) 既存の `startProxy` を、ロガーを選べる関数に分ける。`startProxy` の先頭を次に置き換える。

```go
func startProxy(t *testing.T, upstreamURL, exe string, args []string, maxStreams int, firstByte time.Duration) *httptest.Server {
	t.Helper()
	return startProxyLog(t, discard, upstreamURL, exe, args, maxStreams, firstByte)
}

// startProxyLog is startProxy with a chosen logger.
func startProxyLog(t *testing.T, log *slog.Logger, upstreamURL, exe string, args []string, maxStreams int, firstByte time.Duration) *httptest.Server {
	t.Helper()
```

そして、関数の中の `New(Config{...}, discard)` の `discard` を `log` に変える。

```go
	s, err := New(Config{Upstream: base, Tsreadex: exe, Args: args, MaxStreams: maxStreams, FirstByteTimeout: firstByte}, log)
```

(c) ファイルの末尾に次を追加する。

```go
// lockedBuffer is a bytes.Buffer that the logger and the test may touch from
// different goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func logAt(level slog.Level) (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})), buf
}

func dualMonoTS() []byte {
	return testutil.BuildTS(testutil.TSOptions{
		ServiceID: 1024,
		Audio:     []testutil.AudioES{{PID: 0x110, Frame: testutil.ADTSFrame(0, 3, true, 0)}},
	})
}

func TestServe_DebugLogsDualMonoAndPassesTheStreamThrough(t *testing.T) {
	log, logs := logAt(slog.LevelDebug)
	ts := dualMonoTS()
	proxy := startProxyLog(t, log, bodyUpstream(t, string(ts)).URL, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)

	resp, err := http.Get(proxy.URL + "/stream/3273601024") // service id 1024
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !bytes.Equal(body, ts) {
		t.Errorf("the stream changed on its way through: got %d bytes, want %d", len(body), len(ts))
	}
	got := logs.String()
	if n := strings.Count(got, `msg="audio stream probed"`); n != 1 {
		t.Fatalf("want one probe line, got %d:\n%s", n, got)
	}
	for _, want := range []string{"service_id=1024", "pid=272", "channel_configuration=0", "dual_mono=true"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %q:\n%s", want, got)
		}
	}
}

func TestServe_InfoLevelDoesNotProbe(t *testing.T) {
	log, logs := logAt(slog.LevelInfo)
	ts := dualMonoTS()
	proxy := startProxyLog(t, log, bodyUpstream(t, string(ts)).URL, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)

	resp, err := http.Get(proxy.URL + "/stream/3273601024")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !bytes.Equal(body, ts) {
		t.Error("the stream changed on its way through")
	}
	if got := logs.String(); strings.Contains(got, "audio") {
		t.Errorf("nothing about audio may be logged at the info level:\n%s", got)
	}
}

func TestServe_DebugLogsWhyTheProbeGaveUp(t *testing.T) {
	log, logs := logAt(slog.LevelDebug)
	proxy := startProxyLog(t, log, bodyUpstream(t, strings.Repeat("x", 2000)).URL, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)

	resp, err := http.Get(proxy.URL + "/stream/3273601024")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := logs.String(); !strings.Contains(got, `msg="audio probe gave up"`) || !strings.Contains(got, `reason="sync failed"`) {
		t.Errorf("the give-up was not logged:\n%s", got)
	}
}
```

- [ ] **Step 2: 失敗することを確かめる**

Run: `go test ./internal/stream -race -count=1 -run 'TestServe_(Debug|Info)'`
Expected: FAIL(`TestServe_DebugLogsDualMonoAndPassesTheStreamThrough` と `TestServe_DebugLogsWhyTheProbeGaveUp` が、ログに該当の行がなくて失敗する。`TestServe_InfoLevelDoesNotProbe` は今の実装でも通る)

- [ ] **Step 3: 実装する**

`internal/stream/stream.go` を次のとおり直す。

(a) `import` に `"io"`(`"fmt"` の次)と、`internal/tsprobe`(`internal/upstream` の前)を足す。

```go
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/tsprobe"
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/upstream"
```

(b) `Serve` の中の `cmd.Stdin = resp.Body` を置き換える。

```go
	cmd.Stdin = s.tap(ctx, resp.Body, serviceID)
```

(c) `fail` の前に関数を追加する。

```go
// tap returns the reader tsreadex should read. At the debug level it also
// feeds a copy of the stream to a tsprobe.Probe, which logs whether the audio
// is dual mono; otherwise it is body itself.
func (s *Streamer) tap(ctx context.Context, body io.Reader, serviceID int) io.Reader {
	if !s.log.Enabled(ctx, slog.LevelDebug) {
		return body
	}
	probe := tsprobe.New(serviceID, func(r tsprobe.Result) {
		s.log.Debug("audio stream probed", "service_id", serviceID, "pid", r.PID,
			"channel_configuration", r.ChannelConfig, "dual_mono", r.DualMono)
	}, func(reason string) {
		s.log.Debug("audio probe gave up", "service_id", serviceID, "reason", reason)
	})
	return io.TeeReader(body, probe)
}
```

- [ ] **Step 4: README を直す**

`README.md` の `## セキュリティ` の直前(設定表のあと)に、次の節を足す。

````markdown
### デバッグログ

`LOG_LEVEL=debug` にすると、視聴を始めた直後に、上流のストリームの音声(ADTS)ごとに次のログが 1 回出ます。デュアルモノ(2 か国語放送など)かどうかの確認に使えます。

```
level=DEBUG msg="audio stream probed" service_id=1024 pid=273 channel_configuration=0 dual_mono=true
```

- `dual_mono` は推定です。`channel_configuration` が 0 で、サンプリング周波数が 48 / 44.1 / 32 kHz で、先頭の要素が SCE のときに `true` になります(tsreadex のように SCE が 2 個あることまでは確かめません)。確定は、tsreadex の `-a` に +8 を付けたときに音声が分離されるかで確かめてください。
- 判定できなかったときは `msg="audio probe gave up" reason=…` が出ます(`sync failed` / `sync lost` / `program not in PAT` / `no ADTS audio stream` / `limit reached`)。
- 解析は最初の約 8 MiB(数秒分)だけで、tsreadex に渡すストリームは変わりません。`debug` 以外では何もしません。
````

「実機での確認手順」の最後の項目の次に、次を足す。

```markdown
- [ ] `LOG_LEVEL=debug` で、2 か国語放送の番組を視聴すると `dual_mono=true` のログが出る。ステレオの番組では `dual_mono=false` になる。
```

- [ ] **Step 5: 通ることを確かめる**

Run: `gofmt -l . && go vet ./... && go test ./... -race -count=1`
Expected: `gofmt` の出力なし、すべて `ok`

安定性の確認: `go test ./internal/stream -race -count=30 -run 'TestServe_(Debug|Info)'`
Expected: `ok`

- [ ] **Step 6: ビルドを確かめる**

Run: `go build -o /tmp/claude-1000/-home-onodai145-repos-github-com-onodai145-tsreadex-proxy/f5930f33-cbed-4354-8133-874fb4990148/scratchpad/mtp ./cmd/mirakurun-tsreadex-proxy && echo built`
Expected: `built`

- [ ] **Step 7: コミットする**

```bash
git add internal/stream/stream.go internal/stream/stream_test.go README.md
git commit -m "feat: debug ログでデュアルモノの検出結果を出す"
```

---

## Self-Review(計画を書いた人が実施済み)

- **仕様の網羅:** §1 の成功基準 → Task 4 のテスト(debug で 1 回、info では分岐なし)。§3 の判定基準 → Task 2。§4 の `tsprobe` → Task 3、`stream` の変更 → Task 4、`config` / `main` / README → Task 1 と Task 4。§5 のエラー処理 → Task 3 の `WriteNeverPanics`。§6 の検証 → 各タスクのテストと、Task 4 の実機確認項目。
- **検証の範囲:** Task 2・3 のコードは、スクラッチ領域の使い捨てのコピーで `go test ./... -race` が通ったファイルをそのまま取り込んでいる。Task 1・4 の差分(config、main、stream、stream_test)は、同じコピーの `git diff` と照合した。README と AGENTS.md の文面は、テストで検証していない。
- **プレースホルダー:** なし。
- **型と名前の整合:** `Result` / `New` / `Probe.Write` / `parseADTS` / `adtsInfo` / `testutil.*` は Task 2〜4 で同じ名前と型を使っている。
