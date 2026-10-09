package media

import "sync"

// TapPacket is one relayed RTP packet mirrored out to carrier taps. It
// carries the packet's own header fields plus the room and sender identity;
// Payload is a defensive copy, so taps may retain it.
//
// Codec names the source encoding of Payload. The media plane negotiates
// exactly one codec (Opus, see media.go) and never decodes — taps receive
// Opus payloads untouched, exactly as the SFU forwards them to listeners.
type TapPacket struct {
	CallID         string // the call (== the SFU room)
	UserID         string // the granted talker whose microphone produced the packet
	SSRC           uint32
	SequenceNumber uint16
	Timestamp      uint32
	PayloadType    uint8
	Codec          string
	Payload        []byte
}

// TapWriter consumes mirrored talk-burst packets. Implementations must not
// block and must not call back into the SFU: WriteTap runs on the sender's
// relay goroutine, and a slow tap must shed load, never stall the media path.
type TapWriter interface {
	WriteTap(TapPacket)
}

// WriteTapFunc adapts a function to TapWriter.
type WriteTapFunc func(TapPacket)

// WriteTap implements TapWriter.
func (f WriteTapFunc) WriteTap(p TapPacket) { f(p) }

// tapRegistry is the SFU's set of per-call carrier taps. It is separate from
// the room's peer map so a tap can register before any participant joins —
// the bridge registers on the control plane's call-started event, which
// precedes the first WebRTC join.
type tapRegistry struct {
	mu   sync.Mutex
	taps map[string]map[TapWriter]struct{}
}

func (t *tapRegistry) register(callID string, w TapWriter) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.taps == nil {
		t.taps = make(map[string]map[TapWriter]struct{})
	}
	set, ok := t.taps[callID]
	if !ok {
		set = make(map[TapWriter]struct{})
		t.taps[callID] = set
	}
	set[w] = struct{}{}
	return func() { t.unregister(callID, w) }
}

func (t *tapRegistry) unregister(callID string, w TapWriter) {
	t.mu.Lock()
	defer t.mu.Unlock()
	set, ok := t.taps[callID]
	if !ok {
		return
	}
	delete(set, w)
	if len(set) == 0 {
		delete(t.taps, callID)
	}
}

// drop forgets every tap for a call (teardown hygiene: a call's taps never
// outlive its room).
func (t *tapRegistry) drop(callID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.taps, callID)
}

// packetView is the tap-facing projection of a forwarded *rtp.Packet. It
// exists so the registry can be unit-tested without constructing transports.
type packetView struct {
	ssrc        uint32
	seq         uint16
	timestamp   uint32
	payloadType uint8
	payload     []byte
}

// write mirrors one granted, forwarded packet to every registered tap for
// the call. The payload is copied per registry, once: taps receive distinct
// Packet structs sharing one immutable payload copy.
func (t *tapRegistry) write(callID, userID string, v packetView) {
	t.mu.Lock()
	set, ok := t.taps[callID]
	if !ok {
		t.mu.Unlock()
		return
	}
	taps := make([]TapWriter, 0, len(set))
	for w := range set {
		taps = append(taps, w)
	}
	t.mu.Unlock()

	payload := append([]byte(nil), v.payload...)
	for _, w := range taps {
		w.WriteTap(TapPacket{
			CallID:         callID,
			UserID:         userID,
			SSRC:           v.ssrc,
			SequenceNumber: v.seq,
			Timestamp:      v.timestamp,
			PayloadType:    v.payloadType,
			Codec:          "opus",
			Payload:        payload,
		})
	}
}
