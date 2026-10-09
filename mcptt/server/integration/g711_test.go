package integration

import (
	"bytes"
	"testing"
)

// TestEncodePCMU_anchors pins the µ-law anchors: digital zero and negative
// zero encode to the µ-law zero bytes, full-scale clips to the extreme
// codes. These come from the G.711 tables, not from this encoder.
func TestEncodePCMU_anchors(t *testing.T) {
	anchors := []struct {
		name  string
		samp  int16
		uByte byte
	}{
		{"zero", 0, 0xFF},
		{"negative zero", -1, 0x7F},
		{"max positive (clipped)", 32635, 0x80},
		{"beyond max positive clips", 32767, 0x80},
		{"max negative (clipped)", -32635, 0x00},
		{"beyond max negative clips", -32768, 0x00},
	}
	for _, a := range anchors {
		got := encodePCMU([]byte{byte(a.samp), byte(a.samp >> 8)})
		if len(got) != 1 || got[0] != a.uByte {
			t.Errorf("%s: encodePCMU(%d) = % X, want %02X", a.name, a.samp, got, a.uByte)
		}
	}
}

// decodeULaw is the standard G.711 µ-law decoder (the Sun g711.c formula)
// — an independent implementation used to round-trip the encoder. Writing
// it from the spec's decoder rather than the encoder's structure gives the
// test real power: a drift in the encoder shows up as a decode mismatch.
func decodeULaw(u byte) int16 {
	u = ^u
	t := (int(u)&0x0F)<<3 + ulawBias
	t <<= int(u&0x70) >> 4
	if u&0x80 != 0 {
		return int16(ulawBias - t)
	}
	return int16(t - ulawBias)
}

// TestEncodePCMU_roundTrip decodes every encoded sample and requires the
// reconstruction to land within one quantization half-step (the widest
// segment's step is 1024, so half a step is 512). Inputs beyond the µ-law
// full scale (±32124 decode ceiling) are excluded — clipping is anchor-
// tested above, and a clipped sample cannot round-trip by definition.
func TestEncodePCMU_roundTrip(t *testing.T) {
	for _, samp := range []int16{-32635, -32125, -8159, -8158, -133, -132, -1, 0, 1, 7, 8, 100, 8158, 8159, 32124, 32125, 32635} {
		enc := encodePCMU([]byte{byte(samp), byte(samp >> 8)})
		dec := decodeULaw(enc[0])
		diff := int(dec) - int(samp)
		if diff < 0 {
			diff = -diff
		}
		if diff > 512 {
			t.Errorf("encode(%d) = %02X decodes to %d, off by %d (> 512)", samp, enc[0], dec, diff)
		}
	}
}

// TestEncodePCMU_silenceProperty: the synthetic-stream requirement — a
// frame of linear zeros encodes to all 0xFF (µ-law positive zero), which is
// exactly the adapter's synthetic silence byte.
func TestEncodePCMU_silenceProperty(t *testing.T) {
	pcm := make([]byte, 320) // 160 samples of zero
	enc := encodePCMU(pcm)
	if len(enc) != 160 {
		t.Fatalf("160 samples encoded to %d bytes, want 160", len(enc))
	}
	if !bytes.Equal(enc, bytes.Repeat([]byte{0xFF}, 160)) {
		t.Error("zero PCM did not encode to all-0xFF silence")
	}
}

// TestEncodePCMU_oddLength ignores a trailing odd byte rather than reading
// out of bounds (20 ms frames are always even, but the codec must not
// panic on malformed input).
func TestEncodePCMU_oddLength(t *testing.T) {
	enc := encodePCMU([]byte{0x00, 0x01, 0x02})
	if len(enc) != 1 {
		t.Fatalf("3-byte PCM encoded to %d samples, want 1", len(enc))
	}
	if enc[0] != encodePCMU([]byte{0x00, 0x01})[0] {
		t.Error("odd trailing byte was not ignored")
	}
}

// TestEncodePCMU_knownVector pins one value against the independent
// decoder: 100 sits 4 below the nearest segment-0 quantization level (104),
// the half-step the characteristic guarantees.
func TestEncodePCMU_knownVector(t *testing.T) {
	samp := int16(100)
	enc := encodePCMU([]byte{byte(samp), byte(samp >> 8)})
	if dec := decodeULaw(enc[0]); dec != 104 {
		t.Errorf("encode(100) = %02X decodes to %d, want 104", enc[0], dec)
	}
}
