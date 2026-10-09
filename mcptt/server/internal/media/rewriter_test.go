package media

import (
	"testing"

	"github.com/pion/rtp"
)

func TestStreamRewriterFirstPacketAdopted(t *testing.T) {
	var rw streamRewriter
	h := rtp.Header{SequenceNumber: 200, Timestamp: 5000}
	rw.rewrite(&h)
	if h.SequenceNumber != 200 || h.Timestamp != 5000 {
		t.Fatalf("first packet should be adopted verbatim, got seq=%d ts=%d", h.SequenceNumber, h.Timestamp)
	}
}

func TestStreamRewriterPreservesPacing(t *testing.T) {
	var rw streamRewriter
	rw.rewrite(&rtp.Header{SequenceNumber: 100, Timestamp: 1000})
	for i := 1; i <= 5; i++ {
		h := rtp.Header{SequenceNumber: uint16(100 + i), Timestamp: uint32(1000 + i*960)}
		rw.rewrite(&h)
		wantSeq := uint16(100 + i)
		wantTS := uint32(1000 + i*960)
		if h.SequenceNumber != wantSeq || h.Timestamp != wantTS {
			t.Fatalf("packet %d: got seq=%d ts=%d, want seq=%d ts=%d", i, h.SequenceNumber, h.Timestamp, wantSeq, wantTS)
		}
	}
}

func TestStreamRewriterSpeakerSwapStaysMonotonic(t *testing.T) {
	// Alpha talks (high sequence base from a long burst), then bravo is
	// pre-empted in mid-burst with his own unrelated sequence and
	// timestamp base. The listener's stream must never move backwards.
	var rw streamRewriter
	rw.rewrite(&rtp.Header{SequenceNumber: 65000, Timestamp: 100000})
	for i := 1; i <= 10; i++ { // alpha's burst continues past a wrap boundary
		rw.rewrite(&rtp.Header{SequenceNumber: uint16(65000 + i), Timestamp: uint32(100000 + i*960)})
	}
	swap := rtp.Header{SequenceNumber: 720, Timestamp: 3900000}
	rw.rewrite(&swap)
	if d := int16(swap.SequenceNumber - (65000 + 10)); d != 1 {
		t.Fatalf("after swap the stream should advance by one, got delta %+v (seq=%d)", d, swap.SequenceNumber)
	}
	if swap.Timestamp <= 100000+10*960 {
		t.Fatalf("after swap the timestamp must advance by one frame, got %d", swap.Timestamp)
	}
}

func TestStreamRewriterSequenceWrap(t *testing.T) {
	var rw streamRewriter
	rw.rewrite(&rtp.Header{SequenceNumber: 65530, Timestamp: 1000})
	h := rtp.Header{SequenceNumber: 3, Timestamp: 2920} // +9 packets, wrapped 65535 -> 0
	rw.rewrite(&h)
	if d := int16(h.SequenceNumber - 65530); d != 9 {
		t.Fatalf("wrap-around delta should be preserved (+9), got %+v (seq=%d)", d, h.SequenceNumber)
	}
	if h.Timestamp != 2920 {
		t.Fatalf("sane timestamp delta should be preserved, got %d", h.Timestamp)
	}
}

func TestStreamRewriterTimestampFallback(t *testing.T) {
	var rw streamRewriter
	rw.rewrite(&rtp.Header{SequenceNumber: 10, Timestamp: 5000})
	h := rtp.Header{SequenceNumber: 11, Timestamp: 4000} // new speaker's base below the old one
	rw.rewrite(&h)
	if h.Timestamp != 5000+frameStep {
		t.Fatalf("insane timestamp delta should fall back to one frame, got %d", h.Timestamp)
	}
}

func TestStreamRewriterDuplicateSequenceStillAdvances(t *testing.T) {
	var rw streamRewriter
	rw.rewrite(&rtp.Header{SequenceNumber: 100, Timestamp: 1000})
	h := rtp.Header{SequenceNumber: 100, Timestamp: 1960} // duplicate seq (source restart)
	rw.rewrite(&h)
	if h.SequenceNumber != 101 {
		t.Fatalf("duplicate source sequence should still advance the outbound stream, got %d", h.SequenceNumber)
	}
}
