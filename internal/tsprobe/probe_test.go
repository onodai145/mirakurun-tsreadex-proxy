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

// A section's tail may share a packet with the start of the next section
// (pointer_field > 0). The tail still belongs to the section being assembled.
func TestProbe_PMTTailSharingAPacketWithTheNextSection(t *testing.T) {
	ts := testutil.BuildTS(testutil.TSOptions{
		ServiceID: 1024, ExtraPMTBytes: 300,
		Audio: []testutil.AudioES{audio(0x110, 0, 3, sce)},
	})
	const pkt = 188
	// ts is PAT, PMT (two packets), PES.
	pmtStart := ts[1*pkt : 2*pkt] // 4-byte header, pointer_field, then the first 183 bytes of the section
	pmtTail := ts[2*pkt : 3*pkt]  // 4-byte header, the rest of the section, then 0xFF stuffing
	total := 3 + (int(pmtStart[6]&0x0F)<<8 | int(pmtStart[7]))
	tailLen := total - 183
	if tailLen <= 0 || tailLen >= 183 {
		t.Fatalf("the test PMT must span exactly two packets, tail = %d", tailLen)
	}

	shared := append([]byte(nil), pmtTail[:4]...)
	shared[1] |= 0x40 // payload_unit_start_indicator
	shared = append(shared, byte(tailLen))
	shared = append(shared, pmtTail[4:4+tailLen]...)
	for len(shared) < pkt {
		shared = append(shared, 0xFF) // where the next section would start
	}
	rebuilt := append(append(append([]byte(nil), ts[:2*pkt]...), shared...), ts[3*pkt:]...)

	o := newOutcome(1024)
	o.write(rebuilt)
	if len(o.results) != 1 || !o.results[0].DualMono || len(o.giveUps) != 0 {
		t.Errorf("results = %+v, giveUps = %v", o.results, o.giveUps)
	}
}
