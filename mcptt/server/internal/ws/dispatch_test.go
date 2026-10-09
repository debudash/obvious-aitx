package ws

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
)

// Dispatch tests drive dispatchInbound with the REAL SessionManager behind
// the CallController seam — the same manager class main.go hands the REST
// layer — so grants, queues, and pre-emptions here are arbitration
// decisions, not scripted mocks. Events fan out through the production
// renderer (RenderCallEvent) into the hub, exactly as api.HandleCallEvent
// does in main.go's wiring.

type eventRecorder struct {
	mu  sync.Mutex
	evs []callcontrol.Event
}

func (r *eventRecorder) record(e callcontrol.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
}

func (r *eventRecorder) count(t *testing.T, typ callcontrol.EventType) int {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

type dispatchEnv struct {
	hub *Hub
	h   *Handler
	sm  *callcontrol.SessionManager
	log *eventRecorder
}

// newDispatchEnv builds a handler whose dispatch drives a live session
// manager. Affiliation: alpha, bravo, and charlie belong to g1; nobody
// else belongs anywhere.
func newDispatchEnv(t *testing.T) *dispatchEnv {
	t.Helper()
	hub := NewHub()
	log := &eventRecorder{}
	sm := callcontrol.NewSessionManager(callcontrol.SessionConfig{
		Affiliation: func(userID, groupID string) bool {
			return groupID == "g1" && (userID == "alpha" || userID == "bravo" || userID == "charlie")
		},
		// The production fan-out: record the event, render it through the
		// shared wire renderer, broadcast the frames — what api's
		// HandleCallEvent does with the same renderer.
		OnEvent: func(e callcontrol.Event) {
			log.record(e)
			for _, msg := range RenderCallEvent(e) {
				hub.Broadcast(msg)
			}
		},
	})
	h := &Handler{hub: hub}
	h.SetCallController(sm)
	return &dispatchEnv{hub: hub, h: h, sm: sm, log: log}
}

// addClient registers a connection whose auth context is the given role
// and priority — what ServeHTTP would have captured from verified claims.
func (e *dispatchEnv) addClient(userID, role string, prio floor.FloorLevel) *Client {
	c := newTestClient(e.hub, userID, 64)
	c.handler = e.h
	c.role = role
	c.priority = prio
	e.hub.Add(c)
	return c
}

func (e *dispatchEnv) send(c *Client, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	e.h.dispatchInbound(c, raw)
}

// awaitFrame reads frames until one of the wanted wire type arrives
// (skipping other types — broadcasts for the whole call arrive on every
// queue), then decodes it. Filtering by the frame's type field is the
// strong form: every protocol struct unmarshals out of any other, so
// matching on the type is what makes the assertion mean something.
func awaitFrame[T any](t *testing.T, c *Client, frameType string, within time.Duration) T {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		timer := time.NewTimer(time.Until(deadline))
		select {
		case raw := <-c.send:
			timer.Stop()
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &probe) != nil || probe.Type != frameType {
				continue
			}
			var frame T
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode %s frame: %v", frameType, err)
			}
			return frame
		case <-timer.C:
			t.Fatalf("no %s frame within %v", frameType, within)
		}
	}
}

// startGroupCall opens a group call through the dispatch (the path the
// field clients use) and returns the server-minted call ID.
func (e *dispatchEnv) startGroupCall(t *testing.T, initiator *Client) string {
	t.Helper()
	e.send(initiator, protocol.CallStart{
		Type: protocol.TypeCallStart, CallID: "client-invented",
		GroupID: "g1", InitiatorID: "spoof", Kind: protocol.CallKindGroup,
	})
	started := awaitFrame[protocol.CallStart](t, initiator, protocol.TypeCallStart, time.Second)
	if started.CallID == "" || started.CallID == "client-invented" {
		t.Fatalf("server did not mint the call ID (got %q)", started.CallID)
	}
	return started.CallID
}

func TestDispatchGroupCallStartMintsServerIdentity(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)
	bravo := e.addClient("bravo", auth.RoleField, 5)

	callID := e.startGroupCall(t, alpha)

	snap, err := e.sm.Snapshot(callID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Kind != callcontrol.KindGroup || len(snap.Participants) != 1 || snap.Participants[0] != "alpha" {
		t.Fatalf("call after WS start = kind %s participants %v", snap.Kind, snap.Participants)
	}
	// The initiator broadcast reaches every connected client, and its
	// identity fields are the server's, not the frame's.
	frame := awaitFrame[protocol.CallStart](t, bravo, protocol.TypeCallStart, time.Second)
	if frame.InitiatorID != "alpha" {
		t.Fatalf("broadcast initiator = %q, want alpha", frame.InitiatorID)
	}
}

