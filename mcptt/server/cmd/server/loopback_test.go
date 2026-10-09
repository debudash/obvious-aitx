package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
	"golang.org/x/crypto/bcrypt"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/api"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/media"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/ws"
)

// Loopback acceptance test for the wired system: the full main.go
// composition — store-backed affiliations, REST + WSS on one mux, the
// session manager's event sink fanned out through the api renderer, media
// consequences mirrored into the gate — served by httptest, driven by real
// WebSocket dials and real Pion peers. No test-supplied gate.Apply: every
// floor grant here is arbitrated because a dispatch-routed frame asked
// for it, and the RTP relay exists only because the arbitration mirrored
// into the gate.
//
//	login → WS dial → CallStart(WS) → CallStarted broadcast
//	  → media-token (REST) → MediaOffer(WS)/MediaAnswer → ICE+DTLS
//	  → FloorRequest(WS) → FloorGranted → RTP relayed
//	  → emergency FloorRequest(WS) → FloorPreempted → grant swap mid-burst

const loopbackSecret = "loopback-test-secret"

type loopbackServer struct {
	ts      *httptest.Server
	groupID string
	alphaID string
	bravoID string
}

func newLoopbackServer(t *testing.T) *loopbackServer {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(filepath.Join(t.TempDir(), "loopback.db"), time.Second)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tokens := auth.NewTokenizer([]byte(loopbackSecret), time.Hour)
	gate := media.NewGate()
	roomTokens := media.NewRoomTokens([]byte(loopbackSecret), time.Minute)
	sfu := media.NewSFU(gate, roomTokens)

	calls := callcontrol.NewSessionManager(callcontrol.SessionConfig{
		Media: gate,
		Affiliation: func(userID, groupID string) bool {
			aff, err := st.AffiliationState(ctx, userID, groupID)
			return err == nil && aff.State == store.AffiliationAffiliated
		},
	})

	handler := ws.NewHandler(ws.NewHub(), tokens)
	handler.SetSDPHandler(sfu)
	handler.SetCallController(calls)

	hooks := &api.MediaHooks{
		RemoveParticipant: sfu.Remove,
		EndCall:           sfu.EndCall,
		MintRoomToken: func(callID, userID string) (string, error) {
			return roomTokens.Issue(callID, userID, time.Now())
		},
	}
	apiSrv := api.NewServer(st, tokens, handler, calls, hooks)
	calls.SetOnEvent(apiSrv.HandleCallEvent)

	ts := httptest.NewServer(apiSrv.Handler())
	t.Cleanup(ts.Close)

	// Seed: one dispatcher and two affiliated field users — the demo
	// roster shape, created through the store.
	hash, err := bcrypt.GenerateFromPassword([]byte("loopback-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := st.CreateUser(ctx, "dispatch", string(hash), "Dispatch", store.RoleDispatcher, 10, "TAC-1"); err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	alpha, err := st.CreateUser(ctx, "alpha", string(hash), "Alpha", store.RoleField, 5, "Alpha 1")
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	bravo, err := st.CreateUser(ctx, "bravo", string(hash), "Bravo", store.RoleField, 5, "Bravo 2")
	if err != nil {
		t.Fatalf("create bravo: %v", err)
	}
	group, err := st.CreateGroup(ctx, "Tac-1", "loopback talkgroup", alpha.ID)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	for _, uid := range []string{alpha.ID, bravo.ID} {
		if _, err := st.SetAffiliation(ctx, uid, group.ID, store.AffiliationAffiliated); err != nil {
			t.Fatalf("affiliate: %v", err)
		}
	}

	return &loopbackServer{ts: ts, groupID: group.ID, alphaID: alpha.ID, bravoID: bravo.ID}
}

// login issues real credentials over the real login route and returns the
// bearer JWT.
func (s *loopbackServer) login(t *testing.T, username string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": "loopback-pass"})
	if err != nil {
		t.Fatalf("marshal login: %v", err)
	}
	resp, err := http.Post(s.ts.URL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login %s: %v", username, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s: status %d", username, resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return out.Token
}

// mediaToken mints a room-scoped token through the REST route — the path
// the console and mobile clients use.
func (s *loopbackServer) mediaToken(t *testing.T, bearer, callID string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.ts.URL+"/api/calls/"+callID+"/media-token", nil)
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("media-token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("media-token: status %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode media-token: %v", err)
	}
	if out.Token == "" {
		t.Fatal("media-token minted an empty token")
	}
	return out.Token
}

// dialWS opens a real WebSocket to the server's /ws route with a JWT.
func (s *loopbackServer) dialWS(t *testing.T, bearer string) *websocket.Conn {
	t.Helper()
	url := "ws" + s.ts.URL[len("http"):] + "/ws?token=" + bearer
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// wsSend marshals and transmits one signaling frame.
func wsSend(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("send frame: %v", err)
	}
}

// readTyped reads frames until one of the wanted type arrives (presence
// and unrelated broadcasts are skipped), bounded by d.
func readTyped(t *testing.T, conn *websocket.Conn, want string, d time.Duration) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %s frame: %v", want, err)
		}
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &probe) != nil || probe.Type != want {
			continue
		}
		return raw
	}
}

