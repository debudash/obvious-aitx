package media

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// ErrCallEnded is returned when a join races a call teardown. Room-token
// errors (ErrRoomTokenInvalid) live in tokens.go next to the minting code.
var ErrCallEnded = errors.New("media: call ended")

const (
	floorTrackID = "mcptt-floor"
	streamID     = "mcptt"
)

// SFU is the selective forwarding unit. One instance serves every call in
// the process; each call owns exactly one room. The SFU is never the
// authority on who may speak — it reads floor state from the control
// plane's Gate (in-process, per the architecture invariant) once per
// received packet.
type SFU struct {
	tokens *RoomTokens
	gate   *Gate
	taps   tapRegistry

	mu    sync.Mutex
	rooms map[string]*room
}

// NewSFU wires the SFU to the floor gate and room tokens it reads.
func NewSFU(gate *Gate, tokens *RoomTokens) *SFU {
	return &SFU{tokens: tokens, gate: gate, rooms: make(map[string]*room)}
}

// RegisterTap attaches a carrier tap to one call's forwarded audio. The tap
// mirrors exactly the packets the gate lets the SFU relay — a granted
// talker's bursts — and nothing else; for an un-tapped call it adds one
// map lookup per relayed packet. The returned func unregisters the tap.
// Taps are load-shedding by contract (TapWriter): they never stall relay.
func (s *SFU) RegisterTap(callID string, w TapWriter) func() {
	return s.taps.register(callID, w)
}

// writeTaps mirrors one granted packet to the call's taps (relay path).
func (s *SFU) writeTaps(callID, userID string, pkt *rtp.Packet) {
	s.taps.write(callID, userID, packetView{
		ssrc:        pkt.SSRC,
		seq:         pkt.SequenceNumber,
		timestamp:   pkt.Timestamp,
		payloadType: pkt.PayloadType,
		payload:     pkt.Payload,
	})
}

// HandleOffer processes one client WebRTC offer for callID and returns the
// full answer SDP (non-trickle: one WSS round trip carries it). userID must
// be the authenticated WSS identity of the offerer; token must be the
// room-scoped token minted for this call and this user. The offered SDP is
// expected to contain exactly one sendrecv audio m-line — the client's
// microphone — which the answer binds the floor-audio track to.
func (s *SFU) HandleOffer(callID, userID, token, offerSDP string) (string, error) {
	if err := s.authorize(callID, userID, token); err != nil {
		return "", err
	}
	api, err := newPeerAPI()
	if err != nil {
		return "", err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", fmt.Errorf("media: peer connection: %w", err)
	}
	// Every failure past this point must tear the transport down; only the
	// success path hands it to the room.
	answerSDP, err := s.joinAndAnswer(callID, userID, pc, offerSDP)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	return answerSDP, nil
}

// joinAndAnswer joins the room and completes SDP negotiation.
func (s *SFU) joinAndAnswer(callID, userID string, pc *webrtc.PeerConnection, offerSDP string) (string, error) {
	r := s.getOrCreateRoom(callID)
	p, replaced, err := r.join(userID, pc)
	if err != nil {
		return "", err
	}
	if replaced != nil {
		_ = replaced.pc.Close() // reconnecting participant: retire the stale transport
	}

	// The floor-audio track: carries whoever holds the floor to this peer.
	// Added after SetRemoteDescription so it binds the offer's one audio
	// m-line instead of extending the answer.
	out, err := webrtc.NewTrackLocalStaticRTP(opusCapability, floorTrackID, streamID)
	if err != nil {
		r.drop(userID, p)
		return "", fmt.Errorf("media: floor track: %w", err)
	}
	// Publish the outbound track under the room write lock: forward reads
	// q.out under the matching read lock, and r.join has already made this
	// peer visible to fan-out — an unlocked write here raced the relay and
	// could hand it a nil track.
	r.mu.Lock()
	p.out = out
	r.mu.Unlock()

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}
	if err := pc.SetRemoteDescription(offer); err != nil {
		r.drop(userID, p)
		return "", fmt.Errorf("media: remote offer: %w", err)
	}
	if _, err := pc.AddTrack(out); err != nil {
		r.drop(userID, p)
		return "", fmt.Errorf("media: add floor track: %w", err)
	}

	// The microphone side of the negotiated line. Media cannot flow before
	// DTLS completes, which requires the answer below, so registering the
	// callbacks here is race-free.
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		s.relay(r, p, remote)
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		// A failed or closed transport removes itself. Disconnected may
		// recover (a brief blip), so it is not removal-worthy.
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			r.drop(userID, p)
		}
	})

	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		r.drop(userID, p)
		return "", fmt.Errorf("media: create answer: %w", err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(ans); err != nil {
		r.drop(userID, p)
		return "", fmt.Errorf("media: local answer: %w", err)
	}
	<-gather
	return pc.LocalDescription().SDP, nil
}