func TestDispatchCallStartDeniedForUnaffiliatedUser(t *testing.T) {
	e := newDispatchEnv(t)
	mallory := e.addClient("mallory", auth.RoleField, 5)

	e.send(mallory, protocol.CallStart{
		Type: protocol.TypeCallStart, CallID: "c1",
		GroupID: "g1", InitiatorID: "mallory", Kind: protocol.CallKindGroup,
	})

	assertNoMsg(t, mallory)
	if n := e.log.count(t, callcontrol.EventCallStarted); n != 0 {
		t.Fatalf("unaffiliated start produced %d call-start events", n)
	}
}

func TestDispatchBroadcastStartIsDispatcherOnly(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)
	dispatcher := e.addClient("dispatch_1", auth.RoleDispatcher, 10)

	// A field user cannot start an announcement.
	e.send(alpha, protocol.CallStart{
		Type: protocol.TypeCallStart, CallID: "c1",
		GroupID: "g1", InitiatorID: "alpha", Kind: protocol.CallKindBroadcast,
	})
	assertNoMsg(t, alpha)

	// A dispatcher can; the broadcast carries the initiator and the
	// announcement kind.
	e.send(dispatcher, protocol.CallStart{
		Type: protocol.TypeCallStart, CallID: "c2",
		GroupID: "g1", InitiatorID: "spoof", Kind: protocol.CallKindBroadcast,
	})
	started := awaitFrame[protocol.CallStart](t, alpha, protocol.TypeCallStart, time.Second)
	if started.Kind != protocol.CallKindBroadcast || started.InitiatorID != "dispatch_1" {
		t.Fatalf("broadcast start = %+v", started)
	}
}

func TestDispatchPrivateCallStartAndPartyGuard(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)
	bravo := e.addClient("bravo", auth.RoleField, 5)
	charlie := e.addClient("charlie", auth.RoleField, 5)

	// The wire carries the callee in the frame's GroupID field — the shape
	// the field clients encode.
	e.send(alpha, protocol.CallStart{
		Type: protocol.TypeCallStart, CallID: "c1",
		GroupID: "bravo", InitiatorID: "alpha", Kind: protocol.CallKindPrivate,
	})
	started := awaitFrame[protocol.CallStart](t, bravo, protocol.TypeCallStart, time.Second)
	if started.Kind != protocol.CallKindPrivate || started.InitiatorID != "alpha" {
		t.Fatalf("private start = %+v", started)
	}
	callID := started.CallID

	// Drain the call-start broadcast from charlie's queue so the
	// no-broadcast assertion below means "no join announcement".
	awaitFrame[protocol.CallStart](t, charlie, protocol.TypeCallStart, time.Second)

	// A third user is not a party of a private call: the join is refused
	// and no join broadcast goes out.
	e.send(charlie, protocol.CallJoined{Type: protocol.TypeCallJoined, CallID: callID})
	assertNoMsg(t, charlie)

	// The callee joins through the same frame.
	e.send(bravo, protocol.CallJoined{Type: protocol.TypeCallJoined, CallID: callID})
	joined := awaitFrame[protocol.CallJoined](t, alpha, protocol.TypeCallJoined, time.Second)
	if joined.UserID != "bravo" {
		t.Fatalf("join broadcast = %+v, want bravo", joined)
	}
	snap, err := e.sm.Snapshot(callID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Participants) != 2 {
		t.Fatalf("participants = %v, want [alpha bravo]", snap.Participants)
	}
}

