package ws

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
)

const (
	writeWait = 10 * time.Second // single write deadline
	// pongWait is the read deadline; a client pong resets it. Dead peers are
	// detected on the next read, not the next write.
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	// sendBuffer bounds each client's outbound queue; overflow drops (see the
	// Hub delivery contract in hub.go).
	sendBuffer = 64
)

// Client is one authenticated WSS connection. role and priority come from
// the verified access token at upgrade time — the per-connection auth
// context every inbound frame is dispatched with. Frame fields never name
// an identity: a client cannot speak as, or at the priority of, anyone
// else.
type Client struct {
	hub      *Hub
	handler  *Handler // inbound message dispatch; set by ServeHTTP
	userID   string
	role     string
	priority floor.FloorLevel
	conn     *websocket.Conn
	// send is never closed: a closed channel would turn a racing enqueue
	// (Broadcast snapshot vs removal) into a panic. The done channel is what
	// writePump exits on, and the hub closes it under its own lock.
	send chan []byte
	done chan struct{}
}

// enqueue adds one message; a full buffer drops it (slow client loses
// liveness messages rather than stalling the server).
func (c *Client) enqueue(msg []byte) {
	select {
	case c.send <- msg:
	default:
	}
}

// CallController is the call-control surface the inbound dispatch drives —
// the live SessionManager in production, satisfied at compile time below.
// The interface keeps the dispatch seam explicit: every client floor or
// call frame lands in the same manager the REST paths use, so arbitration
// and the media mirror cannot drift.
type CallController interface {
	StartGroup(groupID, initiator string, initiatorPriority floor.FloorLevel) (callcontrol.SessionInfo, error)
	StartPrivate(caller, callee string, callerPriority floor.FloorLevel, floorControl bool) (callcontrol.SessionInfo, error)
	StartBroadcast(groupID, dispatcher string, dispatcherPriority floor.FloorLevel) (callcontrol.SessionInfo, error)
	Join(callID, userID string, priority floor.FloorLevel) (callcontrol.SessionInfo, error)
	RequestFloor(callID, userID string, emergency bool) (floor.FloorDecision, error)
	ReleaseFloor(callID, userID string) ([]floor.FloorDecision, error)
}

var _ CallController = (*callcontrol.SessionManager)(nil)

// Handler upgrades and owns WSS connections. Token verification happens
// BEFORE the upgrade: /ws rejects unauthenticated dials with 401 like any
// other protected route.
type Handler struct {
	hub    *Hub
	tokens *auth.Tokenizer
	// sdp is the media plane (the SFU); nil until SetSDPHandler wires it in.
	sdp SDPHandler
	// calls is the call-control plane; nil until SetCallController wires
	// it in. Without it, floor and call frames are dropped (the dispatch
	// is inert) — control-plane-only deployments keep the wire alive.
	calls CallController
}

// SDPHandler answers one media-plane offer for a call. Implemented by the
// media SFU; the connection's userID is the authenticated WSS identity and
// must be bound by the room token too.
type SDPHandler interface {
	HandleOffer(callID, userID, token, offerSDP string) (answerSDP string, err error)
}

// SetSDPHandler wires the media plane in. Call once at startup, before the
// HTTP server begins serving — not safe for concurrent use.
func (h *Handler) SetSDPHandler(sdp SDPHandler) { h.sdp = sdp }

// SetCallController wires the call-control plane in — the same
// SessionManager construction main.go hands the REST layer. Call once at
// startup, before the HTTP server begins serving.
func (h *Handler) SetCallController(calls CallController) { h.calls = calls }

func NewHandler(hub *Hub, tokens *auth.Tokenizer) *Handler {
	return &Handler{hub: hub, tokens: tokens}
}

// ServeHTTP implements the /ws route (expects ?token=<jwt> — browsers cannot
// set headers on WebSocket dials).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	claims, err := h.tokens.Verify(token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// upgrader.Upgrade already replied with an HTTP error.
		return
	}

	c := &Client{
		hub:      h.hub,
		handler:  h,
		userID:   claims.Subject,
		role:     claims.Role,
		priority: floor.FloorLevel(claims.Priority),
		conn:     conn,
		send:     make(chan []byte, sendBuffer),
		done:     make(chan struct{}),
	}
	h.hub.Add(c)
	defer func() {
		if offline := h.hub.Remove(c); offline {
			h.broadcastPresence(c.userID, protocol.PresenceOffline)
		}
	}()

	h.broadcastPresence(c.userID, protocol.PresenceOnline)
	go c.writePump()
	c.readPump()
}