// radioPeer is a test-side field radio over the WS signaling path: its
// WebRTC offer goes out as a MediaOffer frame and the SFU's answer comes
// back as a MediaAnswer — the full client shape, not a direct sfu call.
type radioPeer struct {
	t    *testing.T
	pc   *webrtc.PeerConnection
	mic  *webrtc.TrackLocalStaticSample
	sink *packetSink
	name string
}

// packetSink collects the RTP a peer receives.
type packetSink struct {
	mu  sync.Mutex
	got []*rtp.Packet
}

func (s *packetSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

// markers returns the first payload byte of every received packet — the
// talker attribution the test transmits.
func (s *packetSink) markers() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.got))
	for i, p := range s.got {
		if len(p.Payload) > 0 {
			out[i] = p.Payload[0]
		}
	}
	return out
}

// joinRadio performs the full WS-mediated media join for one user.
func (s *loopbackServer) joinRadio(t *testing.T, conn *websocket.Conn, bearer, callID, userID string) *radioPeer {
	t.Helper()

	se := webrtc.SettingEngine{}
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	papi := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := papi.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("%s: peer connection: %v", userID, err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	mic, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	}, "mic-"+userID, "stream-"+userID)
	if err != nil {
		t.Fatalf("%s: mic track: %v", userID, err)
	}
	if _, err := pc.AddTrack(mic); err != nil {
		t.Fatalf("%s: add mic: %v", userID, err)
	}

	sink := &packetSink{}
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			sink.mu.Lock()
			sink.got = append(sink.got, pkt)
			sink.mu.Unlock()
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("%s: create offer: %v", userID, err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("%s: local offer: %v", userID, err)
	}
	<-gather

	wsSend(t, conn, protocol.MediaOffer{
		Type: protocol.TypeMediaOffer, CallID: callID,
		Token: s.mediaToken(t, bearer, callID), SDP: pc.LocalDescription().SDP,
	})
	var answer protocol.MediaAnswer
	if err := json.Unmarshal(readTyped(t, conn, "MediaAnswer", 5*time.Second), &answer); err != nil {
		t.Fatalf("%s: decode answer: %v", userID, err)
	}
	if answer.Err != "" || answer.SDP == "" {
		t.Fatalf("%s: media offer rejected: err=%q", userID, answer.Err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		t.Fatalf("%s: apply answer: %v", userID, err)
	}
	return &radioPeer{t: t, pc: pc, mic: mic, sink: sink, name: userID}
}

