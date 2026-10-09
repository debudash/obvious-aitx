package callcontrol

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// recordingGate captures every mirror write the control plane makes, in
// order, so the tests can assert the mirror followed each transition —
// grant, release, revoke, pre-emption, and expiry.
type recordingGate struct {
	mu  sync.Mutex
	ops []string
}

func (g *recordingGate) Apply(callID string, ds []floor.FloorDecision) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range ds {
		preempted := ""
		if d.PreemptedUserID != "" {
			preempted = fmt.Sprintf(" (drops %s)", d.PreemptedUserID)
		}
		g.ops = append(g.ops, fmt.Sprintf("apply %s@%s L%d%s", d.UserID, d.Token, d.Level, preempted))
	}
}

func (g *recordingGate) RevokeUser(callID, userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ops = append(g.ops, "revoke "+userID)
}

func (g *recordingGate) Clear(callID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ops = append(g.ops, "clear")
}

func (g *recordingGate) log() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.ops...)
}

// assertOps fails the test unless the mirror log ends with want (leading
// operations from earlier phases are ignored).
func assertOps(t *testing.T, g *recordingGate, want ...string) {
	t.Helper()
	got := g.log()
	if len(got) < len(want) {
		t.Fatalf("mirror log %v shorter than expected tail %v", got, want)
	}
	tail := got[len(got)-len(want):]
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("mirror tail = %v, want suffix %v", tail, want)
		}
	}
}

// TestGateMirrorsEveryFloorTransition — the scope-addition acceptance at
// the unit level: with the mirror wired, each machine transition lands in
// the gate inside the transition's critical section, including the two
// paths Apply alone cannot cover (release and expiry must revoke the
// holder the queue-head grant does not name).
func TestGateMirrorsEveryFloorTransition(t *testing.T) {
	rec := &recordingGate{}
	sm, _ := newTestManager(t, map[string][]string{
		"sup": {"g1"},
		"fld": {"g1"},
	}, func(c *SessionConfig) { c.Media = rec })

	call, err := sm.StartGroup("g1", "sup", floor.PrioritySupervisor)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := sm.Join(call.CallID, "fld", floor.PriorityNormal); err != nil {
		t.Fatalf("join: %v", err)
	}

	// Grant: the mirror admits exactly the granted user.
	if d, err := sm.RequestFloor(call.CallID, "sup", false); err != nil || d.Outcome != floor.OutcomeGranted {
		t.Fatalf("sup request: %v %v", d, err)
	}
	assertOps(t, rec, "apply sup@tok-2 L7")

	// Pre-emption: displaced talker revoked, emergency talker applied —
	// the P9 grant names the displaced user and Apply would drop them, but
	// the machine's strip already revoked them in the same critical
	// section, so the mirror never even transiently shows both.
	d, err := sm.RequestFloor(call.CallID, "fld", true)
	if err != nil || d.Outcome != floor.OutcomeGranted || d.Level != floor.PriorityEmergency {
		t.Fatalf("emergency request: %v %v", d, err)
	}
	assertOps(t, rec, "revoke sup", "apply fld@tok-3 L9 (drops sup)")

	// Emergency release: holder revoked; queue holds the displaced
	// supervisor, who is re-granted by the same transition.
	if ds, err := sm.ReleaseFloor(call.CallID, "fld"); err != nil || len(ds) != 1 {
		t.Fatalf("release: %v %v", ds, err)
	}
	assertOps(t, rec, "revoke fld", "apply sup@tok-4 L7")

	// Plain release with an empty queue: holder revoked, nothing granted.
	if ds, err := sm.ReleaseFloor(call.CallID, "sup"); err != nil || len(ds) != 0 {
		t.Fatalf("release: %v %v", ds, err)
	}
	assertOps(t, rec, "revoke sup")

	// Dispatcher revoke strips the holder and the queue head continues.
	if _, err := sm.RequestFloor(call.CallID, "fld", false); err != nil {
		t.Fatalf("fld request: %v", err)
	}
	if _, err := sm.RevokeFloor(call.CallID, "fld", "dispatch", floor.PriorityDispatcher); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	assertOps(t, rec, "apply fld@tok-5 L4", "revoke fld")

	// Teardown clears the mirror with the call.
	if err := sm.End(call.CallID, "dispatch"); err != nil {
		t.Fatalf("end: %v", err)
	}
	assertOps(t, rec, "clear")
	if sm.Count() != 0 {
		t.Fatalf("call count = %d, want 0", sm.Count())
	}
}

