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
