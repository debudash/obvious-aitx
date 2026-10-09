package callcontrol

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// newTestManager builds a SessionManager with deterministic tokens, a static
// affiliation map, and an event recorder. groups maps userID → affiliated
// group IDs.
func newTestManager(t *testing.T, groups map[string][]string, cfgFns ...func(*SessionConfig)) (*SessionManager, *eventLog) {
	t.Helper()
	tokens := 0
	log := &eventLog{}
	cfg := SessionConfig{
		TokenGen: func() string {
			tokens++
			return fmt.Sprintf("tok-%d", tokens)
		},
		Affiliation: func(userID, groupID string) bool {
			for _, g := range groups[userID] {
				if g == groupID {
					return true
				}
			}
			return false
		},
		OnEvent: log.record,
	}
	for _, fn := range cfgFns {
		fn(&cfg)
	}
	return NewSessionManager(cfg), log
}

// eventLog records events from the manager's callback.
type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) record(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.events...)
}

func (l *eventLog) count(ty EventType) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if e.Type == ty {
			n++
		}
	}
	return n
}

// TestGroupCallLifecycle covers the group-call happy path and the session
// gates the spec puts around it: affiliation on start and join, floor
// traffic through the machine, and late entry that sees the live speaker.
func TestGroupCallLifecycle(t *testing.T) {
	groups := map[string][]string{
		"alpha":    {"tg1"},
		"bravo":    {"tg1"},
		"outsider": {},
	}
	sm, _ := newTestManager(t, groups)

	// Unaffiliated users cannot start a group call.
	if _, err := sm.StartGroup("tg1", "outsider", 4); !errors.Is(err, ErrNotAffiliated) {
		t.Fatalf("unaffiliated start: %v, want ErrNotAffiliated", err)
	}

	info, err := sm.StartGroup("tg1", "alpha", 4)
	if err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	if info.Kind != KindGroup || info.GroupID != "tg1" || !info.FloorControl {
		t.Fatalf("start info = %+v", info)
	}

	// An unaffiliated user cannot join even to an open call.
	if _, err := sm.Join(info.CallID, "outsider", 4); !errors.Is(err, ErrNotAffiliated) {
		t.Fatalf("unaffiliated join: %v, want ErrNotAffiliated", err)
	}

	if _, err := sm.Join(info.CallID, "bravo", 4); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := sm.Join(info.CallID, "bravo", 4); !errors.Is(err, ErrAlreadyParticipant) {
		t.Fatalf("double join: %v, want ErrAlreadyParticipant", err)
	}

	// Floor: alpha grants, bravo queues, alpha releases → bravo auto-grants.
	d, err := sm.RequestFloor(info.CallID, "alpha", false)
	if err != nil || d.Outcome != floor.OutcomeGranted {
		t.Fatalf("alpha request = %+v err=%v", d, err)
	}
	d, err = sm.RequestFloor(info.CallID, "bravo", false)
	if err != nil || d.Outcome != floor.OutcomeQueued || d.QueuePosition != 1 {
		t.Fatalf("bravo request = %+v err=%v", d, err)
	}
	ds, err := sm.ReleaseFloor(info.CallID, "alpha")
	if err != nil || len(ds) != 1 || ds[0].UserID != "bravo" {
		t.Fatalf("release = %+v err=%v", ds, err)
	}

	// Late entry: a member joining mid-call sees the current speaker.
	if _, err := sm.Join(info.CallID, "charlie", 4); !errors.Is(err, ErrNotAffiliated) {
		t.Fatalf("charlie join without affiliation: %v, want ErrNotAffiliated", err)
	}
	groups["charlie"] = []string{"tg1"}
	late, err := sm.Join(info.CallID, "charlie", 4)
	if err != nil {
		t.Fatalf("late join: %v", err)
	}
	if late.Speaker != "bravo" || late.SpeakerSince.IsZero() {
		t.Fatalf("late entry snapshot = %+v, want speaker bravo with since", late)
	}
}

