package callcontrol

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/media"
)

// Wired-path integration tests: the control plane's own mirror (the
// SessionConfig.Media seam) drives a real media.Gate behind a real SFU, and
// real Pion peers negotiate with it over loopback ICE. Nothing in these
// tests calls gate.Apply — every gate transition is produced by a
// SessionManager method, which is exactly the wiring main.go builds.
// The scope-addition acceptance lives here: a FloorRequest→grant→RTP relay
// through the wired path, and the gate swapping mid-burst on emergency
// pre-emption.

const wiredSecret = "wired-path-integration-secret"

// wiredPeer is a test-side field radio: a real Pion PeerConnection joined
// to the SFU with a room token, one microphone track, and a sink collecting
// the floor audio it receives. Marker bytes in the payload distinguish
// which radio a packet came from (the SFU relays payloads verbatim).
type wiredPeer struct {
	t    *testing.T
	pc   *webrtc.PeerConnection
	mic  *webrtc.TrackLocalStaticSample
	sink *wiredSink
	name string
}

type wiredSink struct {
	mu      sync.Mutex
	packets []*rtp.Packet
}

func (s *wiredSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.packets)
}

func (s *wiredSink) countWithMarker(marker byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.packets {
		if len(p.Payload) > 0 && p.Payload[0] == marker {
			n++
		}
	}
	return n
}

// tailIsMarker reports whether every packet received since the caller last
// observed the sink (beforeCount) carries marker — the pre-emption
// assertion: after the swap, no displaced audio arrives.
func (s *wiredSink) tailIsMarker(beforeCount int, marker byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.packets[beforeCount:] {
		if len(p.Payload) == 0 || p.Payload[0] != marker {
			return false
		}
	}
	return true
}

func (s *wiredSink) add(p *rtp.Packet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packets = append(s.packets, p)
}

// joinWiredPeer connects one field radio to the SFU the same way the
// console and mobile clients will: an offer with one sendrecv audio
// m-line, a room-scoped token, the SFU's answer applied.
func joinWiredPeer(t *testing.T, sfu *media.SFU, tokens *media.RoomTokens, callID, userID string) *wiredPeer {
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

	sink := &wiredSink{}
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
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

	token, err := tokens.Issue(callID, userID, time.Now())
	if err != nil {
		t.Fatalf("room token: %v", err)
	}
	answerSDP, err := sfu.HandleOffer(callID, userID, token, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("SFU.HandleOffer(%s): %v", userID, err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}); err != nil {
		t.Fatalf("apply answer: %v", err)
	}
	return &wiredPeer{t: t, pc: pc, mic: mic, sink: sink, name: userID}
}

func (p *wiredPeer) waitConnected() {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.t.Fatalf("%s: transport never connected (state %s)", p.name, p.pc.ConnectionState())
}

