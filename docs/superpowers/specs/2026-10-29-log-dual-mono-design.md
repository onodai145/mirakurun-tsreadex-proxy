# デュアルモノ検出のデバッグログ 設計

作成日: 2026-10-29 / 状態: ドラフト(ユーザーレビュー待ち)

## 1. 目的

Jellyfin での音声まわりの不具合を調べるため、視聴中のストリームがデュアルモノ(2 か国語放送など)かどうかを、デバッグログで確認できるようにする。

- 常用する機能ではなくデバッグ用の補助である。`LOG_LEVEL=debug` のときだけ動き、それ以外では配信の経路を変えない。
- 検出は上流 (Mirakurun / mirakc) から来た、tsreadex が加工する前の TS の AAC で行う。

### 成功基準

- `LOG_LEVEL=debug` で、デュアルモノの番組を視聴すると `dual_mono=true` のログが音声 ES ごとに 1 回出る。ステレオの番組では `dual_mono=false` が出る。
- `LOG_LEVEL` が `info` 以上(既定)のときは、解析も goroutine も追加されず、ログも変わらない。
- 解析の失敗や panic が、配信に影響しない。
- `gofmt -l .` が出力なし。`go vet ./...`、`go test ./... -race` が通る。

### 非目的(YAGNI)

- EIT の audio component descriptor による判定。
- LATM / LOAS (stream_type 0x11) と MPEG-2 Audio (0x04) の解析。tsreadex も ADTS (0x0F) だけを扱う。
- 番組の切り替え(PMT の変更)への追従。判定は視聴開始後の最初の 1 回だけ。
- SCE がちょうど 2 個あることの検証(§3 の「近似」を参照)。
- ログ以外への公開(レスポンスヘッダ、メトリクス)。
- tsreadex の `-a` 設定の自動変更。

## 2. 決定事項

| 項目 | 決定 | 理由 |
|---|---|---|
| 検出の情報源 | 上流 TS の AAC (ADTS) | tsreadex が見る情報と同じ。実際に流れている音声と一致する |
| 解析の場所 | 上流の `resp.Body` を `io.TeeReader` で分け、その場(同期)で解析する | tsreadex の出力は `-a` で変換されうる。別接続はチューナーを余計に占有する |
| 同期にした理由 | goroutine とチャネルを使わない | 非同期でコピーを捨てると TS パケットの境界が壊れる。解析は上限つきで軽く、同期で済む |
| 有効化 | `LOG_LEVEL=debug` のとき | 既存に `LOG_LEVEL` はなく、新設する |
| 判定の基準 | §3 | tsreadex の判定に合わせる。SCE の個数だけ近似する |

## 3. 判定の基準

tsreadex (`servicefilter.cpp` / `aac.cpp`、master を 2026-10-29 に確認) は、次をすべて満たす第 1 音声をデュアルモノとして分離する。

1. PMT の stream_type が 0x0F (ADTS)。
2. ADTS の samplingFrequencyIndex が 3〜5。
3. ADTS の channel_configuration が 0。
4. 各 raw_data_block に SCE がちょうど 2 個あり、CPE などを含まない。コード内のコメントは「ARIB STD-B32 は channel_configuration=0 かつ SCE 2 個をデュアルモノと定義しているようだ」としている。

PCE は付かない。FFmpeg の開発メーリングリストの議論も同じ見解である。

SCE の個数を数えるには、スペクトルデータ(ハフマン符号)まで読み進める必要があり、デバッグ用途に見合わない。そこでこのプロキシは **1〜3 を満たし、かつ raw_data_block の先頭の id_syn_ele が SCE (0) のとき `dual_mono=true`** とする。

- 先頭が PCE (5) や CPE (1) なら `false` とする。
- channel_configuration が 0 以外なら `false` とする。
- 近似なので、まれに誤検出がありうる。ログの `dual_mono` は「推定」であり、確定は tsreadex の挙動(`-a` に +8 を付けたときの分離の有無)で確かめる。

## 4. 構成と変更点

### 新規: `internal/tsprobe`

標準ライブラリだけで書く。tsreadex にも他のパッケージにも依存しない。

```go
type Result struct {
    PID           uint16
    ChannelConfig int  // ADTS の channel_configuration
    DualMono      bool
}

func New(serviceID int, onResult func(Result), onGiveUp func(reason string)) *Probe
func (p *Probe) Write(b []byte) (int, error) // io.Writer。決してエラーを返さない
```