// BroadcastAffiliationChanged publishes an affiliation transition to every
// connected client — the "within one round trip" propagation path the API's
// affiliation handlers call.
func (h *Handler) BroadcastAffiliationChanged(userID, groupID, state string) {
	msg, err := marshalAffiliation(userID, groupID, state, time.Now())
	if err != nil {
		log.Printf("ws: marshal affiliation: %v", err)
		return
	}
	h.hub.Broadcast(msg)
}

// BroadcastCallEvent publishes one pre-rendered call-control or emergency
// frame (protocol JSON built by the api layer) to every connected client.
// The api layer owns rendering; the ws layer only fans out.
func (h *Handler) BroadcastCallEvent(msg []byte) {
	h.hub.Broadcast(msg)
}

// dispatchInbound routes one client frame into the plane that owns it:
// floor and call frames into the call-control services (through the
// connection's auth context — identity and priority are the verified
// claims, never frame fields), media offers to the SDP handler. Unknown,
// malformed, or unexpected types are dropped silently: the same
// forward-compatible wire behavior the foundation pinned. Floor frames
// that never reach a call (unknown call, requester not joined) are denied
// directly to the requester so their PTT state resolves; decisions that
// do land inside a call fan out through the SessionManager's event path
// (the same broadcasts the REST paths emit).
func (h *Handler) dispatchInbound(c *Client, raw []byte) {
	typ, err := protocol.ParseType(raw)
	if err != nil {
		return
	}
	switch typ {
	case protocol.TypeMediaOffer:
		h.dispatchMediaOffer(c, raw)
	case protocol.TypeFloorRequest:
		h.dispatchFloorRequest(c, raw)
	case protocol.TypeFloorReleased:
		h.dispatchFloorReleased(c, raw)
	case protocol.TypeCallStart:
		h.dispatchCallStart(c, raw)
	case protocol.TypeCallJoined:
		h.dispatchCallJoined(c, raw)
	}
}

// dispatchMediaOffer answers one media-plane offer in a goroutine —
// answering blocks on ICE gathering, and a read pump that stalled there
// would delay the connection's other frames. Replies ride the client's
// own outbound queue.
func (h *Handler) dispatchMediaOffer(c *Client, raw []byte) {
	if h.sdp == nil {
		return
	}
	var offer protocol.MediaOffer
	if err := json.Unmarshal(raw, &offer); err != nil || offer.CallID == "" {
		return
	}
	userID := c.userID
	go func() {
		answerSDP, err := h.sdp.HandleOffer(offer.CallID, userID, offer.Token, offer.SDP)
		resp := protocol.MediaAnswer{Type: protocol.TypeMediaAnswer, CallID: offer.CallID}
		if err != nil {
			resp.Err = err.Error()
		} else {
			resp.SDP = answerSDP
		}
		msg, merr := json.Marshal(resp)
		if merr != nil {
			log.Printf("ws: marshal media answer: %v", merr)
			return
		}
		c.enqueue(msg)
	}()
}

// dispatchFloorRequest routes one talk-burst request through the call's
// floor machine. The connection's identity and recorded priority are the
// only inputs that matter — a frame claiming another user or a higher
// priority is ignored (the session recorded the priority at join;
// client-claimed levels are never trusted). The emergency flag is a
// semantic request, not an identity claim, so it passes through: the
// machine upgrades the level to 9 inside the same arbitration.
func (h *Handler) dispatchFloorRequest(c *Client, raw []byte) {
	if h.calls == nil {
		return
	}
	var req protocol.FloorRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.CallID == "" {
		return
	}
	if _, err := h.calls.RequestFloor(req.CallID, c.userID, req.Emergency); err != nil {
		// The broadcast path carries every decision that landed inside a
		// call; a request that never reached one is denied to the
		// requester directly, with the machine's sentinel as the reason.
		h.denyFloor(c, req.CallID, floorDenialReason(err))
	}
}

// dispatchFloorReleased routes a PTT-up. The release echo (and any
// queue-head auto-grant) fans out through the event path.
func (h *Handler) dispatchFloorReleased(c *Client, raw []byte) {
	if h.calls == nil {
		return
	}
	var rel protocol.FloorReleased
	if err := json.Unmarshal(raw, &rel); err != nil || rel.CallID == "" {
		return
	}
	if _, err := h.calls.ReleaseFloor(rel.CallID, c.userID); err != nil {
		log.Printf("ws: floor release %s by %s: %v", rel.CallID, c.userID, err)
	}
}