// TestDispatchFloorRequestIdentityFromClaims pins the security property:
// the granted user is the connection's verified identity at the
// connection's recorded priority — the frame's spoofed userId and
// priority are ignored.
func TestDispatchFloorRequestIdentityFromClaims(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)
	bravo := e.addClient("bravo", auth.RoleField, 5)

	callID := e.startGroupCall(t, alpha)
	e.send(bravo, protocol.CallJoined{Type: protocol.TypeCallJoined, CallID: callID})
	awaitFrame[protocol.CallJoined](t, alpha, protocol.TypeCallJoined, time.Second)

	// bravo claims another identity and priority 10; the grant must name
	// bravo at his real P5 — a spoofed P10 would have been recorded.
	e.send(bravo, protocol.FloorRequest{
		Type: protocol.TypeFloorRequest, CallID: callID,
		UserID: "spoof", Priority: 10,
	})
	granted := awaitFrame[protocol.FloorGranted](t, alpha, protocol.TypeFloorGranted, time.Second)
	if granted.UserID != "bravo" {
		t.Fatalf("granted user = %q, want bravo (frame identity ignored)", granted.UserID)
	}
	snap, err := e.sm.Snapshot(callID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Speaker != "bravo" {
		t.Fatalf("speaker = %q, want bravo", snap.Speaker)
	}

	// alpha claims priority 10 too — but his connection is P5, so his
	// request queues behind the active speaker instead of pre-empting.
	e.send(alpha, protocol.FloorRequest{
		Type: protocol.TypeFloorRequest, CallID: callID,
		UserID: "alpha", Priority: 10,
	})
	denied := awaitFrame[protocol.FloorDenied](t, bravo, protocol.TypeFloorDenied, time.Second)
	if denied.UserID != "alpha" || denied.QueuePosition != 1 {
		t.Fatalf("queued denial = %+v, want alpha at position 1 (claimed P10 ignored)", denied)
	}
	if snap, _ := e.sm.Snapshot(callID); snap.Speaker != "bravo" {
		t.Fatalf("speaker changed to %q; a claimed priority must not pre-empt", snap.Speaker)
	}
}

// TestDispatchEmergencyFloorRequestPreempts: an ordinary user's emergency
// request (semantic flag, not an identity claim) upgrades to P9 and takes
// the floor mid-burst through the dispatch path.
func TestDispatchEmergencyFloorRequestPreempts(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)
	bravo := e.addClient("bravo", auth.RoleField, 5)

	callID := e.startGroupCall(t, alpha)
	e.send(bravo, protocol.CallJoined{Type: protocol.TypeCallJoined, CallID: callID})
	awaitFrame[protocol.CallJoined](t, alpha, protocol.TypeCallJoined, time.Second)
	e.send(alpha, protocol.FloorRequest{Type: protocol.TypeFloorRequest, CallID: callID})
	awaitFrame[protocol.FloorGranted](t, alpha, protocol.TypeFloorGranted, time.Second)
	// bravo also observes the live grant, so the frames after this point
	// are unambiguously the emergency arbitration.
	awaitFrame[protocol.FloorGranted](t, bravo, protocol.TypeFloorGranted, time.Second)

	e.send(bravo, protocol.FloorRequest{
		Type: protocol.TypeFloorRequest, CallID: callID,
		UserID: "bravo", Priority: 5, Emergency: true,
	})

	preempted := awaitFrame[protocol.FloorPreempted](t, alpha, protocol.TypeFloorPreempt, 2*time.Second)
	if preempted.By != "bravo" || !preempted.Emergency {
		t.Fatalf("pre-emption = %+v, want by bravo (emergency)", preempted)
	}
	granted := awaitFrame[protocol.FloorGranted](t, bravo, protocol.TypeFloorGranted, 2*time.Second)
	if granted.UserID != "bravo" {
		t.Fatalf("emergency grant = %+v, want bravo", granted)
	}
}

func TestDispatchFloorReleaseGrantsQueueHead(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)
	bravo := e.addClient("bravo", auth.RoleField, 5)

	callID := e.startGroupCall(t, alpha)
	e.send(bravo, protocol.CallJoined{Type: protocol.TypeCallJoined, CallID: callID})
	awaitFrame[protocol.CallJoined](t, alpha, protocol.TypeCallJoined, time.Second)
	e.send(alpha, protocol.FloorRequest{Type: protocol.TypeFloorRequest, CallID: callID})
	awaitFrame[protocol.FloorGranted](t, alpha, protocol.TypeFloorGranted, time.Second)
	e.send(bravo, protocol.FloorRequest{Type: protocol.TypeFloorRequest, CallID: callID})
	awaitFrame[protocol.FloorDenied](t, bravo, protocol.TypeFloorDenied, time.Second) // queued

	// PTT-up: the release echo fans out first, then the queue-head grant.
	e.send(alpha, protocol.FloorReleased{Type: protocol.TypeFloorReleased, CallID: callID})
	echo := awaitFrame[protocol.FloorReleased](t, alpha, protocol.TypeFloorReleased, time.Second)
	if echo.UserID != "alpha" {
		t.Fatalf("release echo = %+v, want alpha", echo)
	}
	granted := awaitFrame[protocol.FloorGranted](t, alpha, protocol.TypeFloorGranted, time.Second)
	if granted.UserID != "bravo" {
		t.Fatalf("queue-head grant = %+v, want bravo", granted)
	}
	if snap, _ := e.sm.Snapshot(callID); snap.Speaker != "bravo" {
		t.Fatalf("speaker = %q, want bravo after the release", snap.Speaker)
	}
}