// talk transmits n payloads paced at 20 ms, each tagged with marker.
func (p *wiredPeer) talk(n int, marker byte, wg *sync.WaitGroup) {
	p.t.Helper()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			payload := make([]byte, 40)
			payload[0] = marker
			payload[1] = byte(i)
			if err := p.mic.WriteSample(pionmedia.Sample{Data: payload, Duration: 20 * time.Millisecond}); err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

func (p *wiredPeer) waitForAudio(want int, budget time.Duration) {
	p.t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if got := p.sink.count(); got >= want {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("%s: got %d packets, want %d within %s", p.name, p.sink.count(), want, budget)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newWiredStack builds what main.go builds: a real gate, real room tokens,
// the SFU on top of them, and a control plane whose mirror feeds the gate.
func newWiredStack(t *testing.T) (*SessionManager, *media.SFU, *media.Gate, *media.RoomTokens) {
	t.Helper()
	gate := media.NewGate()
	tokens := media.NewRoomTokens([]byte(wiredSecret), time.Minute)
	sfu := media.NewSFU(gate, tokens)
	sm, _ := newTestManager(t, map[string][]string{
		"alpha":   {"g1"},
		"bravo":   {"g1"},
		"charlie": {"g1"},
	}, func(c *SessionConfig) { c.Media = gate })
	return sm, sfu, gate, tokens
}

// joinThree peers alpha, bravo, and charlie into a g1 group call and its
// media room, all transports connected.
func joinThree(t *testing.T, sm *SessionManager, sfu *media.SFU, tokens *media.RoomTokens, callID string) (*wiredPeer, *wiredPeer, *wiredPeer) {
	t.Helper()
	if _, err := sm.Join(callID, "bravo", floor.PriorityNormal); err != nil {
		t.Fatalf("join bravo: %v", err)
	}
	if _, err := sm.Join(callID, "charlie", floor.PriorityNormal); err != nil {
		t.Fatalf("join charlie: %v", err)
	}
	alpha := joinWiredPeer(t, sfu, tokens, callID, "alpha")
	bravo := joinWiredPeer(t, sfu, tokens, callID, "bravo")
	charlie := joinWiredPeer(t, sfu, tokens, callID, "charlie")
	alpha.waitConnected()
	bravo.waitConnected()
	charlie.waitConnected()
	return alpha, bravo, charlie
}

// TestWiredPathRelaysFloorAudio — a FloorRequest grant relays real RTP
// end to end through main.go's construction: control-plane decision →
// mirror → gate → SFU fan-out → listener sink.
func TestWiredPathRelaysFloorAudio(t *testing.T) {
	sm, sfu, gate, tokens := newWiredStack(t)

	call, err := sm.StartGroup("g1", "alpha", floor.PrioritySupervisor)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	alpha, bravo, charlie := joinThree(t, sm, sfu, tokens, call.CallID)

	// Grant through the wired path — the test never touches the gate.
	d, err := sm.RequestFloor(call.CallID, "alpha", false)
	if err != nil || d.Outcome != floor.OutcomeGranted {
		t.Fatalf("alpha floor request: %v %v", d, err)
	}
	if !gate.Granted(call.CallID, "alpha") {
		t.Fatal("gate does not mark alpha granted after the control-plane grant")
	}

	var wg sync.WaitGroup
	alpha.talk(75, 0x01, &wg) // ~1.5 s burst
	bravo.waitForAudio(10, 3*time.Second)
	charlie.waitForAudio(10, 3*time.Second)
}

// TestWiredPathEmergencyGateSwapMidBurst — the emergency pre-emption
// acceptance in media: during an in-flight burst, escalating bravo to the
// emergency tier revokes alpha's relay permission and admits bravo's, so
// the listeners' audio source swaps without a gap of relaying the
// displaced talker.
func TestWiredPathEmergencyGateSwapMidBurst(t *testing.T) {
	sm, sfu, gate, tokens := newWiredStack(t)

	call, err := sm.StartGroup("g1", "alpha", floor.PrioritySupervisor)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	alpha, bravo, charlie := joinThree(t, sm, sfu, tokens, call.CallID)

	d, err := sm.RequestFloor(call.CallID, "alpha", false)
	if err != nil || d.Outcome != floor.OutcomeGranted {
		t.Fatalf("alpha floor request: %v %v", d, err)
	}

	var wg sync.WaitGroup
	alpha.talk(150, 0x01, &wg) // ~3 s burst; pre-empted mid-way
	charlie.waitForAudio(10, 3*time.Second)

	// Mid-burst emergency escalation: bravo strips alpha at level 9.
	bravoCountBefore := charlie.sink.count()
	dec, info, err := sm.EscalateEmergency(call.CallID, "bravo", false)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if dec.Outcome != floor.OutcomeGranted || dec.Level != floor.PriorityEmergency || dec.PreemptedUserID != "alpha" {
		t.Fatalf("escalation decision = %+v, want L9 grant displacing alpha", dec)
	}
	if !info.Emergency || info.Speaker != "bravo" {
		t.Fatalf("escalated snapshot = %+v", info)
	}

	// The gate swapped synchronously inside the escalation's critical
	// section: alpha revoked, bravo admitted.
	if gate.Granted(call.CallID, "alpha") {
		t.Fatal("gate still marks alpha granted after pre-emption")
	}
	if !gate.Granted(call.CallID, "bravo") {
		t.Fatal("gate does not mark bravo granted after pre-emption")
	}

	// Alpha's burst is still running — but none of its audio reaches
	// charlie after the swap; bravo's does.
	bravo.talk(75, 0x02, &wg)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := charlie.sink.countWithMarker(0x02); got >= 10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("charlie received %d bravo packets after pre-emption, want >=10", charlie.sink.countWithMarker(0x02))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !charlie.sink.tailIsMarker(bravoCountBefore, 0x02) {
		t.Fatal("charlie received displaced alpha audio after the gate swap")
	}
	wg.Wait()
}