// dispatchCallStart opens a call. The server mints the call ID (a
// client-chosen one is ignored — call IDs are unguessable server-side and
// every client learns the real one from the CallStart broadcast), and the
// initiator is the connection's user, not the frame's. Private calls
// carry the callee in the frame's GroupID field (the wire has no separate
// callee field; the field clients encode it there). Broadcast starts are
// dispatcher-only, mirroring the REST admin gate.
func (h *Handler) dispatchCallStart(c *Client, raw []byte) {
	if h.calls == nil {
		return
	}
	var start protocol.CallStart
	if err := json.Unmarshal(raw, &start); err != nil {
		return
	}
	switch start.Kind {
	case protocol.CallKindGroup:
		if start.GroupID == "" {
			return
		}
		if _, err := h.calls.StartGroup(start.GroupID, c.userID, c.priority); err != nil {
			log.Printf("ws: group call start %s by %s: %v", start.GroupID, c.userID, err)
		}
	case protocol.CallKindPrivate:
		if start.GroupID == "" {
			return
		}
		if _, err := h.calls.StartPrivate(c.userID, start.GroupID, c.priority, true); err != nil {
			log.Printf("ws: private call start by %s to %s: %v", c.userID, start.GroupID, err)
		}
	case protocol.CallKindBroadcast:
		if c.role != auth.RoleDispatcher {
			log.Printf("ws: broadcast start by %s denied: not a dispatcher", c.userID)
			return
		}
		if start.GroupID == "" {
			return
		}
		if _, err := h.calls.StartBroadcast(start.GroupID, c.userID, c.priority); err != nil {
			log.Printf("ws: broadcast start %s by %s: %v", start.GroupID, c.userID, err)
		}
	default:
		// Unknown or empty kind: dropped, like any unexpected type.
	}
}

// dispatchCallJoined admits a party to a live call (first join or late
// entry). Affiliation and party checks live in the manager; the join is
// announced to every client by the event path's CallJoined broadcast.
func (h *Handler) dispatchCallJoined(c *Client, raw []byte) {
	if h.calls == nil {
		return
	}
	var joined protocol.CallJoined
	if err := json.Unmarshal(raw, &joined); err != nil || joined.CallID == "" {
		return
	}
	if _, err := h.calls.Join(joined.CallID, c.userID, c.priority); err != nil {
		log.Printf("ws: join %s by %s: %v", joined.CallID, c.userID, err)
	}
}

// denyFloor sends one FloorDenied frame to the requester alone — the
// direct reply path for requests that never reached a call's arbitration.
func (h *Handler) denyFloor(c *Client, callID, reason string) {
	msg, err := json.Marshal(protocol.FloorDenied{
		Type: protocol.TypeFloorDenied, CallID: callID,
		UserID: c.userID, Reason: reason,
	})
	if err != nil {
		log.Printf("ws: marshal floor denial: %v", err)
		return
	}
	c.enqueue(msg)
}

// floorDenialReason maps a floor-request error to the wire's denial
// reason string.
func floorDenialReason(err error) string {
	switch {
	case errors.Is(err, callcontrol.ErrUnknownCall):
		return "unknown-call"
	case errors.Is(err, callcontrol.ErrNotInCall):
		return "not-in-call"
	default:
		log.Printf("ws: floor request: %v", err)
		return "error"
	}
}

// broadcastPresence publishes a presence transition to everyone.
func (h *Handler) broadcastPresence(userID, state string) {
	msg, err := marshalPresence(userID, state, time.Now())
	if err != nil {
		log.Printf("ws: marshal presence: %v", err)
		return
	}
	h.hub.Broadcast(msg)
}

func marshalPresence(userID, state string, at time.Time) ([]byte, error) {
	return json.Marshal(protocol.PresenceUpdate{
		Type:   protocol.TypePresenceUpdate,
		UserID: userID,
		State:  state,
		At:     at.UnixMilli(),
	})
}

func marshalAffiliation(userID, groupID, state string, at time.Time) ([]byte, error) {
	return json.Marshal(protocol.AffiliationChanged{
		Type:    protocol.TypeAffiliationChanged,
		UserID:  userID,
		GroupID: groupID,
		State:   state,
		At:      at.UnixMilli(),
	})
}

var upgrader = websocket.Upgrader{
	// Origin policy: dials are token-authenticated (a cross-origin browser
	// dial still needs a valid JWT, which this system never puts in cookies),
	// so the default same-origin refusal is relaxed. Revisit only if auth
	// ever moves to cookies.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// readPump reads inbound frames and hands each to the dispatch. Its read
// deadline is what detects dead peers; the deferred conn.Close ends the
// write pump's next write or ping.
func (c *Client) readPump() {
	defer func() { _ = c.conn.Close() }()
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if c.handler != nil {
			c.handler.dispatchInbound(c, raw)
		}
	}
}

// writePump flushes the send channel; pings keep half-open connections honest.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg := <-c.send:
			if err := c.write(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.write(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			// Hub removed this client: close and exit.
			_ = c.conn.Close()
			return
		}
	}
}

func (c *Client) write(messageType int, data []byte) error {
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return c.conn.WriteMessage(messageType, data)
}
