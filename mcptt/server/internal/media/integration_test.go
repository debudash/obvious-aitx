package media

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// In-process integration tests: real Pion field peers negotiate with the
// SFU over loopback ICE and exchange real RTP — the spec's "loopback
// integration test with real WebRTC peers", covering the media-plane rows
// of the acceptance table:
//
//   - two peers exchange audio through the SFU only after a grant;
//   - an un-granted sender is relayed to nobody (default-deny);
//   - a removed participant receives zero further packets;
//   - a late joiner hears the live talker within 2 s of connect.

const (
	// connectWait bounds ICE+DTLS over loopback per peer.
	connectWait = 5 * time.Second
	// silenceWindow is how long a negative test listens to prove nothing
	// arrives. Gating drops synchronously (never delays), so anything that
	// was going to arrive does so in packet-transit time; 700 ms is ample.
	silenceWindow = 700 * time.Millisecond
	// burstInterval paces the fake encoder at a realistic 20 ms frame.
	burstInterval = 20 * time.Millisecond
)

// fieldPeer is a test-side field radio: a real Pion PeerConnection with one
// microphone track, offering to the SFU exactly as the Web console and the
// mobile clients will.
type fieldPeer struct {
	t    *testing.T
	pc   *webrtc.PeerConnection
	mic  *webrtc.TrackLocalStaticSample
	pkt  *rtpPacketSink
	name string
}

// rtpPacketSink collects the floor audio a field peer receives.
type rtpPacketSink struct {
	mu     sync.Mutex
	got    []*rtp.Packet
	closed bool
}

func (s *rtpPacketSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func (s *rtpPacketSink) packets() []*rtp.Packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*rtp.Packet(nil), s.got...)
}

// sinkError is nil while the sink is live and reading; non-nil once the
// transport delivered a read error (i.e. the sink's transport closed).
func (s *rtpPacketSink) sinkError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("media sink closed after %d packets", len(s.got))
	}
	return nil
}

func (s *rtpPacketSink) add(p *rtp.Packet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.got = append(s.got, p)
	}
}

func (s *rtpPacketSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

// joinFieldPeer connects a field peer to the SFU: an offer with one
// sendrecv audio m-line, a room token issued for this call and user, and
// the SFU's answer applied.
func joinFieldPeer(t *testing.T, sfu *SFU, tokens *RoomTokens, callID, userID string) *fieldPeer {
	t.Helper()

	se := webrtc.SettingEngine{}
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("peer connection: %v", err)
	}

	mic, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	}, "mic-"+userID, "stream-"+userID)
	if err != nil {
		t.Fatalf("mic track: %v", err)
	}
	if _, err := pc.AddTrack(mic); err != nil {
		t.Fatalf("add mic: %v", err)
	}

	sink := &rtpPacketSink{}
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		defer sink.close()
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			sink.add(pkt)
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("local offer: %v", err)
	}
	<-gather

	answerSDP, err := sfu.HandleOffer(callID, userID, mustRoomToken(t, tokens, callID, userID), pc.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("SFU.HandleOffer(%s): %v", userID, err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}); err != nil {
		t.Fatalf("apply answer: %v", err)
	}
	return &fieldPeer{t: t, pc: pc, mic: mic, pkt: sink, name: userID}
}

func mustRoomToken(t *testing.T, tokens *RoomTokens, callID, userID string) string {
	t.Helper()
	tok, err := tokens.Issue(callID, userID, time.Now())
	if err != nil {
		t.Fatalf("room token: %v", err)
	}
	return tok
}

// waitConnected blocks until the peer's transport is up or the bound
// expires — a field peer is only "connected" once media can flow.
func (p *fieldPeer) waitConnected() {
	p.t.Helper()
	deadline := time.Now().Add(connectWait)
	for time.Now().Before(deadline) {
		if p.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.t.Fatalf("%s: transport never connected (state %s)", p.name, p.pc.ConnectionState())
}

// talk transmits n RTP payloads paced at 20 ms, tagging each with a
// distinguishable marker byte. The SFU does not decode audio, so the
// payload content is whatever the test says it is.
func (p *fieldPeer) talk(n int, marker byte, wg *sync.WaitGroup) {
	p.t.Helper()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			payload := make([]byte, 40)
			payload[0] = marker
			payload[1] = byte(i)
			if err := p.mic.WriteSample(media.Sample{Data: payload, Duration: burstInterval}); err != nil {
				return // transport closed mid-burst (removal or teardown)
			}
			time.Sleep(burstInterval)
		}
	}()
}

