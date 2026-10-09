package integration

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/config"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/media"
)

// fakeAdapter records every adapter call for assertions.
type fakeAdapter struct {
	mu       sync.Mutex
	calls    []CallEvent
	grants   []FloorUpdate
	releases []FloorUpdate
	frames   []AudioFrame
	closed   bool
}

func (f *fakeAdapter) OnCallEvent(ev CallEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ev)
}
func (f *fakeAdapter) OnFloorGrant(up FloorUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = append(f.grants, up)
}
func (f *fakeAdapter) OnFloorRelease(up FloorUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, up)
}
func (f *fakeAdapter) AudioSink() AudioSink { return f }
func (f *fakeAdapter) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
func (f *fakeAdapter) WriteAudio(frame AudioFrame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, frame)
}
func (f *fakeAdapter) callCount(ty CallEventType) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Type == ty {
			n++
		}
	}
	return n
}

// newTestBridge builds an enabled bridge with its adapters replaced by a
// fake (no sockets) and wires a real SessionManager's events to it — the
// same exported surface the dispatch wiring will use.
func newTestBridge(t *testing.T, groups ...string) (*Bridge, *fakeAdapter, *callcontrol.SessionManager) {
	t.Helper()
	b, err := NewFromConfig(config.CarrierConfig{
		Enabled: true,
		Adapter: "rtp",
		Codec:   "pcmu",
		Groups:  groups,
	}, BridgeOptions{})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if b == nil {
		t.Fatal("enabled config produced a nil bridge")
	}
	fake := &fakeAdapter{}
	b.mu.Lock()
	b.adapters = []RXAdapter{fake}
	b.mu.Unlock()

	sm := callcontrol.NewSessionManager(callcontrol.SessionConfig{
		OnEvent:     b.HandleEvent,
		Affiliation: func(userID, groupID string) bool { return true },
	})
	t.Cleanup(func() { _ = b.Close() })
	return b, fake, sm
}

// TestNewFromConfig_disabledReturnsNilBridge — the flag-off contract: a
// disabled section builds nothing at all.
func TestNewFromConfig_disabledReturnsNilBridge(t *testing.T) {
	b, err := NewFromConfig(config.CarrierConfig{Enabled: false, Adapter: "rtp"}, BridgeOptions{})
	if err != nil {
		t.Fatalf("disabled config returned an error: %v", err)
	}
	if b != nil {
		t.Fatal("disabled config built a bridge — the flag-off guarantee is broken")
	}
}

// TestBridge_flagOffOpensZeroSockets — the acceptance row verbatim: with
// carrier.enabled=false, pumping a full call lifecycle through the (nil)
// bridge performs zero dials. This is the test that fails the moment anyone
// wires a socket outside the flag gate.
func TestBridge_flagOffOpensZeroSockets(t *testing.T) {
	restore := dialFunc
	var dials atomic.Int64
	dialFunc = func(network, addr string) (net.Conn, error) {
		dials.Add(1)
		return nopConn{}, nil
	}
	t.Cleanup(func() { dialFunc = restore })

	b, err := NewFromConfig(config.CarrierConfig{Enabled: false, Adapter: "rtp", Groups: []string{"tg-fire"}}, BridgeOptions{})
	if err != nil || b != nil {
		t.Fatalf("disabled config produced (nil=%v, err=%v), want (nil, nil)", b == nil, err)
	}

	// The full lifecycle, exactly as wiring would drive it — every entry
	// point is nil-safe, and none may dial.
	var nilBridge *Bridge
	nilBridge.HandleEvent(callcontrol.Event{Type: callcontrol.EventCallStarted, CallID: "c1"})
	nilBridge.HandleEvent(callcontrol.Event{Type: callcontrol.EventCallEnded, CallID: "c1"})
	nilBridge.WriteTap(media.TapPacket{CallID: "c1", Codec: "opus", Payload: []byte{1}})
	if err := nilBridge.Close(); err != nil {
		t.Fatalf("nil bridge Close: %v", err)
	}

	time.Sleep(20 * time.Millisecond) // dial attempts would be asynchronous
	if n := dials.Load(); n != 0 {
		t.Fatalf("flag-off flow performed %d dial attempt(s), want 0 — zero-external-sockets guarantee broken", n)
	}
}

