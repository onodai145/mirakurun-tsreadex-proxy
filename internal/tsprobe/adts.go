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
