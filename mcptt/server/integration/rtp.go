package integration

import (
	"bytes"
	"log"
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// RTP framing per the spec's carrier flag table: 20 ms frames, codec-driven
// payload type, continuous cadence — the carrier receives a packet every
// frame interval for the lifetime of a flagged call, carrying the granted
// talker's audio while one holds the floor and silence otherwise.
const (
	frameInterval = 20 * time.Millisecond

	// Codec parameters. PCMU is IANA payload type 0 at 8 kHz (160 bytes per
	// 20 ms); Opus rides a dynamic payload type (96, the conventional
	// choice) at 48 kHz (960 timestamp units per 20 ms).
	ptPCMU    = 0
	clockPCMU = 8000
	ptOpus    = 96
	clockOpus = 48000

	// silencePCMU is true digital silence: µ-law positive zero (0xFF).
	silencePCMU = 0xFF
	// opusSilenceBytes is the payload size of the synthetic silence packet
	// for Opus carriers. There is no canonical silent Opus bitstream (real
	// silence is encoder-state dependent), so the adapter emits an
	// empty-payload RTP packet and the README documents it as a marker,
	// not audio.
	opusSilenceBytes = 0
)

// dialFunc is the socket dialer, indirected so tests can assert the
// flag-off guarantee (zero dial attempts) and keep flag-on tests on
// loopback.
var dialFunc = net.Dial

// RTPAdapter streams the granted talker's audio as receive-only RTP to one
// carrier endpoint. For each flagged call it opens a UDP socket at call
// start, ticks at the frame interval, and sends either the most recent
// mirrored frame or synthetic silence — so a denied, queued, or revoked
// floor is heard by the carrier as silence within at most a couple of
// frames, with no trust in client behavior.
//
// Receive-only: the socket is used exclusively for writes and nothing is
// ever read from it; the adapter grants no floor and accepts no inbound
// audio.
//
// The adapter keys audio on what actually arrives from the media tap, not
// on grant bookkeeping: the tap only sees packets the SFU's gate chose to
// forward, so "fresh frames" and "a granted talker" are the same fact by
// construction. Floor updates are observed but not needed for the audio
// path; the silence guarantee rests on packet presence.
type RTPAdapter struct {
	endpoint string
	codec    string // "pcmu" | "opus"

	// interval is the frame cadence; tests may shorten it, which changes
	// the send rate but not the framing contract (frames are 20 ms of
	// audio regardless of cadence).
	interval time.Duration
	// now is injectable for tests.
	now func() time.Time

	mu    sync.Mutex
	calls map[string]*rtpStream
}

// Compile-time proof that the RTP adapter satisfies the stable contract.
var _ RXAdapter = (*RTPAdapter)(nil)

// NewRTPAdapter builds an RTP streaming adapter for one endpoint. codec is
// "pcmu" or "opus" (validated by config before wiring).
func NewRTPAdapter(endpoint, codec string) *RTPAdapter {
	return &RTPAdapter{
		endpoint: endpoint,
		codec:    codec,
		interval: frameInterval,
		now:      time.Now,
		calls:    make(map[string]*rtpStream),
	}
}

// OnCallEvent opens the stream at call start and closes it at call end.
func (a *RTPAdapter) OnCallEvent(ev CallEvent) {
	switch ev.Type {
	case CallStarted:
		a.start(ev.CallID)
	case CallEnded:
		a.stop(ev.CallID)
	}
}

// OnFloorGrant observes a grant. The audio path keys on forwarded packets,
// so there is nothing to arm here; a future adapter may use it.
func (a *RTPAdapter) OnFloorGrant(FloorUpdate) {}

// OnFloorRelease observes a floor loss. As above: silence is driven by the
// absence of fresh tap frames, not by bookkeeping.
func (a *RTPAdapter) OnFloorRelease(FloorUpdate) {}

// AudioSink returns the handle the media tap feeds; the adapter itself is
// the sink (frames are routed by call ID).
func (a *RTPAdapter) AudioSink() AudioSink { return a }

// Close stops every stream (server shutdown).
func (a *RTPAdapter) Close() error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.calls))
	for id := range a.calls {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.stop(id)
	}
	return nil
}

// start dials the carrier endpoint and begins the cadence for one call.
func (a *RTPAdapter) start(callID string) {
	a.mu.Lock()
	if _, live := a.calls[callID]; live {
		a.mu.Unlock()
		return
	}
	// A connected UDP socket sends to exactly the configured endpoint and
	// is never read from. Dial errors surface here, per stream, rather
	// than poisoning the bridge: the carrier feed must never take call
	// control down with it.
	conn, err := dialFunc("udp", a.endpoint)
	if err != nil {
		a.mu.Unlock()
		log.Printf("carrier rtp: %s: dial %s: %v", callID, a.endpoint, err)
		return
	}
	stream := a.newStream(callID, conn)
	a.calls[callID] = stream
	a.mu.Unlock()
}

// stop tears one call's stream down and waits for its cadence to exit.
func (a *RTPAdapter) stop(callID string) {
	a.mu.Lock()
	stream := a.calls[callID]
	delete(a.calls, callID)
	a.mu.Unlock()
	if stream != nil {
		stream.close()
	}
}

