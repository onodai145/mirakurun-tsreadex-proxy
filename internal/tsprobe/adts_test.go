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
