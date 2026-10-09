// Package ws implements the signaling hub: every client holds one WSS
// connection, and the hub fans out presence and affiliation (and, in later
// PRs, floor and call) events to all connected clients.
package ws

import (
	"sync"
)

// Hub is the fan-out point for server-broadcast messages.
//
// Delivery contract: Broadcast never blocks the caller (the REST round trip
// that caused an event must not wait on sockets), so a send that would fill a
// client's buffer is dropped for that client — a slow client loses liveness
// messages rather than stalling the server. Handlers re-sync via REST on
// reconnect. This is the foundation contract; the floor-engine PR adds
// per-call reliability where it matters (emergency retransmit).
type Hub struct {
	mu      sync.Mutex
	clients map[*Client]bool
	byUser  map[string]map[*Client]bool
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*Client]bool),
		byUser:  make(map[string]map[*Client]bool),
	}
}

// Add registers a client; the caller (ServeWS) broadcasts presence after.
func (h *Hub) Add(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = true
	if h.byUser[c.userID] == nil {
		h.byUser[c.userID] = make(map[*Client]bool)
	}
	h.byUser[c.userID][c] = true
}

// Remove drops a client, closes its done channel (unblocking writePump), and
// reports whether the user is now fully offline (no sockets left for that
// user id). Closing done under the hub lock is what makes Broadcast safe:
// any snapshot taken under the same lock contains only clients whose done
// channel is still open, so no enqueue can race a close.
func (h *Hub) Remove(c *Client) (userOffline bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.clients[c] {
		return false
	}
	delete(h.clients, c)
	if set := h.byUser[c.userID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.byUser, c.userID)
			userOffline = true
		}
	}
	close(c.done)
	return userOffline
}

// Broadcast fans a pre-encoded message out to every connected client.
func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	targets := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.enqueue(msg)
	}
}

// OnlineUserIDs lists users with at least one live socket (presence view).
func (h *Hub) OnlineUserIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.byUser))
	for id := range h.byUser {
		ids = append(ids, id)
	}
	return ids
}

// IsOnline reports whether the user has at least one live socket.
func (h *Hub) IsOnline(userID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byUser[userID]) > 0
}

// connectionCount exists for tests: total live sockets.
func (h *Hub) connectionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