// TestBridge_flaggedGroupStreamsEndToEnd — a real SessionManager drives a
// flagged call through its exported event surface; the adapter sees the
// start, the grant, and the queue-head grant after release. Unflagged and
// private calls never reach the adapter.
func TestBridge_flaggedGroupStreamsEndToEnd(t *testing.T) {
	_, fake, sm := newTestBridge(t, "tg-fire")

	// Flagged group call: start, talker grant, release with a queued peer
	// (bob joins at alice's level: equal levels queue, they do not pre-empt).
	info, err := sm.StartGroup("tg-fire", "alice", 5)
	if err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	if _, err := sm.RequestFloor(info.CallID, "alice", false); err != nil {
		t.Fatalf("RequestFloor(alice): %v", err)
	}
	if _, err := sm.Join(info.CallID, "bob", 5); err != nil {
		t.Fatalf("Join(bob): %v", err)
	}
	if _, err := sm.RequestFloor(info.CallID, "bob", false); err != nil {
		t.Fatalf("RequestFloor(bob): %v", err) // queued behind alice
	}
	if _, err := sm.ReleaseFloor(info.CallID, "alice"); err != nil {
		t.Fatalf("ReleaseFloor(alice): %v", err) // queue head granted
	}

	if n := fake.callCount(CallStarted); n != 1 {
		t.Errorf("call.started events = %d, want 1", n)
	}
	// assertGrants checks the grant list under the fake's lock; the helper's
	// scope releases it before the later callCount reads in this test.
	assertGrants := func(want ...string) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.grants) != len(want) {
			t.Fatalf("grants = %d, want %d", len(fake.grants), len(want))
		}
		for i, w := range want {
			if fake.grants[i].Talker != w {
				t.Errorf("grants[%d] = %s, want %s", i, fake.grants[i].Talker, w)
			}
		}
	}
	assertGrants("alice", "bob")

	// Unflagged group with the flag still on: nothing may reach the adapter.
	other, err := sm.StartGroup("tg-other", "carol", 5)
	if err != nil {
		t.Fatalf("StartGroup(unflagged): %v", err)
	}
	if _, err := sm.RequestFloor(other.CallID, "carol", false); err != nil {
		t.Fatalf("RequestFloor(carol): %v", err) // succeeds; the bridge ignores it
	}
	if n := fake.callCount(CallStarted); n != 1 {
		t.Errorf("unflagged group produced a call event: call.started events = %d, want 1", n)
	}

	// Private calls never stream (they resolve to no talkgroup).
	if _, err := sm.StartPrivate("alice", "bob", 5, true); err != nil {
		t.Fatalf("StartPrivate: %v", err)
	}
	if n := fake.callCount(CallStarted); n != 1 {
		t.Errorf("private call produced a call event: call.started events = %d, want 1", n)
	}
}