// waitConnected blocks until the peer's transport is up.
func (p *radioPeer) waitConnected() {
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

// talk transmits n payloads paced at 20 ms; the first payload byte marks
// the talker so sinks can attribute relayed audio.
func (p *radioPeer) talk(n int, marker byte, wg *sync.WaitGroup) {
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

// waitForAudio blocks until at least want packets arrived.
func (p *radioPeer) waitForAudio(want int, budget time.Duration) {
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

// TestLoopbackSignalingDrivesRTPRelay is the acceptance bar for the
// dispatch wiring: two field users over real WebSockets — group call
// start, late join, PTT grant, live RTP through the SFU, and an emergency
// pre-emption that swaps the grant mid-burst. Every arbitration decision
// originates from a dispatch-routed frame; the test never touches the
// media gate.
func TestLoopbackSignalingDrivesRTPRelay(t *testing.T) {
	s := newLoopbackServer(t)
	alphaTok := s.login(t, "alpha")
	bravoTok := s.login(t, "bravo")
	alphaWS := s.dialWS(t, alphaTok)
	bravoWS := s.dialWS(t, bravoTok)

	// Group call started by a field user over WS: the server mints the
	// call ID and broadcasts the start with its own identity fields.
	wsSend(t, alphaWS, protocol.CallStart{
		Type: protocol.TypeCallStart, CallID: "client-invented",
		GroupID: s.groupID, InitiatorID: "spoof", Kind: protocol.CallKindGroup,
	})
	var started protocol.CallStart
	if err := json.Unmarshal(readTyped(t, alphaWS, "CallStart", 2*time.Second), &started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.CallID == "" || started.CallID == "client-invented" || started.InitiatorID != s.alphaID {
		t.Fatalf("start broadcast = %+v", started)
	}
	var startedBravo protocol.CallStart
	if err := json.Unmarshal(readTyped(t, bravoWS, "CallStart", 2*time.Second), &startedBravo); err != nil {
		t.Fatalf("decode start (bravo): %v", err)
	}
	if startedBravo.CallID != started.CallID {
		t.Fatalf("bravo saw call %q, alpha saw %q", startedBravo.CallID, started.CallID)
	}
	callID := started.CallID

	// Late join over WS: bravo announces into the call.
	wsSend(t, bravoWS, protocol.CallJoined{Type: protocol.TypeCallJoined, CallID: callID})
	var joined protocol.CallJoined
	if err := json.Unmarshal(readTyped(t, alphaWS, "CallJoined", 2*time.Second), &joined); err != nil {
		t.Fatalf("decode join: %v", err)
	}
	if joined.UserID != s.bravoID {
		t.Fatalf("join broadcast = %+v, want bravo (%s)", joined, s.bravoID)
	}

	// Both users join the media plane through the WS MediaOffer path with
	// REST-minted room tokens.
	alpha := s.joinRadio(t, alphaWS, alphaTok, callID, "alpha")
	bravo := s.joinRadio(t, bravoWS, bravoTok, callID, "bravo")
	alpha.waitConnected()
	bravo.waitConnected()

	// PTT down: bravo requests the floor over WS (spoofed identity and
	// priority in the frame are ignored — the grant names his verified
	// identity). The granted burst relays to alpha through the SFU.
	wsSend(t, bravoWS, protocol.FloorRequest{
		Type: protocol.TypeFloorRequest, CallID: callID,
		UserID: "spoof", Priority: 10,
	})
	var granted protocol.FloorGranted
	if err := json.Unmarshal(readTyped(t, alphaWS, "FloorGranted", 2*time.Second), &granted); err != nil {
		t.Fatalf("decode grant: %v", err)
	}
	if granted.UserID != s.bravoID {
		t.Fatalf("grant = %+v, want bravo (frame identity ignored)", granted)
	}
	if err := json.Unmarshal(readTyped(t, bravoWS, "FloorGranted", 2*time.Second), &granted); err != nil {
		t.Fatalf("decode grant (bravo): %v", err)
	}

	var wg sync.WaitGroup
	bravo.talk(150, 0x0B, &wg) // ~3 s: bravo speaking across the whole scenario
	alpha.waitForAudio(5, 3*time.Second)
	for i, m := range alpha.sink.markers() {
		if m != 0x0B {
			t.Fatalf("relayed packet %d marker = %#x, want 0x0B (bravo)", i, m)
		}
	}

	// Emergency mid-burst: alpha presses the guarded PTT; his semantic
	// emergency flag upgrades the request to P9 and takes the floor.
	// The swap is audible: alpha's marker replaces bravo's on bravo's sink.
	wsSend(t, alphaWS, protocol.FloorRequest{
		Type: protocol.TypeFloorRequest, CallID: callID,
		UserID: "alpha", Priority: 5, Emergency: true,
	})
	var preempted protocol.FloorPreempted
	if err := json.Unmarshal(readTyped(t, bravoWS, "FloorPreempted", 2*time.Second), &preempted); err != nil {
		t.Fatalf("decode pre-emption: %v", err)
	}
	if preempted.By != s.alphaID || !preempted.Emergency {
		t.Fatalf("pre-emption = %+v, want by alpha (emergency)", preempted)
	}
	var swapped protocol.FloorGranted
	if err := json.Unmarshal(readTyped(t, bravoWS, "FloorGranted", 2*time.Second), &swapped); err != nil {
		t.Fatalf("decode swap grant: %v", err)
	}
	if swapped.UserID != s.alphaID {
		t.Fatalf("swap grant = %+v, want alpha", swapped)
	}

	alpha.talk(150, 0x0A, &wg) // ~3 s of emergency transmission, overlapping bravo's burst
	bravo.waitForAudio(5, 3*time.Second)
	for i, m := range bravo.sink.markers() {
		if m != 0x0A {
			t.Fatalf("post-swap packet %d marker = %#x, want 0x0A (alpha)", i, m)
		}
	}
	wg.Wait()

	// The displaced talker's stream stopped relaying at the swap: alpha's
	// sink carries only bravo's pre-swap audio — his own audio is not
	// looped back to him, and bravo's post-swap transmission is gated out.
	for i, m := range alpha.sink.markers() {
		if m != 0x0B {
			t.Fatalf("alpha sink packet %d marker = %#x after swap, want only pre-swap 0x0B", i, m)
		}
	}
}
