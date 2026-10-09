package integration

// G.711 µ-law encoding — linear 16-bit PCM to one µ-law byte per sample.
// The classic segment/exponent algorithm (bias 0x84, clip ±32635); enough
// for receive-only carrier feeds and fully testable in pure Go.

// ulawBias is the standard G.711 encoding bias.
const ulawBias = 0x84

// ulawClip is the maximum linear magnitude representable without overflow
// once the bias is added.
const ulawClip = 32635

// encodePCMU packs little-endian 16-bit PCM samples into µ-law bytes.
// A trailing odd byte is ignored (a 20 ms frame at 8 kHz is always even:
// 160 samples).
func encodePCMU(pcm []byte) []byte {
	out := make([]byte, len(pcm)/2)
	for i := range out {
		lo := uint16(pcm[2*i])
		hi := uint16(pcm[2*i+1])
		out[i] = linearToULaw(int16(hi<<8 | lo))
	}
	return out
}

// linearToULaw encodes one linear sample. Digital zero encodes to 0xFF
// (µ-law positive zero) — the silence byte of the synthetic stream.
func linearToULaw(sample int16) byte {
	s := int(sample)
	sign := byte(0)
	if s < 0 {
		sign = 0x80
		s = -s
	}
	if s > ulawClip {
		s = ulawClip
	}
	s += ulawBias

	exp := 7
	mask := 0x4000
	for s&mask == 0 && exp > 0 {
		exp--
		mask >>= 1
	}
	mantissa := byte(s >> (exp + 3) & 0x0F)
	return ^(sign | byte(exp)<<4 | mantissa)
}