- **同期:** 最初のデータで 0x47 が 188 バイト間隔で 3 回続く位置を探して同期を取る。取れなければ打ち切る。以降に同期を失ったときも打ち切る。188 バイトに満たない端数は、次の `Write` に持ち越す。
- **PAT:** PID 0 から `program_number == serviceID` の PMT の PID を得る。なければ「program not in PAT」で打ち切る。
- **PMT:** 複数のパケットにまたがるセクションを組み立てる。CRC は検査しない。stream_type が 0x0F の ES を音声として集める。なければ「no ADTS audio stream」で打ち切る。
- **音声 ES:** その PID の `payload_unit_start_indicator` が立ったパケットだけを見る。PES ヘッダを読み、ペイロードの先頭の ADTS ヘッダ(同期語 0xFFF)と、直後の id_syn_ele を読む。ADTS ヘッダは、`protection_absent=0` なら CRC の 2 バイトを飛ばす。1 パケットに収まるので、PES の組み立ては要らない。同期語が合わなければ、次の PES まで待つ。
- **終了:** 集めたすべての音声 ES が判定できたとき、または累計 8 MiB (約 4 秒分) を超えたときに止まる。超えたら未判定の ES について「limit reached」で打ち切る。止まったあとの `Write` は即座に戻る。
- 内部の panic は `Write` の中で `recover` し、打ち切り扱いにする。

### 変更: `internal/stream/stream.go`

`Serve` は、`s.log.Enabled(ctx, slog.LevelDebug)` のときだけ `cmd.Stdin = io.TeeReader(resp.Body, probe)` とする。それ以外は今のまま `resp.Body` を渡す。ログは次の形で出す。

```
level=DEBUG msg="audio stream probed" service_id=1024 pid=273 channel_configuration=0 dual_mono=true
level=DEBUG msg="audio probe gave up" service_id=1024 reason="limit reached"
```

### 変更: `internal/config`、`cmd/.../main.go`

- `LOG_LEVEL` (フラグ `-log-level`)。`debug` / `info` / `warn` / `error`(大文字小文字を区別しない)。既定は `info`。それ以外は起動時のエラー。
- `Config.LogLevel slog.Level` を追加する。
- `main.go` のハンドラに `&slog.HandlerOptions{Level: cfg.LogLevel}` を渡す。

### 変更: README

設定表に `LOG_LEVEL` を足す。`warn` 以上にすると、既存の Info ログ(tsreadex の stderr など)も出なくなることと、`debug` で上のログが出ることを書く。

## 5. エラー処理と影響

- 解析は配信とは独立で、失敗しても `Write` はエラーを返さず、ストリームには何も起きない。
- `io.TeeReader` は `Write` のエラーで `Read` を止めるが、`Probe.Write` は常に成功を返すので起きない。
- 解析のコストは、PAT/PMT と音声 PES の先頭だけを見ることと、上限(8 MiB)で止まることで抑える。止まったあとは 188 バイト単位の走査もしない。
- debug 以外では経路が変わらないので、リスクは debug を有効にした環境に限られる。

## 6. 検証

- `internal/tsprobe` の単体テスト: 合成した TS (PAT / PMT / ADTS) で、次を確認する。
  - デュアルモノ(channel_configuration=0、先頭が SCE)で `DualMono=true`。
  - ステレオ(channel_configuration=2)で `false`。
  - channel_configuration=0 で先頭が PCE または CPE のとき `false`。
  - PMT が 2 パケットにまたがる場合。
  - PAT に serviceID がない、音声がない、同期がとれない、上限に達する場合に打ち切る。
  - 任意のバイト列で panic しない(簡単なファズ)。
  - 分割された入力(1 バイトずつ、任意の境界)でも結果が同じ。
- `internal/stream` のテスト(tsreadex はシェルスクリプトのスタブ): debug のとき、ログが 1 回出て、stdout の中身は入力と変わらない。info のとき、分岐が付かない。
- `internal/config` のテスト: `LOG_LEVEL` の既定値、各値、不正値。
- 実機の確認: 2 か国語放送の番組で `dual_mono=true` が出ることを、リリース前の人手の確認項目に加える(README の「実機での確認手順」)。

## 7. リスク

- **近似による誤検出:** §3 のとおり。ログは推定であり、確定には tsreadex の挙動を使う。
- **ARIB 規格の本文は未確認:** 判定の根拠は tsreadex のソースのコメントと FFmpeg の議論であり、ARIB STD-B32 の本文そのものは確認していない。
- **LATM の放送:** 0x0F 以外の音声は解析しない。地上・BS・CS の通常の放送は ADTS (0x0F) である(tsreadex の前提と同じ)。
