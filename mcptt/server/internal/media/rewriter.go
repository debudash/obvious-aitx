package media

import (
	"sync"

	"github.com/pion/rtp"
)

// streamRewriter makes one listener's floor stream continuous across
// speaker swaps. Each speaker's client packetizes into its own sequence
// and timestamp space, but the listener's SRTP session sees a single
// SSRC for the whole call (the floor track): if forwarded sequence
// numbers jump backwards, the replay window rejects the stream and the
// listener hears silence after every hand-over. The rewriter therefore
// keeps per-listener outbound sequence and timestamp counters that
// preserve the source's pacing within a burst and stay monotonic across
// swaps — the same per-subscriber rewrite production SFUs do.
//
// Payload bytes pass through untouched; the outbound track binding
// rewrites SSRC and payload type per listener (Pion does that in
// TrackLocalStaticRTP.writeRTP).
type streamRewriter struct {
	mu     sync.Mutex
	outSeq uint16
	outTS  uint32
	inSeq  uint16 // last source sequence observed
	inTS   uint32
	primed bool
}

// frameStep is one Opus frame at 48 kHz / 20 ms — the SFU's audio format.
// It bounds the step when a source's timestamp delta is not sane (a new
// speaker's packetizer base is unrelated to the previous speaker's).
const frameStep = 960

// rewrite stamps a forwarded packet into the listener's continuous stream.
// The caller passes a private header copy; the payload is shared.
func (rw *streamRewriter) rewrite(h *rtp.Header) {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if !rw.primed {
		rw.primed = true
		rw.outSeq = h.SequenceNumber
		rw.outTS = h.Timestamp
		rw.inSeq = h.SequenceNumber
		rw.inTS = h.Timestamp
		return
	}

	// Sequence: wrap-aware delta from the source; a source restart or
	// speaker swap (non-positive delta) still advances by one so the
	// listener's stream never stalls or repeats.
	dSeq := int16(h.SequenceNumber - rw.inSeq)
	rw.inSeq = h.SequenceNumber
	if dSeq > 0 && dSeq <= 512 {
		rw.outSeq += uint16(dSeq)
	} else {
		rw.outSeq++
	}

	// Timestamp: preserve the source's pacing while it is sane; fall back
	// to one frame when a new speaker's base is unrelated to the old one.
	dTS := h.Timestamp - rw.inTS
	rw.inTS = h.Timestamp
	if dTS > 0 && dTS <= 1<<30 {
		rw.outTS += dTS
	} else {
		rw.outTS += frameStep
	}

	h.SequenceNumber = rw.outSeq
	h.Timestamp = rw.outTS
}
