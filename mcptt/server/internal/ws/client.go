package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
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

// Client is one authenticated WSS connection.
type Client struct {
	hub    *Hub
	userID string
	conn   *websocket.Conn
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

// Handler upgrades and owns WSS connections. Token verification happens
// BEFORE the upgrade: /ws rejects unauthenticated dials with 401 like any
// other protected route.
type Handler struct {
	hub    *Hub
	tokens *auth.Tokenizer
}

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
		hub:    h.hub,
		userID: claims.Subject,
		conn:   conn,
		send:   make(chan []byte, sendBuffer),
		done:   make(chan struct{}),
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

// readPump discards inbound frames: the foundation has no client→server WSS
// messages (floor control arrives with the floor-engine PR). Its real job is
// detecting dead peers via the read deadline; its deferred conn.Close ends
// the write pump's next write or ping.
func (c *Client) readPump() {
	defer func() { _ = c.conn.Close() }()
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
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