// waitForAudio blocks until the peer has received at least want packets;
// fails the test when the budget expires.
func (p *fieldPeer) waitForAudio(want int, budget time.Duration) {
	p.t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if got := p.pkt.count(); got >= want {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("%s: got %d packets, want %d within %s", p.name, p.pkt.count(), want, budget)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expectSilence fails the test if any packet arrives within the window.
func (p *fieldPeer) expectSilence(window time.Duration, phase string) {
	p.t.Helper()
	time.Sleep(window)
	if got := p.pkt.count(); got != 0 {
		p.t.Fatalf("%s: %s received %d packets, want 0", p.name, phase, got)
	}
}

func newIntegrationSFU() (*SFU, *RoomTokens) {
	tokens := NewRoomTokens([]byte(testSecret), time.Minute)
	return NewSFU(NewGate(), tokens), tokens
}

// TestTwoPeersExchangeOpusAfterGrant — the core acceptance: real peers,
// real negotiation, audio flows if and only if the gate says so. The grant
// lands mid-burst, so the same transmission is silenced before it and
// relayed after it.
func TestTwoPeersExchangeOpusAfterGrant(t *testing.T) {
	sfu, tokens := newIntegrationSFU()
	const callID = "call_grant"

	tango := joinFieldPeer(t, sfu, tokens, callID, "tango")
	juliet := joinFieldPeer(t, sfu, tokens, callID, "juliet")
	tango.waitConnected()
	juliet.waitConnected()

	var wg sync.WaitGroup
	// Un-granted first: tango transmits a long burst; the first 700 ms must
	// be silence for juliet.
	tango.talk(75, 0x01, &wg) // ~1.5 s
	juliet.expectSilence(silenceWindow, "before grant")

	// The floor controller grants tango (control-plane write) while he is
	// still transmitting — the burst continues, now relayed.
	sfu.gate.Apply(callID, grantFor("tango"))
	juliet.waitForAudio(5, 2*time.Second)
	for i, pkt := range juliet.pkt.packets() {
		if pkt.Payload[0] != 0x01 {
			t.Fatalf("packet %d marker = %#x, want 0x01 (tango)", i, pkt.Payload[0])
		}
	}
	wg.Wait()

	if got := sfu.Participants(callID); len(got) != 2 || got[0] != "juliet" || got[1] != "tango" {
		t.Errorf("participants = %v, want [juliet tango]", got)
	}
}

// TestUngrantedSenderRelayedToNobody — default-deny under sustained load:
// the gate grants nobody for the whole test, the talker transmits the
// entire time, and the listener still receives nothing.
func TestUngrantedSenderRelayedToNobody(t *testing.T) {
	sfu, tokens := newIntegrationSFU()
	const callID = "call_deny"

	tango := joinFieldPeer(t, sfu, tokens, callID, "tango")
	juliet := joinFieldPeer(t, sfu, tokens, callID, "juliet")
	tango.waitConnected()
	juliet.waitConnected()

	var wg sync.WaitGroup
	tango.talk(50, 0x02, &wg) // ~1 s of sustained transmission
	juliet.expectSilence(silenceWindow, "sustained un-granted burst")
	wg.Wait()
	if got := juliet.pkt.count(); got != 0 {
		t.Fatalf("juliet received %d packets from an un-granted talker, want 0", got)
	}
}

// TestRemovedParticipantReceivesNoMedia — after Remove returns, the removed
// peer's transport is closed: zero further packets, ever, and the reader
// observes the closed transport.
func TestRemovedParticipantReceivesNoMedia(t *testing.T) {
	sfu, tokens := newIntegrationSFU()
	const callID = "call_removed"

	tango := joinFieldPeer(t, sfu, tokens, callID, "tango")
	juliet := joinFieldPeer(t, sfu, tokens, callID, "juliet")
	tango.waitConnected()
	juliet.waitConnected()

	sfu.gate.Apply(callID, grantFor("tango"))
	var wg sync.WaitGroup
	tango.talk(25, 0x03, &wg)
	juliet.waitForAudio(5, 2*time.Second)
	wg.Wait()

	before := juliet.pkt.count()
	if !sfu.Remove(callID, "juliet") {
		t.Fatal("Remove reported no live transport for juliet")
	}

	// Keep talking well past the removal window.
	tango.talk(75, 0x04, &wg)
	time.Sleep(silenceWindow)
	wg.Wait()

	if after := juliet.pkt.count(); after != before {
		t.Fatalf("removed peer received %d additional packets (%d → %d), want 0", after-before, before, after)
	}
	if err := juliet.pkt.sinkError(); err == nil {
		t.Fatal("removed peer's media sink never observed a closed transport")
	}
}

// TestLateJoinerHearsLiveTalkerWithin2s — the spec budget: a peer joining
// a call already in progress hears the current speaker within 2 s.
func TestLateJoinerHearsLiveTalkerWithin2s(t *testing.T) {
	sfu, tokens := newIntegrationSFU()
	const callID = "call_late"

	tango := joinFieldPeer(t, sfu, tokens, callID, "tango")
	tango.waitConnected()
	sfu.gate.Apply(callID, grantFor("tango"))

	// Sustained transmission for the whole scenario.
	var wg sync.WaitGroup
	tango.talk(150, 0x05, &wg) // ~3 s
	time.Sleep(300 * time.Millisecond)

	// The late joiner, mid-burst.
	start := time.Now()
	juliet := joinFieldPeer(t, sfu, tokens, callID, "juliet")
	juliet.waitConnected()
	juliet.waitForAudio(1, 2*time.Second-time.Since(start))
	elapsed := time.Since(start)
	t.Logf("late joiner: first live audio %v after join", elapsed)

	for i, pkt := range juliet.pkt.packets() {
		if pkt.Payload[0] != 0x05 {
			t.Fatalf("packet %d marker = %#x, want 0x05 (live talker tango)", i, pkt.Payload[0])
		}
	}
	wg.Wait()
}
