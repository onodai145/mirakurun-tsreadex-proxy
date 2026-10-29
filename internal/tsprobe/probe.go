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