// WriteAudio implements AudioSink: store the newest frame (latest-wins).
// Never blocks; frames for unknown or torn-down calls are dropped.
func (a *RTPAdapter) WriteAudio(frame AudioFrame) {
	a.mu.Lock()
	stream := a.calls[frame.CallID]
	a.mu.Unlock()
	if stream == nil {
		return
	}
	stream.store(frame)
}

func (a *RTPAdapter) newStream(callID string, conn net.Conn) *rtpStream {
	s := &rtpStream{
		adapter: a,
		callID:  callID,
		conn:    conn,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	switch a.codec {
	case "opus":
		s.payloadType = ptOpus
		s.clockRate = clockOpus
	default: // "pcmu" — validated by config
		s.payloadType = ptPCMU
		s.clockRate = clockPCMU
	}
	s.tsStep = a.tsStepFor(s.clockRate)
	s.frameBytes = a.frameBytesFor(s.clockRate)
	s.ssrc = randomSSRC()
	s.seq = randomSeq()
	s.ts = randomTS()
	go s.run()
	return s
}

// tsStepFor is the RTP timestamp increment per 20 ms frame at the codec's
// clock rate (160 at 8 kHz, 960 at 48 kHz).
func (a *RTPAdapter) tsStepFor(clockRate uint32) uint32 {
	return uint32(uint64(clockRate) * uint64(20*time.Millisecond) / uint64(time.Second))
}

// frameBytesFor is one 20 ms PCMU frame in bytes at the codec's clock rate.
func (a *RTPAdapter) frameBytesFor(clockRate uint32) int {
	return int(uint64(clockRate) * uint64(20*time.Millisecond) / uint64(time.Second))
}

// rtpStream is one call's cadence: its socket, header state, and the
// latest mirrored frame.
type rtpStream struct {
	adapter *RTPAdapter
	callID  string
	conn    net.Conn

	payloadType uint8
	clockRate   uint32
	tsStep      uint32
	frameBytes  int // PCMU silence frame size; unused for Opus

	ssrc uint32
	seq  uint16
	ts   uint32

	stop chan struct{}
	done chan struct{}

	// mu guards the latest-frame slot between the tap (WriteAudio) and the
	// cadence goroutine.
	mu      sync.Mutex
	latest  AudioFrame
	has     bool
	arrived time.Time
	warned  bool // codec-mismatch warning, once per stream
}

func (s *rtpStream) store(frame AudioFrame) {
	s.mu.Lock()
	s.latest = frame
	s.has = true
	s.arrived = s.adapter.now()
	s.mu.Unlock()
}

// run is the cadence goroutine: one RTP packet per frame interval until the
// call ends.
func (s *rtpStream) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.adapter.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick sends one frame: the mirrored audio when fresh, silence otherwise.
func (s *rtpStream) tick() {
	fresh, ok := s.take()
	payload := s.payloadFor(fresh, ok)

	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    s.payloadType,
			SequenceNumber: s.seq,
			Timestamp:      s.ts,
			SSRC:           s.ssrc,
		},
		Payload: payload,
	}
	buf, err := pkt.Marshal()
	if err != nil {
		// Marshaling a 12-byte-header packet with known payload types
		// cannot fail; if it ever does, drop the frame, keep the cadence.
		return
	}
	_, _ = s.conn.Write(buf) // best-effort real-time transport; no retransmit
	s.seq++
	s.ts += s.tsStep
}

// take copies the newest frame if it is fresh (arrived within two frame
// intervals — an audio gap of that size is inaudible; a longer gap is the
// floor going quiet, i.e. silence).
func (s *rtpStream) take() (AudioFrame, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has || s.adapter.now().Sub(s.arrived) > 2*s.adapter.interval {
		return AudioFrame{}, false
	}
	return s.latest, true
}

// payloadFor selects the wire payload for this tick: mirrored audio
// transcode, or synthetic silence.
func (s *rtpStream) payloadFor(frame AudioFrame, fresh bool) []byte {
	if fresh {
		switch {
		case s.adapter.codec == "opus" && frame.Codec == FrameOpus:
			return frame.Payload
		case s.adapter.codec == "pcmu" && frame.Codec == FramePCM16:
			return encodePCMU(frame.Payload)
		}
		s.warnCodecMismatch(frame)
	}
	return s.silence()
}

func (s *rtpStream) silence() []byte {
	if s.adapter.codec == "opus" {
		return make([]byte, opusSilenceBytes)
	}
	return bytes.Repeat([]byte{silencePCMU}, s.frameBytes)
}

// warnCodecMismatch logs once per stream: a carrier configured for one
// codec while the tap supplies another must be loud, never a silent
// black-hole feed. The stream still flows (silence), which is fail-safe.
func (s *rtpStream) warnCodecMismatch(frame AudioFrame) {
	s.mu.Lock()
	warned := s.warned
	s.warned = true
	s.mu.Unlock()
	if !warned {
		log.Printf("carrier rtp: %s: tap supplied %q audio but carrier codec is %q; streaming silence until the media plane supplies %s (see README, Codec support)",
			s.callID, frame.Codec, s.adapter.codec, s.adapter.codec)
	}
}

// close stops the cadence and the socket. Called from OnCallEvent(ended) or
// adapter Close.
func (s *rtpStream) close() {
	select {
	case <-s.stop:
		return // already closing
	default:
	}
	close(s.stop)
	<-s.done
	_ = s.conn.Close()
}
