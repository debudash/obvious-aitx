package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// webhookCollector is an httptest-backed stand-in for the carrier's event
// receiver: it records every JSON body posted to it.
type webhookCollector struct {
	mu   sync.Mutex
	got  []map[string]any
	srv  *httptest.Server
	done chan struct{}
}

func newWebhookCollector(t *testing.T) *webhookCollector {
	t.Helper()
	c := &webhookCollector{done: make(chan struct{})}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("webhook body is not JSON: %v", err)
		}
		c.mu.Lock()
		c.got = append(c.got, body)
		n := len(c.got)
		c.mu.Unlock()
		if n == 1 {
			close(c.done) // signal first delivery
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *webhookCollector) bodies() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.got...)
}

func (c *webhookCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// eventNames returns the "event" field of every body, in arrival order.
func eventNames(bodies []map[string]any) []string {
	names := make([]string, 0, len(bodies))
	for _, b := range bodies {
		if s, ok := b["event"].(string); ok {
			names = append(names, s)
		}
	}
	return names
}

// hasEvent reports whether the named event arrived.
func hasEvent(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// waitEvents polls until at least n deliveries have arrived.
func waitEvents(c *webhookCollector, n int) {
	deadline := time.Now().Add(3 * time.Second)
	for c.count() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWebhookAdapter_callEventsArriveAsJSON — call start/ended arrive as
// JSON with timestamps (the acceptance row), through the real HTTP path.
func TestWebhookAdapter_callEventsArriveAsJSON(t *testing.T) {
	collector := newWebhookCollector(t)
	a := NewWebhookAdapter(collector.srv.URL)
	defer a.Close()

	now := time.Now()
	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "call_1", GroupID: "tg-fire", By: "alice", Timestamp: now})
	a.OnCallEvent(CallEvent{Type: CallEnded, CallID: "call_1", GroupID: "tg-fire", By: "alice", Timestamp: now})
	waitEvents(collector, 2)

	bodies := collector.bodies()
	if len(bodies) != 2 {
		t.Fatalf("webhook deliveries = %d, want 2", len(bodies))
	}
	names := eventNames(bodies)
	if !hasEvent(names, "call.started") || !hasEvent(names, "call.ended") {
		t.Fatalf("event names = %v, want call.started and call.ended", names)
	}
	for _, b := range bodies {
		ts, ok := b["ts"].(string)
		if !ok || ts == "" {
			t.Errorf("event %v: missing ts field", b["event"])
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("event %v: timestamp %q is not RFC3339: %v", b["event"], ts, err)
		}
		if b["callId"] != "call_1" {
			t.Errorf("event %v: callId = %v, want call_1", b["event"], b["callId"])
		}
	}
}

// TestWebhookAdapter_emergencyEventExplicit — an emergency floor grant
// additionally queues an explicit ptt.emergency event so carriers can route
// alarms without parsing grant events.
func TestWebhookAdapter_emergencyEventExplicit(t *testing.T) {
	collector := newWebhookCollector(t)
	a := NewWebhookAdapter(collector.srv.URL)
	defer a.Close()

	a.OnFloorGrant(FloorUpdate{
		CallID: "call_1", GroupID: "tg-fire", Talker: "bravo",
		Priority: 9, Emergency: true, Timestamp: time.Now(),
	})
	waitEvents(collector, 2)

	names := eventNames(collector.bodies())
	if !hasEvent(names, "floor.granted") {
		t.Errorf("event names = %v, want floor.granted", names)
	}
	if !hasEvent(names, "ptt.emergency") {
		t.Errorf("event names = %v, want explicit ptt.emergency for emergency grants", names)
	}
}

// TestWebhookAdapter_closeIsIdempotentAndBounded — the webhook is
// best-effort: Close stops the worker without an unbounded drain (the
// dispatcher rail and audit log own event durability), returns promptly,
// and is safe to call twice.
func TestWebhookAdapter_closeIsIdempotentAndBounded(t *testing.T) {
	collector := newWebhookCollector(t)
	a := NewWebhookAdapter(collector.srv.URL)

	a.OnCallEvent(CallEvent{Type: CallStarted, CallID: "call_2", Timestamp: time.Now()})
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := a.Close(); err != nil { // idempotent
		t.Fatalf("second Close: %v", err)
	}
	if n := collector.count(); n > 1 {
		t.Fatalf("deliveries after Close = %d, want 0 or 1 (worker stopped)", n)
	}
}
