package integration

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// udpSink is a local stand-in for the carrier's RTP endpoint: a UDP socket
// that collects and parses whatever arrives.
type udpSink struct {
	conn net.PacketConn

	mu   sync.Mutex
	got  []*rtp.Packet
	stop bool
}

func newUDPSink(t *testing.T) *udpSink {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("udp sink: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s := &udpSink{conn: conn}
	go s.read()
	return s
}

func (s *udpSink) read() {
	buf := make([]byte, 2048)
	for {
		if err := s.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return
		}
		n, _, err := s.conn.ReadFrom(buf)
		s.mu.Lock()
		if s.stop {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		// pion's Unmarshal zero-copies: the packet's payload would alias
		// buf, and the next ReadFrom would rewrite already-recorded
		// packets. Copy the datagram before parsing.
		data := append([]byte(nil), buf[:n]...)
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data); err != nil {
			continue // not RTP; ignore
		}
		s.mu.Lock()
		s.got = append(s.got, &pkt)
		s.mu.Unlock()
	}
}

// drain returns every packet received so far.
func (s *udpSink) drain() []*rtp.Packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*rtp.Packet(nil), s.got...)
}

func (s *udpSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func (s *udpSink) close() {
	s.mu.Lock()
	s.stop = true
	s.mu.Unlock()
	_ = s.conn.Close()
}

// pcm16Frame builds one 20 ms little-endian PCM16 frame at the given level.
func pcm16Frame(level int16) []byte {
	frame := make([]byte, 320) // 160 samples
	for i := 0; i < 160; i++ {
		binary.LittleEndian.PutUint16(frame[2*i:], uint16(level))
	}
	return frame
}

func isPCMusilence(p []byte) bool {
	for _, b := range p {
		if b != silencePCMU {
			return false
		}
	}
	return len(p) == 160
}

// TestRTPAdapter_pcmuFraming — the flag-on acceptance at the adapter level:
// a granted talker's PCM16 tap frames reach the local RTP sink as PT-0
// µ-law RTP, 160-byte frames, strictly increasing seq/ts, constant SSRC.
func TestRTPAdapter_pcmuFraming(t *testing.T) {
	sink := newUDPSink(t)
	a := NewRTPAdapter(sink.conn.LocalAddr().String(), "pcmu")
	a.interval = 10 * time.Millisecond
	defer a.Close()

	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "c1", Timestamp: time.Now()})

	// Feed tap frames faster than the cadence for ~120 ms.
	stop := make(chan struct{})
	var fed sync.WaitGroup
	fed.Add(1)
	go func() {
		defer fed.Done()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				a.WriteAudio(AudioFrame{CallID: "c1", Codec: FramePCM16, Payload: pcm16Frame(250), At: time.Now()})
			}
		}
	}()
	time.Sleep(120 * time.Millisecond)
	close(stop)
	fed.Wait()

	// Let the stream degrade to silence, then collect everything.
	time.Sleep(80 * time.Millisecond)
	pkts := sink.drain()
	if len(pkts) < 5 {
		t.Fatalf("received %d RTP packets, want >= 5", len(pkts))
	}

	audioPackets := 0
	silencePackets := 0
	var ssrc uint32
	lastSeq := -1
	lastTS := uint32(0)
	for i, pkt := range pkts {
		if pkt.Version != 2 {
			t.Errorf("packet %d: RTP version = %d, want 2", i, pkt.Version)
		}
		if pkt.PayloadType != ptPCMU {
			t.Errorf("packet %d: payload type = %d, want %d (PCMU)", i, pkt.PayloadType, ptPCMU)
		}
		if i == 0 {
			ssrc = pkt.SSRC
			if ssrc == 0 {
				t.Error("SSRC = 0")
			}
		} else {
			if pkt.SSRC != ssrc {
				t.Errorf("packet %d: SSRC changed %d → %d", i, ssrc, pkt.SSRC)
			}
			if int(pkt.SequenceNumber) != lastSeq+1 {
				t.Errorf("packet %d: seq = %d, want %d", i, pkt.SequenceNumber, lastSeq+1)
			}
			if pkt.Timestamp != lastTS+160 {
				t.Errorf("packet %d: ts = %d, want %d (+160 per 20 ms at 8 kHz)", i, pkt.Timestamp, lastTS+160)
			}
		}
		lastSeq = int(pkt.SequenceNumber)
		lastTS = pkt.Timestamp

		if isPCMusilence(pkt.Payload) {
			silencePackets++
			continue
		}
		audioPackets++
		if len(pkt.Payload) != 160 {
			t.Errorf("audio packet %d: payload = %d bytes, want 160", i, len(pkt.Payload))
		}
		// Decode one sample against the independent decoder: within a
		// half-step of the fed level (250).
		if got := int(decodeULaw(pkt.Payload[0])); got < 250-8 || got > 250+8 {
			t.Errorf("audio packet %d: sample decodes to %d, want ≈250", i, got)
		}
	}
	if audioPackets == 0 {
		t.Error("no audio packets arrived while tap frames were fresh")
	}
	if silencePackets == 0 {
		t.Error("no silence packets arrived after the tap frames stopped — the cadence must continue")
	}
}

