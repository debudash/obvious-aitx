package integration

import (
	"crypto/rand"
	"encoding/binary"
)

// RTP header state starts random per RFC 3550 §5.1 (SSRC collision
// avoidance, sequence/timestamp privacy): crypto-quality, never
// deterministic.

func randomSSRC() uint32 {
	var b [4]byte
	mustRandom(b[:])
	return binary.BigEndian.Uint32(b[:])
}

func randomSeq() uint16 {
	var b [2]byte
	mustRandom(b[:])
	return binary.BigEndian.Uint16(b[:])
}

func randomTS() uint32 {
	var b [4]byte
	mustRandom(b[:])
	return binary.BigEndian.Uint32(b[:])
}

// mustRandom panics on entropy failure: an RTP feed that cannot randomize
// its SSRC must fail loudly, not emit colliding streams.
func mustRandom(buf []byte) {
	if _, err := rand.Read(buf); err != nil {
		panic("integration: entropy unavailable: " + err.Error())
	}
}