// TestPrivateCallWithFloorControl: two parties only; floor traffic works; a
// third party cannot join; emergency pre-empts.
func TestPrivateCallWithFloorControl(t *testing.T) {
	sm, _ := newTestManager(t, nil)

	info, err := sm.StartPrivate("caller", "callee", 4, true)
	if err != nil {
		t.Fatalf("StartPrivate: %v", err)
	}
	if info.Kind != KindPrivate || !info.FloorControl {
		t.Fatalf("start info = %+v", info)
	}

	if _, err := sm.Join(info.CallID, "eavesdropper", 4); !errors.Is(err, ErrNotAParty) {
		t.Fatalf("third-party join: %v, want ErrNotAParty", err)
	}
	if _, err := sm.Join(info.CallID, "callee", 4); err != nil {
		t.Fatalf("callee join: %v", err)
	}

	if d, _ := sm.RequestFloor(info.CallID, "caller", false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("caller request = %+v", d)
	}
	if d, _ := sm.RequestFloor(info.CallID, "callee", false); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("callee request = %+v", d)
	}
	// Emergency pre-empts inside a private call.
	if d, _ := sm.RequestFloor(info.CallID, "callee", true); d.Outcome != floor.OutcomeGranted || d.PreemptedUserID != "caller" {
		t.Fatalf("emergency request = %+v", d)
	}
}

// TestDirectPrivateCall: floorControl=false — full duplex, stable tokens, no
// arbitration, and no revoke.
func TestDirectPrivateCall(t *testing.T) {
	sm, _ := newTestManager(t, nil)

	info, err := sm.StartPrivate("caller", "callee", 4, false)
	if err != nil {
		t.Fatalf("StartPrivate: %v", err)
	}
	if info.FloorControl {
		t.Fatalf("direct call reported floor control: %+v", info)
	}

	if _, err := sm.Join(info.CallID, "callee", 4); err != nil {
		t.Fatalf("callee join: %v", err)
	}
	d1, err := sm.RequestFloor(info.CallID, "caller", false)
	if err != nil || d1.Outcome != floor.OutcomeGranted || d1.Token == "" {
		t.Fatalf("caller direct request = %+v err=%v", d1, err)
	}
	// Both parties transmit simultaneously: the callee is granted while the
	// caller still holds their token.
	d2, err := sm.RequestFloor(info.CallID, "callee", false)
	if err != nil || d2.Outcome != floor.OutcomeGranted || d2.Token == "" {
		t.Fatalf("callee direct request = %+v err=%v", d2, err)
	}
	if d1.Token == d2.Token {
		t.Fatal("direct tokens are per participant — got the same token twice")
	}
	// Re-request restates the same token (idempotent).
	d3, _ := sm.RequestFloor(info.CallID, "caller", false)
	if d3.Token != d1.Token {
		t.Fatalf("direct token not stable: %s then %s", d1.Token, d3.Token)
	}
	// No floor to revoke on a direct call.
	if _, err := sm.RevokeFloor(info.CallID, "caller", "disp", 10); !errors.Is(err, ErrNoFloorControl) {
		t.Fatalf("revoke on direct call: %v, want ErrNoFloorControl", err)
	}
}

// TestBroadcastCall: dispatcher-only transmission; listeners denied with
// "listen-only"; start requires dispatcher priority.
func TestBroadcastCall(t *testing.T) {
	groups := map[string][]string{
		"disp":   {"tg1"},
		"member": {"tg1"},
	}
	sm, _ := newTestManager(t, groups)

	// A normal user cannot start a broadcast.
	if _, err := sm.StartBroadcast("tg1", "member", 4); err == nil {
		t.Fatal("normal user started a broadcast")
	}
	info, err := sm.StartBroadcast("tg1", "disp", floor.PriorityDispatcher)
	if err != nil {
		t.Fatalf("StartBroadcast: %v", err)
	}
	if info.Kind != KindBroadcast {
		t.Fatalf("kind = %q", info.Kind)
	}

	if _, err := sm.Join(info.CallID, "member", 4); err != nil {
		t.Fatalf("member join: %v", err)
	}
	// Listener PTT is denied with a reason, not an error — the client shows
	// the toast.
	d, err := sm.RequestFloor(info.CallID, "member", false)
	if err != nil || d.Outcome != floor.OutcomeDenied || d.Reason != "listen-only" {
		t.Fatalf("listener request = %+v err=%v", d, err)
	}
	// The dispatcher transmits.
	if d, _ := sm.RequestFloor(info.CallID, "disp", false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("dispatcher request = %+v", d)
	}
}

