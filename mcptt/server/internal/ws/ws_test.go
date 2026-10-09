package ws

import (
	"testing"
	"time"
)

// White-box hub tests: the end-to-end socket behavior (dial with token,
// receive broadcasts) lives in the api package's integration suite.

func newTestClient(h *Hub, userID string, buf int) *Client {
	return &Client{hub: h, userID: userID, send: make(chan []byte, buf), done: make(chan struct{})}
}

func recv(t *testing.T, c *Client) []byte {
	t.Helper()
	select {
	case msg := <-c.send:
		return msg
	case <-time.After(time.Second):
		t.Fatal("no message within 1s")
		return nil
	}
}

func assertNoMsg(t *testing.T, c *Client) {
	t.Helper()
	select {
	case msg := <-c.send:
		t.Fatalf("unexpected message %q", msg)
	default:
	}
}

func TestHubTracksOnlineUsers(t *testing.T) {
	h := NewHub()
	c1 := newTestClient(h, "u1", 8)
	h.Add(c1)
	if !h.IsOnline("u1") {
		t.Error("u1 should be online after Add")
	}
	if ids := h.OnlineUserIDs(); len(ids) != 1 || ids[0] != "u1" {
		t.Errorf("OnlineUserIDs = %v, want [u1]", ids)
	}

	// Second connection for the same user: still one online user.
	c1b := newTestClient(h, "u1", 8)
	h.Add(c1b)
	if ids := h.OnlineUserIDs(); len(ids) != 1 {
		t.Errorf("OnlineUserIDs with two sockets = %v, want one entry", ids)
	}

	// Removing one socket keeps the user online.
	if offline := h.Remove(c1); offline {
		t.Error("Remove(c1) reported offline while a second socket lives")
	}
	if !h.IsOnline("u1") {
		t.Error("u1 should still be online")
	}

	// Removing the last socket flips the user offline.
	if offline := h.Remove(c1b); !offline {
		t.Error("Remove(c1b) should report the user offline")
	}
	if h.IsOnline("u1") {
		t.Error("u1 should be offline after last socket removal")
	}
}

func TestHubBroadcastFanout(t *testing.T) {
	h := NewHub()
	a := newTestClient(h, "a", 8)
	b := newTestClient(h, "b", 8)
	h.Add(a)
	h.Add(b)

	h.Broadcast([]byte(`{"type":"PresenceUpdate"}`))
	recv(t, a)
	recv(t, b)

	// A client added later also receives subsequent broadcasts.
	c := newTestClient(h, "c", 8)
	h.Add(c)
	h.Broadcast([]byte(`{"type":"AffiliationChanged"}`))
	recv(t, a)
	recv(t, b)
	recv(t, c)
}

// TestHubBroadcastDropsForSlowClient pins the delivery contract: Broadcast
// never blocks the caller, and a full buffer loses the message for that
// client only.
func TestHubBroadcastDropsForSlowClient(t *testing.T) {
	h := NewHub()
	slow := newTestClient(h, "slow", 1)
	fast := newTestClient(h, "fast", 8)
	h.Add(slow)
	h.Add(fast)

	done := make(chan struct{})
	go func() {
		h.Broadcast([]byte(`{"n":1}`))
		h.Broadcast([]byte(`{"n":2}`))
		h.Broadcast([]byte(`{"n":3}`))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked the caller")
	}

	if got := recv(t, slow); string(got) != `{"n":1}` {
		t.Errorf("slow client first message = %s, want {\"n\":1}", got)
	}
	assertNoMsg(t, slow) // 2 and 3 dropped: buffer of 1
	// fast drains everything; the channel holds messages in order, so the
	// third receive must be the final broadcast.
	recv(t, fast)
	recv(t, fast)
	if got := recv(t, fast); string(got) != `{"n":3}` {
		t.Errorf("fast client last message = %s, want {\"n\":3}", got)
	}
}

// TestRemoveUnknownClientIsSafe covers the guard for double removal.
func TestRemoveUnknownClientIsSafe(t *testing.T) {
	h := NewHub()
	stray := newTestClient(h, "x", 1)
	if offline := h.Remove(stray); offline {
		t.Error("Remove of an unknown client must be a no-op, not an offline flip")
	}
}