// relay reads one participant's microphone and forwards it to every other
// live participant in the room while — and only while — the floor gate
// marks this participant granted. Packets from an un-granted sender are
// counted as received and dropped: the SFU never relays a talk burst the
// floor controller has not granted (boundary invariant).
func (s *SFU) relay(r *room, p *peer, remote *webrtc.TrackRemote) {
	for {
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			return // transport closed or failed; the state handler removes the peer
		}
		if !s.gate.Granted(r.id, p.userID) {
			continue
		}
		r.forward(p, pkt)
		s.writeTaps(r.id, p.userID, pkt)
	}
}

// forward relays one packet from speaker p to every other participant. The
// room's read lock makes fan-out mutually exclusive with removal (write
// lock), so a participant deleted from the map is never written to again.
// Each listener gets a private header stamped by its stream rewriter —
// speaker swaps must be invisible at the transport layer. Write errors are
// inherent to best-effort real-time transport — a listener whose link is
// dying misses bursts until its state handler removes it, and there is no
// retransmit for live audio.
func (r *room) forward(from *peer, pkt *rtp.Packet) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	for _, q := range r.peers {
		if q == from || q.out == nil {
			continue // mid-negotiation joiner: answer not complete, nothing to write into
		}
		out := *pkt // private header per listener; payload is shared
		q.rw.rewrite(&out.Header)
		_ = q.out.WriteRTP(&out)
	}
}

// Remove strips one participant from a call (dispatcher removal, control
// plane tearing down a leg). Their transport is closed after being deleted
// from the room under the room lock, so from the moment Remove returns no
// further packet can be written to them. Reports whether a live transport
// was removed.
func (s *SFU) Remove(callID, userID string) bool {
	r := s.room(callID)
	if r == nil {
		return false
	}
	r.mu.Lock()
	p := r.peers[userID]
	if p != nil {
		delete(r.peers, userID)
	}
	r.mu.Unlock()
	if p == nil {
		return false
	}
	_ = p.pc.Close()
	return true
}

// EndCall tears a whole room down and forgets it. Joins for the call after
// this recreate an empty room — call lifecycle authority stays with the
// control plane, and a fresh join still needs a valid room token. Returns
// the number of live transports closed.
func (s *SFU) EndCall(callID string) int {
	s.mu.Lock()
	r := s.rooms[callID]
	delete(s.rooms, callID)
	s.mu.Unlock()
	s.taps.drop(callID) // a call's taps never outlive its room
	if r == nil {
		return 0
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0
	}
	r.closed = true
	peers := make([]*peer, 0, len(r.peers))
	for _, p := range r.peers {
		peers = append(peers, p)
	}
	r.peers = make(map[string]*peer)
	r.mu.Unlock()
	for _, p := range peers {
		_ = p.pc.Close()
	}
	return len(peers)
}

// Participants lists the users currently connected to a call's room,
// sorted for stable displays and assertions.
func (s *SFU) Participants(callID string) []string {
	r := s.room(callID)
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.peers))
	for id := range r.peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// authorize verifies the room token binds this call and this user.
func (s *SFU) authorize(callID, userID, token string) error {
	tokenCall, tokenUser, err := s.tokens.Verify(token)
	if err != nil {
		return err
	}
	if tokenCall != callID || tokenUser != userID {
		return fmt.Errorf("%w: token binds %q on call %q", ErrRoomTokenInvalid, tokenUser, tokenCall)
	}
	return nil
}

func (s *SFU) getOrCreateRoom(callID string) *room {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[callID]
	if !ok {
		r = &room{id: callID, peers: make(map[string]*peer)}
		s.rooms[callID] = r
	}
	return r
}

func (s *SFU) room(callID string) *room {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rooms[callID]
}

// room is one call's forwarding state.
type room struct {
	id     string
	mu     sync.RWMutex
	peers  map[string]*peer
	closed bool
}

// join adds a transport for userID, returning any transport it replaced
// (a reconnecting client re-offers). The caller closes the replaced one.
func (r *room) join(userID string, pc *webrtc.PeerConnection) (*peer, *peer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, ErrCallEnded
	}
	old := r.peers[userID]
	p := &peer{userID: userID, pc: pc}
	r.peers[userID] = p
	return p, old, nil
}

// drop removes a transport if it is still the current one for userID — a
// replaced transport's late state callbacks must not evict its successor.
func (r *room) drop(userID string, p *peer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers[userID] == p {
		delete(r.peers, userID)
	}
}

// peer is one participant's transport: the connection, and the outbound
// floor-audio track the SFU relays the current speaker's packets into.
type peer struct {
	userID string
	pc     *webrtc.PeerConnection
	out    *webrtc.TrackLocalStaticRTP
	// rw stamps forwarded packets into this listener's continuous
	// outbound stream; see streamRewriter.
	rw streamRewriter
}