// TestBridge_preemptionForwardsGrantAndRelease — a P10 pre-emption over an
// active P5 talker forwards the new grant AND the displaced talker's floor
// loss (reason "preempted") — the carrier follows the new grant like any
// other.
func TestBridge_preemptionForwardsGrantAndRelease(t *testing.T) {
	_, fake, sm := newTestBridge(t, "tg-fire")

	info, err := sm.StartGroup("tg-fire", "alice", 5)
	if err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	if _, err := sm.RequestFloor(info.CallID, "alice", false); err != nil {
		t.Fatalf("RequestFloor(alice): %v", err)
	}
	if _, err := sm.Join(info.CallID, "netcontrol", 10); err != nil {
		t.Fatalf("Join(netcontrol): %v", err)
	}
	if _, err := sm.RequestFloor(info.CallID, "netcontrol", false); err != nil {
		t.Fatalf("RequestFloor(netcontrol): %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	// The bridge mirrors every grant — the carrier hears the P5 burst start,
	// then the P10 takeover. The last grant must be the pre-emption.
	if len(fake.grants) != 2 {
		t.Fatalf("grants = %d (%+v), want 2 (the P5 burst and the P10 pre-emption)", len(fake.grants), fake.grants)
	}
	if last := fake.grants[1]; last.Talker != "netcontrol" || last.Priority != 10 {
		t.Fatalf("last grant = (%s, P%d), want (netcontrol, P10)", last.Talker, last.Priority)
	}
	if len(fake.releases) != 1 {
		t.Fatalf("releases = %d, want 1 (the pre-empted talker)", len(fake.releases))
	}
	if fake.releases[0].Talker != "alice" || fake.releases[0].Reason != "preempted" {
		t.Errorf("release = (%s, %s), want (alice, preempted)", fake.releases[0].Talker, fake.releases[0].Reason)
	}
}

// TestBridge_emergencyGrantFlowsThrough — an emergency request upgrades the
// priority inside the machine; the bridge forwards the grant with the
// emergency flag set on the same path as any other grant (spec: the carrier
// hears the emergency talker like any other grant).
func TestBridge_emergencyGrantFlowsThrough(t *testing.T) {
	_, fake, sm := newTestBridge(t, "tg-fire")

	info, err := sm.StartGroup("tg-fire", "alice", 5)
	if err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	if _, err := sm.RequestFloor(info.CallID, "alice", true); err != nil { // emergency
		t.Fatalf("RequestFloor(emergency): %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.grants) != 1 {
		t.Fatalf("grants = %d, want 1", len(fake.grants))
	}
	g := fake.grants[0]
	if g.Talker != "alice" || !g.Emergency || g.Priority != 9 {
		t.Errorf("emergency grant = (talker=%s, emergency=%v, priority=%d), want (alice, true, 9)",
			g.Talker, g.Emergency, g.Priority)
	}
}

// TestBridge_tapMirrorsForwardedPacketsOnly — the bridge's tap entry drops
// packets for calls it never opened (unflagged group, ended call) and
// forwards flagged-call packets to every adapter's audio sink as Opus
// frames — by construction the same packets the SFU's gate chose to relay.
func TestBridge_tapMirrorsForwardedPacketsOnly(t *testing.T) {
	b, fake, sm := newTestBridge(t, "tg-fire")
	b.AttachSFU(media.NewSFU(media.NewGate(), media.NewRoomTokens([]byte("test-secret-test-secret"), time.Minute)))

	info, err := sm.StartGroup("tg-fire", "alice", 5)
	if err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	other, err := sm.StartGroup("tg-other", "carol", 5)
	if err != nil {
		t.Fatalf("StartGroup(unflagged): %v", err)
	}

	payload := []byte{0xAA, 0xBB}
	b.WriteTap(media.TapPacket{CallID: info.CallID, UserID: "alice", Codec: "opus", Payload: payload})
	b.WriteTap(media.TapPacket{CallID: other.CallID, UserID: "carol", Codec: "opus", Payload: payload}) // unflagged
	b.WriteTap(media.TapPacket{CallID: "never-started", UserID: "x", Codec: "opus", Payload: payload})

	fake.mu.Lock()
	if len(fake.frames) != 1 {
		fake.mu.Unlock()
		t.Fatalf("audio frames = %d, want 1 (only the flagged call's packet)", len(fake.frames))
	}
	f := fake.frames[0]
	fake.mu.Unlock()
	if f.CallID != info.CallID || f.Codec != FrameOpus || string(f.Payload) != string(payload) {
		t.Errorf("frame = (call=%s, codec=%s, payload=% X), want (call=%s, opus, mirrored payload)",
			f.CallID, f.Codec, f.Payload, info.CallID)
	}

	// Call end tears the stream down: a later packet for the same call is
	// dropped.
	if err := sm.End(info.CallID, "alice"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if n := fake.callCount(CallEnded); n != 1 {
		t.Errorf("call.ended events = %d, want 1", n)
	}
	b.WriteTap(media.TapPacket{CallID: info.CallID, UserID: "alice", Codec: "opus", Payload: payload})
	if got := len(fake.frames); got != 1 {
		t.Errorf("ended call still accepted tap packets: frames = %d, want 1", got)
	}
}

// TestBridge_closeClosesAdapters — Close tears down streams and stops the
// adapters (idempotently).
func TestBridge_closeClosesAdapters(t *testing.T) {
	b, fake, sm := newTestBridge(t, "tg-fire")
	if _, err := sm.StartGroup("tg-fire", "alice", 5); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil { // idempotent
		t.Fatalf("second Close: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.closed {
		t.Error("Close did not reach the adapter")
	}
}