// TestGateMirrorsExpiry — a max-duration expiry is a floor transition like
// any other: the expired holder loses relay permission and the queue head
// (if any) is admitted, both inside the machine's critical section.
func TestGateMirrorsExpiry(t *testing.T) {
	rec := &recordingGate{}
	sm, _ := newTestManager(t, map[string][]string{
		"a": {"g1"},
		"b": {"g1"},
	}, func(c *SessionConfig) {
		c.Media = rec
		c.MaxTalkDuration = 30 * time.Millisecond
	})

	call, err := sm.StartGroup("g1", "a", floor.PriorityNormal)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := sm.Join(call.CallID, "b", floor.PriorityNormal); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := sm.RequestFloor(call.CallID, "b", false); err != nil {
		t.Fatalf("b request: %v", err)
	}
	if _, err := sm.RequestFloor(call.CallID, "a", false); err != nil {
		t.Fatalf("a request: %v", err)
	}
	// Queueing writes nothing to the mirror — b still holds the floor and
	// no permission changed hands.
	assertOps(t, rec, "apply b@tok-2 L4")

	// The expiry fires on the timer goroutine; wait past the burst bound.
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err := sm.Snapshot(call.CallID)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if info.Speaker == "a" && len(info.Queue) == 0 {
			break // expired holder's burst freed the floor to the queued head
		}
		if time.Now().After(deadline) {
			t.Fatalf("expiry never re-granted the queue head: %+v", info)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The mirror followed the transition: holder b revoked, head a applied.
	assertOps(t, rec, "revoke b", "apply a@tok-3 L4")
}

// TestGateDirectCallMirror — direct private calls have no machine; their
// grants and releases are mirrored by the session layer under the manager
// lock, and the call's teardown clears the mirror.
func TestGateDirectCallMirror(t *testing.T) {
	rec := &recordingGate{}
	sm, _ := newTestManager(t, nil, func(c *SessionConfig) { c.Media = rec })

	call, err := sm.StartPrivate("a", "b", floor.PriorityNormal, false)
	if err != nil {
		t.Fatalf("start private: %v", err)
	}
	if _, err := sm.Join(call.CallID, "b", floor.PriorityNormal); err != nil {
		t.Fatalf("join: %v", err)
	}
	d, err := sm.RequestFloor(call.CallID, "a", false)
	if err != nil || d.Outcome != floor.OutcomeGranted || d.Token == "" {
		t.Fatalf("direct request: %v %v", d, err)
	}
	assertOps(t, rec, fmt.Sprintf("apply a@%s L4", d.Token))

	if _, err := sm.ReleaseFloor(call.CallID, "a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	assertOps(t, rec, "revoke a")

	if _, err := sm.Leave(call.CallID, "a"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	assertOps(t, rec, "clear") // private call ends when either party leaves
}

// TestEscalateEmergencyEffects — the three session-level emergency
// effects: priority upgrade to 9, pre-emption of a lower holder, and the
// emergency state visible on the snapshot. Imminent peril is recorded as
// the distinct flagged mode.
func TestEscalateEmergencyEffects(t *testing.T) {
	rec := &recordingGate{}
	sm, _ := newTestManager(t, map[string][]string{
		"sup": {"g1"},
		"fld": {"g1"},
	}, func(c *SessionConfig) { c.Media = rec })

	call, err := sm.StartGroup("g1", "sup", floor.PrioritySupervisor)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	callID := call.CallID
	if _, err := sm.Join(callID, "fld", floor.PriorityNormal); err != nil {
		t.Fatalf("join: %v", err)
	}
	if d, err := sm.RequestFloor(callID, "sup", false); err != nil || d.Outcome != floor.OutcomeGranted {
		t.Fatalf("sup request: %v %v", d, err)
	}

	// The P5 field user's emergency press takes the floor from the P8
	// supervisor at level 9 — priority upgrade and pre-emption in one
	// scheduling turn.
	d, info, err := sm.EscalateEmergency(callID, "fld", false)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if d.Outcome != floor.OutcomeGranted || d.Level != floor.PriorityEmergency || d.PreemptedUserID != "sup" {
		t.Fatalf("escalation decision = %+v, want granted at L9 displacing sup", d)
	}
	if !info.Emergency || info.ImminentPeril || info.Speaker != "fld" {
		t.Fatalf("escalated snapshot = %+v", info)
	}

	// Every later burst from the escalated user arbitrates at 9 — even a
	// plain (non-emergency-flagged) request.
	if ds, err := sm.ReleaseFloor(callID, "fld"); err != nil || len(ds) != 1 || ds[0].UserID != "sup" {
		t.Fatalf("release: %v %v", ds, err)
	}
	d2, err := sm.RequestFloor(callID, "fld", false)
	if err != nil || d2.Outcome != floor.OutcomeGranted || d2.Level != floor.PriorityEmergency {
		t.Fatalf("post-escalation plain request = %+v, want grant at L9", d2)
	}
}

// TestEscalateEmergencyQueuesBehindNetControl — the dispatcher's P10
// outranks the P9 emergency tier: the escalated user queues rather than
// stripping net control.
func TestEscalateEmergencyQueuesBehindNetControl(t *testing.T) {
	sm, _ := newTestManager(t, map[string][]string{"disp": {"g1"}, "fld": {"g1"}})
	call, err := sm.StartGroup("g1", "disp", floor.PriorityDispatcher)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := sm.Join(call.CallID, "fld", floor.PriorityNormal); err != nil {
		t.Fatalf("join: %v", err)
	}
	if d, err := sm.RequestFloor(call.CallID, "disp", false); err != nil || d.Outcome != floor.OutcomeGranted {
		t.Fatalf("disp request: %v %v", d, err)
	}
	d, info, err := sm.EscalateEmergency(call.CallID, "fld", true)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if d.Outcome != floor.OutcomeQueued || d.QueuePosition != 1 {
		t.Fatalf("escalation decision = %+v, want queued at position 1", d)
	}
	if !info.Emergency || !info.ImminentPeril {
		t.Fatalf("imminent-peril flags missing from snapshot: %+v", info)
	}
}

// TestEscalateEmergencyEdgePaths — broadcast listeners are denied the
// floor but still escalate the call's state; unknown calls and non-members
// are rejected; direct calls restate their grant.
func TestEscalateEmergencyEdgePaths(t *testing.T) {
	rec := &recordingGate{}
	sm, _ := newTestManager(t, map[string][]string{"disp": {"g1"}, "fld": {"g1"}}, func(c *SessionConfig) { c.Media = rec })

	bc, err := sm.StartBroadcast("g1", "disp", floor.PriorityDispatcher)
	if err != nil {
		t.Fatalf("start broadcast: %v", err)
	}
	if _, err := sm.Join(bc.CallID, "fld", floor.PriorityNormal); err != nil {
		t.Fatalf("join: %v", err)
	}
	d, info, err := sm.EscalateEmergency(bc.CallID, "fld", false)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if d.Outcome != floor.OutcomeDenied || d.Reason != "listen-only" {
		t.Fatalf("broadcast escalation decision = %+v, want listen-only denial", d)
	}
	if !info.Emergency || info.Speaker != "" {
		t.Fatalf("broadcast escalated snapshot = %+v", info)
	}

	priv, err := sm.StartPrivate("disp", "fld", floor.PriorityDispatcher, false)
	if err != nil {
		t.Fatalf("start private: %v", err)
	}
	if _, err := sm.Join(priv.CallID, "fld", floor.PriorityNormal); err != nil {
		t.Fatalf("join: %v", err)
	}
	d, _, err = sm.EscalateEmergency(priv.CallID, "disp", true)
	if err != nil || d.Outcome != floor.OutcomeGranted || d.Level != floor.PriorityEmergency {
		t.Fatalf("direct-call escalation = %v %v, want restated grant at L9", d, err)
	}

	if _, _, err := sm.EscalateEmergency("call_none", "fld", false); err != ErrUnknownCall {
		t.Fatalf("unknown call escalation = %v, want ErrUnknownCall", err)
	}
	if _, _, err := sm.EscalateEmergency(bc.CallID, "stranger", false); err != ErrNotInCall {
		t.Fatalf("non-member escalation = %v, want ErrNotInCall", err)
	}
	if strings.Join(rec.log(), "|") == "" {
		t.Fatal("mirror saw no writes at all")
	}
}