// TestRTPAdapter_opusPassthrough — Opus carriers carry the mirrored payload
// verbatim as PT 96, and synthesize silence as an empty payload.
func TestRTPAdapter_opusPassthrough(t *testing.T) {
	sink := newUDPSink(t)
	a := NewRTPAdapter(sink.conn.LocalAddr().String(), "opus")
	a.interval = 10 * time.Millisecond
	defer a.Close()

	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "c1", Timestamp: time.Now()})
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	stop := make(chan struct{})
	var fed sync.WaitGroup
	fed.Add(1)
	go func() {
		defer fed.Done()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				a.WriteAudio(AudioFrame{CallID: "c1", Codec: FrameOpus, Payload: payload, At: time.Now()})
			}
		}
	}()
	time.Sleep(80 * time.Millisecond)
	close(stop)
	fed.Wait()
	time.Sleep(60 * time.Millisecond)

	pkts := sink.drain()
	if len(pkts) < 5 {
		t.Fatalf("received %d RTP packets, want >= 5", len(pkts))
	}
	seenAudio, seenEmpty := false, false
	var lastTS uint32
	for i, pkt := range pkts {
		if pkt.PayloadType != ptOpus {
			t.Errorf("packet %d: payload type = %d, want %d (Opus)", i, pkt.PayloadType, ptOpus)
		}
		if i > 0 && pkt.Timestamp != lastTS+960 {
			t.Errorf("packet %d: ts = %d, want +%d (960 per 20 ms at 48 kHz)", i, pkt.Timestamp, 960)
		}
		lastTS = pkt.Timestamp
		if string(pkt.Payload) == string(payload) {
			seenAudio = true
		}
		if len(pkt.Payload) == 0 {
			seenEmpty = true
		}
	}
	if !seenAudio {
		t.Error("Opus payload was not mirrored verbatim")
	}
	if !seenEmpty {
		t.Error("no empty-payload silence marker after frames stopped")
	}
}

// TestRTPAdapter_silenceOnDenyQueueRevoke — the acceptance row's negative
// half: once no fresh frames arrive (floor denied, queued, or revoked all
// stop the tap), the sink hears true digital silence within the freshness
// window, with no bookkeeping trust.
func TestRTPAdapter_silenceOnDenyQueueRevoke(t *testing.T) {
	sink := newUDPSink(t)
	a := NewRTPAdapter(sink.conn.LocalAddr().String(), "pcmu")
	a.interval = 10 * time.Millisecond
	defer a.Close()

	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "c1", Timestamp: time.Now()})
	// One burst, then silence (the floor was taken away).
	a.WriteAudio(AudioFrame{CallID: "c1", Codec: FramePCM16, Payload: pcm16Frame(1000), At: time.Now()})

	deadline := time.Now().Add(2 * time.Second)
	for sink.count() < 10 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pkts := sink.drain()
	if len(pkts) < 10 {
		t.Fatalf("received %d packets, want >= 10 (cadence continues over silence)", len(pkts))
	}
	// At most the first two packets may carry audio (freshness = 2 frames).
	for i, pkt := range pkts {
		if i >= 2 && !isPCMusilence(pkt.Payload) {
			t.Fatalf("packet %d carries non-silence payload % X after the floor was lost", i, pkt.Payload[:min(8, len(pkt.Payload))])
		}
	}
}

// TestRTPAdapter_codecMismatchStreamsSilence — a tap/audio codec that does
// not match the configured carrier codec must fail safe to silence (and a
// log), never to wrongly-encoded bytes.
func TestRTPAdapter_codecMismatchStreamsSilence(t *testing.T) {
	sink := newUDPSink(t)
	a := NewRTPAdapter(sink.conn.LocalAddr().String(), "pcmu")
	a.interval = 10 * time.Millisecond
	defer a.Close()

	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "c1", Timestamp: time.Now()})
	a.WriteAudio(AudioFrame{CallID: "c1", Codec: FrameOpus, Payload: []byte{0x01, 0x02}, At: time.Now()})

	deadline := time.Now().Add(1 * time.Second)
	for sink.count() < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for i, pkt := range sink.drain() {
		if !isPCMusilence(pkt.Payload) {
			t.Errorf("packet %d: mismatched-codec audio leaked as % X, want silence", i, pkt.Payload[:min(8, len(pkt.Payload))])
		}
	}
}

// nopConn is a write-discarding net.Conn for dial-count tests.
type nopConn struct{}

func (nopConn) Write(b []byte) (int, error)      { return len(b), nil }
func (nopConn) Read(b []byte) (int, error)       { return 0, net.ErrClosed }
func (nopConn) Close() error                     { return nil }
func (nopConn) LocalAddr() net.Addr              { return nil }
func (nopConn) RemoteAddr() net.Addr             { return nil }
func (nopConn) SetDeadline(time.Time) error      { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }

// TestRTPAdapter_oneDialPerCallStream — sockets are opened at call start
// and closed at call end: exactly one dial per stream, a re-start dials
// again, and duplicate starts are idempotent.
func TestRTPAdapter_oneDialPerCallStream(t *testing.T) {
	var dials int
	var mu sync.Mutex
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return dials
	}
	restore := dialFunc
	dialFunc = func(network, addr string) (net.Conn, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		return nopConn{}, nil
	}
	t.Cleanup(func() { dialFunc = restore })

	a := NewRTPAdapter("127.0.0.1:1", "pcmu")
	a.interval = 5 * time.Millisecond

	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "c1", Timestamp: time.Now()})
	waitFor(t, func() bool { return count() == 1 }, "first dial after call start")
	if n := count(); n != 1 {
		t.Fatalf("dials = %d, want exactly 1 after call start", n)
	}

	a.OnCallEvent(CallEvent{Type: CallEnded, CallID: "c1", Timestamp: time.Now()})
	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "c1", Timestamp: time.Now()})
	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "c1", Timestamp: time.Now()}) // idempotent while live
	waitFor(t, func() bool { return count() == 2 }, "restart dial after call end")
	if n := count(); n != 2 {
		t.Fatalf("dials = %d, want exactly 2 (one per stream; duplicate starts ignored)", n)
	}
}

// waitFor polls cond until it holds or the budget expires.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