// TestDispatchFloorRequestNotInCall: a request from a user who never
// joined is denied to the requester directly — the PTT state must resolve,
// not hang.
func TestDispatchFloorRequestNotInCall(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)
	charlie := e.addClient("charlie", auth.RoleField, 7)

	callID := e.startGroupCall(t, alpha)
	// Drain charlie's copy of the call-start broadcast before the request.
	awaitFrame[protocol.CallStart](t, charlie, protocol.TypeCallStart, time.Second)

	e.send(charlie, protocol.FloorRequest{Type: protocol.TypeFloorRequest, CallID: callID})
	denied := awaitFrame[protocol.FloorDenied](t, charlie, protocol.TypeFloorDenied, time.Second)
	if denied.Reason != "not-in-call" || denied.UserID != "charlie" {
		t.Fatalf("denial = %+v, want charlie / not-in-call", denied)
	}
}

func TestDispatchFloorRequestUnknownCall(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)

	e.send(alpha, protocol.FloorRequest{Type: protocol.TypeFloorRequest, CallID: "call_nope"})
	denied := awaitFrame[protocol.FloorDenied](t, alpha, protocol.TypeFloorDenied, time.Second)
	if denied.Reason != "unknown-call" {
		t.Fatalf("denial = %+v, want unknown-call", denied)
	}
}

// TestDispatchMalformedAndUnknownDrops: garbage, unknown types, and
// missing required fields are dropped silently without touching the
// manager — the forward-compatible wire behavior the foundation pinned.
func TestDispatchMalformedAndUnknownDrops(t *testing.T) {
	e := newDispatchEnv(t)
	alpha := e.addClient("alpha", auth.RoleField, 5)

	for _, raw := range []string{
		`not json at all`,
		`{"type":"NoSuchFrame","callId":"c1"}`,
		`{"type":"FloorRequest"}`,
		`{"type":"FloorReleased","callId":""}`,
		`{"type":"CallStart","callId":"c1","kind":"group","groupId":""}`,
		`{"type":"CallStart","callId":"c1","kind":"carrier-pigeon","groupId":"g1"}`,
	} {
		e.h.dispatchInbound(alpha, []byte(raw))
	}
	assertNoMsg(t, alpha)
	if n := e.log.count(t, callcontrol.EventCallStarted); n != 0 {
		t.Fatalf("malformed frames produced %d call-start events", n)
	}
}

// TestDispatchWithoutControllerIsInert: a handler with no wired call
// plane (control-plane-only deployments and media-less tests) drops
// floor/call frames instead of panicking.
func TestDispatchWithoutControllerIsInert(t *testing.T) {
	hub := NewHub()
	h := &Handler{hub: hub} // no SetCallController
	alpha := &Client{hub: hub, handler: h, userID: "alpha", role: auth.RoleField, priority: 5, send: make(chan []byte, 8), done: make(chan struct{})}
	hub.Add(alpha)

	h.dispatchInbound(alpha, []byte(`{"type":"FloorRequest","callId":"c1"}`))
	h.dispatchInbound(alpha, []byte(`{"type":"CallStart","callId":"c1","kind":"group","groupId":"g1"}`))
	assertNoMsg(t, alpha)
}

// TestDispatchDropsMediaOfferWithoutSDPHandler: a handler without the
// media plane wired answers no offers — and does not panic (the
// pre-existing media contract, preserved by the dispatch split).
func TestDispatchDropsMediaOfferWithoutSDPHandler(t *testing.T) {
	hub := NewHub()
	h := &Handler{hub: hub} // sdp nil
	alpha := &Client{hub: hub, handler: h, userID: "alpha", role: auth.RoleField, priority: 5, send: make(chan []byte, 8), done: make(chan struct{})}
	hub.Add(alpha)

	h.dispatchInbound(alpha, []byte(`{"type":"MediaOffer","callId":"c1","sdp":"v=0"}`))
	assertNoMsg(t, alpha)
}