// TestRemoveParticipantAuthority: only an outranking caller can remove; a
// holding target loses the floor (not re-queued); a queued entry is
// cancelled on removal.
func TestRemoveParticipantAuthority(t *testing.T) {
	sm, _ := newTestManager(t, map[string][]string{"a": {"g"}, "b": {"g"}, "c": {"g"}})

	info, _ := sm.StartGroup("g", "a", 4)
	sm.Join(info.CallID, "b", 5)
	sm.Join(info.CallID, "c", 4)

	// Equal-level removal is refused.
	if _, err := sm.RemoveParticipant(info.CallID, "b", "a", 4); !errors.Is(err, ErrInsufficientAuthority) {
		t.Fatalf("equal-level removal: %v, want ErrInsufficientAuthority", err)
	}

	// The holder (a, P4) is removed by an outranking caller: floor stripped,
	// not re-queued.
	if d, _ := sm.RequestFloor(info.CallID, "a", false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("holder request = %+v", d)
	}
	ds, err := sm.RemoveParticipant(info.CallID, "a", "b", 5)
	if err != nil {
		t.Fatalf("removal: %v", err)
	}
	if len(ds) != 1 || ds[0].UserID != "a" || ds[0].Outcome != floor.OutcomeDenied || ds[0].Reason != "revoked" {
		t.Fatalf("removal decisions = %+v", ds)
	}
	snap, _ := sm.Snapshot(info.CallID)
	for _, uid := range snap.Participants {
		if uid == "a" {
			t.Fatalf("removed user still a participant: %+v", snap)
		}
	}
	if snap.Speaker != "" {
		t.Fatalf("speaker after removal = %q, want idle", snap.Speaker)
	}

	// A queued entry is cancelled on removal: c takes the floor, b
	// pre-empts (P5 > P4) and c lands in the queue, then b removes c.
	if d, _ := sm.RequestFloor(info.CallID, "c", false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("c request = %+v", d)
	}
	if d, _ := sm.RequestFloor(info.CallID, "b", false); d.Outcome != floor.OutcomeGranted || d.PreemptedUserID != "c" {
		t.Fatalf("b pre-emption = %+v", d)
	}
	if ds, err := sm.RemoveParticipant(info.CallID, "c", "b", 5); err != nil || len(ds) != 0 {
		t.Fatalf("queued removal = %+v err=%v", ds, err)
	}
	snap, _ = sm.Snapshot(info.CallID)
	if len(snap.Queue) != 0 {
		t.Fatalf("queue after removal = %+v, want empty", snap.Queue)
	}
}

// TestLeaveSemantics: a holder's leave auto-grants the queue head; the last
// member leaving ends a group call; a private call ends when either party
// leaves; a dispatcher can force-end.
func TestLeaveSemantics(t *testing.T) {
	sm, _ := newTestManager(t, map[string][]string{"a": {"g"}, "b": {"g"}})

	info, _ := sm.StartGroup("g", "a", 4)
	sm.Join(info.CallID, "b", 4)
	sm.RequestFloor(info.CallID, "a", false)
	if d, _ := sm.RequestFloor(info.CallID, "b", false); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("b request = %+v", d)
	}
	ds, err := sm.Leave(info.CallID, "a")
	if err != nil {
		t.Fatalf("leave: %v", err)
	}
	if len(ds) != 1 || ds[0].UserID != "b" || ds[0].Outcome != floor.OutcomeGranted {
		t.Fatalf("leave decisions = %+v, want head grant for b", ds)
	}
	if c := sm.Count(); c != 1 {
		t.Fatalf("live calls = %d, want 1", c)
	}

	// Last member leaves → the call ends.
	if _, err := sm.Leave(info.CallID, "b"); err != nil {
		t.Fatalf("b leave: %v", err)
	}
	if c := sm.Count(); c != 0 {
		t.Fatalf("live calls after last leave = %d, want 0", c)
	}
	if _, err := sm.Snapshot(info.CallID); !errors.Is(err, ErrUnknownCall) {
		t.Fatalf("snapshot of ended call: %v, want ErrUnknownCall", err)
	}

	// Private: either party leaving ends the call.
	pinfo, _ := sm.StartPrivate("caller", "callee", 4, true)
	sm.Join(pinfo.CallID, "callee", 4)
	if _, err := sm.Leave(pinfo.CallID, "caller"); err != nil {
		t.Fatalf("caller leave: %v", err)
	}
	if c := sm.Count(); c != 0 {
		t.Fatalf("live calls after private leave = %d, want 0", c)
	}

	// Dispatcher force-end.
	pinfo, _ = sm.StartPrivate("caller", "callee", 4, true)
	sm.Join(pinfo.CallID, "callee", 4)
	if err := sm.End(pinfo.CallID, "disp"); err != nil {
		t.Fatalf("End: %v", err)
	}
	if c := sm.Count(); c != 0 {
		t.Fatalf("live calls after End = %d, want 0", c)
	}
}

// TestSessionEvents: the manager emits lifecycle and floor events with the
// right types, so wiring can fan them out and audit them.
func TestSessionEvents(t *testing.T) {
	sm, log := newTestManager(t, map[string][]string{"a": {"g"}, "b": {"g"}})

	info, _ := sm.StartGroup("g", "a", 4)
	sm.Join(info.CallID, "b", 4)
	sm.RequestFloor(info.CallID, "a", false)
	sm.RequestFloor(info.CallID, "b", false)
	sm.ReleaseFloor(info.CallID, "a")
	sm.Leave(info.CallID, "a")
	sm.Leave(info.CallID, "b")

	evs := log.snapshot()
	if evs[0].Type != EventCallStarted || evs[0].Actor != "a" {
		t.Fatalf("first event = %+v, want CallStarted by a", evs[0])
	}
	if last := evs[len(evs)-1]; last.Type != EventCallEnded {
		t.Fatalf("last event = %+v, want CallEnded", last)
	}
	if n := log.count(EventParticipantJoined); n != 1 {
		t.Fatalf("joined events = %d, want 1", n)
	}
	if n := log.count(EventFloorDecisions); n != 3 { // grant, queue, head grant
		t.Fatalf("floor events = %d, want 3", n)
	}
	// The release echo goes out first, then the queue-head grant — the
	// wire contract's cause-before-effect for a PTT-up.
	if n := log.count(EventFloorReleased); n != 1 {
		t.Fatalf("release events = %d, want 1", n)
	}
	for i, ev := range evs {
		if ev.Type != EventFloorReleased {
			continue
		}
		if ev.Actor != "a" {
			t.Fatalf("release event actor = %q, want a", ev.Actor)
		}
		if i+1 >= len(evs) || evs[i+1].Type != EventFloorDecisions || len(evs[i+1].Decisions) == 0 {
			t.Fatalf("release at %d not followed by the head-grant decision event", i)
		}
		break
	}
}

// TestExpiryFlowsThroughSession: a burst past its bound auto-releases
// through the session's machine and the queue head is granted — the timer is
// per call, created by the manager.
func TestExpiryFlowsThroughSession(t *testing.T) {
	groups := map[string][]string{"a": {"g"}, "b": {"g"}}
	sm, log := newTestManager(t, groups, func(c *SessionConfig) {
		c.MaxTalkDuration = 40 * time.Millisecond
	})

	info, _ := sm.StartGroup("g", "a", 4)
	sm.Join(info.CallID, "b", 4)
	if d, _ := sm.RequestFloor(info.CallID, "a", false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("a request = %+v", d)
	}
	if d, _ := sm.RequestFloor(info.CallID, "b", false); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("b request = %+v", d)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := sm.Snapshot(info.CallID)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if snap.Speaker == "b" {
			// grant(a), queue(b), expiry head-grant(b)
			if n := log.count(EventFloorDecisions); n != 3 {
				t.Fatalf("floor events after expiry = %d, want 3", n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expiry did not grant the queue head through the session")
}
