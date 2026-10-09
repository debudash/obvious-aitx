package callcontrol

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// newTestMachine builds a machine with deterministic tokens (tok-1, tok-2, …).
// TokenGen runs under the machine lock, so a plain counter is race-free;
// zero durations/depths fall back to the spec defaults (60 s, 16).
func newTestMachine(maxTalk time.Duration, maxQueue int) *floorMachine {
	tokens := 0
	return newFloorMachine(MachineConfig{
		MaxTalkDuration: maxTalk,
		MaxQueueDepth:   maxQueue,
		TokenGen: func() string {
			tokens++
			return fmt.Sprintf("tok-%d", tokens)
		},
	})
}

// TestFloorStateMachineTable walks every row of the spec's floor-state table:
// the row's entry transition, the decision the requester sees, and every exit
// the row names. Max-duration expiry is timed separately (it needs real
// timers); the pre-empted row's full exit path lives in rowPreempted.
func TestFloorStateMachineTable(t *testing.T) {
	cases := []struct {
		name string // the spec table row
		run  func(t *testing.T, m *floorMachine)
	}{
		{"idle", rowIdle},
		{"granted", rowGranted},
		{"queued", rowQueued},
		{"denied", rowDenied},
		{"pre-empted", rowPreempted},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, newTestMachine(0, 0))
		})
	}
}

// rowIdle — entry: call open, nobody talking. Client sees PTT ready. Exit:
// grant on request (the emergency pre-emption exit is rowPreempted's case).
func rowIdle(t *testing.T, m *floorMachine) {
	if m.State() != stateIdle {
		t.Fatalf("fresh machine state = %q, want idle", m.State())
	}
	if q := m.Queue(); len(q) != 0 {
		t.Fatalf("fresh machine queue = %v, want empty", q)
	}
	if _, ok := m.Grant(); ok {
		t.Fatal("fresh machine reports an active grant")
	}
	d := m.Request("u1", floor.PriorityNormal, false)
	if d.Outcome != floor.OutcomeGranted || d.Token == "" {
		t.Fatalf("idle request = %+v, want grant with token", d)
	}
	g, ok := m.Grant()
	if !ok || g.UserID != "u1" || g.Token != d.Token {
		t.Fatalf("grant after idle request = %+v ok=%v", g, ok)
	}
}

// rowGranted — entry: request wins outright. Client sees the transmitting
// indicator and talk timer. Exits verified here: release and revoke; expiry
// and pre-emption have dedicated tests below.
func rowGranted(t *testing.T, m *floorMachine) {
	if d := m.Request("u1", floor.PriorityNormal, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("first request = %+v, want grant", d)
	}

	// Exit: release — floor frees; no queue, so no auto-grant.
	if ds := m.Release("u1"); len(ds) != 0 {
		t.Fatalf("release with empty queue = %v, want none", ds)
	}
	if m.State() != stateIdle {
		t.Fatalf("state after release = %q, want idle", m.State())
	}

	// Exit: revoke — dispatcher strips the burst; the talker is not queued.
	if d := m.Request("u1", floor.PriorityNormal, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("re-grant = %+v", d)
	}
	ds, err := m.Revoke("u1", "disp", floor.PriorityDispatcher)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if len(ds) != 1 || ds[0].UserID != "u1" || ds[0].Reason != "revoked" {
		t.Fatalf("revoke decisions = %+v", ds)
	}
	if m.State() != stateIdle {
		t.Fatalf("state after revoke = %q, want idle", m.State())
	}
	if _, ok := m.Grant(); ok {
		t.Fatal("grant still active after revoke — token not cleared")
	}
}

// rowQueued — entry: request while floor held. Client sees position and
// cancel affordance. Exits: auto-grant at queue head, or cancel.
func rowQueued(t *testing.T, m *floorMachine) {
	// A supervisor (P7) holds: normal users queue, none can pre-empt.
	if d := m.Request("sup", floor.PrioritySupervisor, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("holder grant = %+v", d)
	}

	d4 := m.Request("u4", 4, false)
	if d4.Outcome != floor.OutcomeQueued || d4.QueuePosition != 1 {
		t.Fatalf("first queue request = %+v, want queued #1", d4)
	}
	d6 := m.Request("u6", 6, false)
	if d6.Outcome != floor.OutcomeQueued || d6.QueuePosition != 1 {
		t.Fatalf("higher-priority request = %+v, want queued #1 (above u4)", d6)
	}
	d5 := m.Request("u5", 5, false)
	if d5.Outcome != floor.OutcomeQueued || d5.QueuePosition != 2 {
		t.Fatalf("mid-priority request = %+v, want queued #2", d5)
	}
	if got := queueIDs(m); got[0] != "u6" || got[1] != "u5" || got[2] != "u4" {
		t.Fatalf("queue order = %v, want [u6 u5 u4] (priority desc, FIFO ties)", got)
	}

	// Exit: cancel — a queued user releases and leaves the queue.
	if ds := m.Release("u5"); len(ds) != 0 {
		t.Fatalf("queued cancel produced decisions %v, want none", ds)
	}
	if got := queueIDs(m); len(got) != 2 || got[0] != "u6" || got[1] != "u4" {
		t.Fatalf("queue after cancel = %v, want [u6 u4]", got)
	}

	// Exit: auto-grant at head — the holder releases, the head is granted
	// synchronously inside the Release call.
	ds := m.Release("sup")
	if len(ds) != 1 || ds[0].UserID != "u6" || ds[0].Outcome != floor.OutcomeGranted || ds[0].Token == "" {
		t.Fatalf("head auto-grant = %+v, want grant for u6", ds)
	}
	g, ok := m.Grant()
	if !ok || g.UserID != "u6" || g.Token != ds[0].Token {
		t.Fatalf("holder after release = %+v ok=%v", g, ok)
	}

	// A queued user re-requesting restates their position, never re-enqueues.
	if d := m.Request("u4", 4, false); d.Outcome != floor.OutcomeQueued || d.QueuePosition != 1 {
		t.Fatalf("re-request while queued = %+v, want queued #1", d)
	}
	if got := queueIDs(m); len(got) != 1 || got[0] != "u4" {
		t.Fatalf("queue after re-request = %v, want [u4] exactly once", got)
	}
}

// rowDenied — entry: request rejected (queue spillover here; affiliation,
// participation, and broadcast-lock denials live in the session tests, which
// own those rules). Client sees a reason toast. Exit: idle after
// acknowledgement; may re-request.
func rowDenied(t *testing.T, m *floorMachine) {
	small := newTestMachine(0, 1) // queue depth 1 → third requester spills over
	if d := small.Request("u1", 4, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("holder grant = %+v", d)
	}
	if d := small.Request("u2", 4, false); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("queue fill = %+v", d)
	}
	d := small.Request("u3", 4, false)
	if d.Outcome != floor.OutcomeDenied || d.Reason != "busy" || d.QueuePosition != 2 {
		t.Fatalf("spillover = %+v, want denied busy at position 2", d)
	}

	// Exit: may re-request — the denied user presses again once the queue
	// has room and queues normally.
	small.Release("u2")
	if d := small.Request("u3", 4, false); d.Outcome != floor.OutcomeQueued || d.QueuePosition != 1 {
		t.Fatalf("re-request after deny = %+v, want queued #1", d)
	}

	// Defensive: out-of-ladder levels deny, never grant or panic.
	for _, lvl := range []floor.FloorLevel{0, 11, -3} {
		if d := m.Request("u1", lvl, false); d.Outcome != floor.OutcomeDenied || d.Reason != "invalid-priority" {
			t.Fatalf("level %d = %+v, want denied invalid-priority", lvl, d)
		}
	}
}

// rowPreempted — entry: a higher-priority request mid-burst. Client sees the
// audio cut and a "pre-empted by …" banner. Exit: idle; original speaker
// joins the queue if still holding PTT (Release cancels that entry).
func rowPreempted(t *testing.T, m *floorMachine) {
	g1 := m.Request("field", 4, false)
	if g1.Outcome != floor.OutcomeGranted {
		t.Fatalf("initial grant = %+v", g1)
	}

	d := m.Request("net", floor.PriorityDispatcher, false)
	if d.Outcome != floor.OutcomeGranted || d.PreemptedUserID != "field" {
		t.Fatalf("pre-empting request = %+v, want grant displacing field", d)
	}
	if d.Token == g1.Token {
		t.Fatal("floor token not rotated on pre-emption")
	}
	if g, _ := m.Grant(); g.UserID != "net" || g.Token != d.Token {
		t.Fatalf("holder after pre-emption = %+v", g)
	}
	// Original speaker joins the queue (still holding PTT).
	if got := queueIDs(m); len(got) != 1 || got[0] != "field" {
		t.Fatalf("queue after pre-emption = %v, want [field]", got)
	}

	// Letting go of PTT cancels the re-queue entry.
	if ds := m.Release("field"); len(ds) != 0 {
		t.Fatalf("release after pre-emption = %v, want none", ds)
	}
	if got := queueIDs(m); len(got) != 0 {
		t.Fatalf("queue after release = %v, want empty", got)
	}

	// Emergency (9) cannot pre-empt the dispatcher's 10 — it queues instead.
	if d := m.Request("field2", 5, true); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("emergency request vs P10 = %+v, want queued", d)
	}
	if ds := m.Release("field2"); len(ds) != 0 {
		t.Fatalf("cancel queued emergency entry = %v, want none", ds)
	}

	// Emergency vs a NORMAL holder: upgraded to 9, pre-empts mid-burst.
	if ds := m.Release("net"); len(ds) != 0 { // queue empty → idle
		t.Fatalf("net release = %v, want none", ds)
	}
	if d := m.Request("field4", 4, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("normal holder grant = %+v", d)
	}
	d = m.Request("field3", 5, true)
	if d.Outcome != floor.OutcomeGranted || d.PreemptedUserID != "field4" || !d.Emergency {
		t.Fatalf("emergency pre-emption = %+v, want grant displacing field4", d)
	}
	if g, _ := m.Grant(); g.Level != floor.PriorityEmergency || !g.Emergency {
		t.Fatalf("emergency holder = %+v, want level 9", g)
	}

	// Equal levels never pre-empt — they queue.
	if d := m.Request("e2", floor.PriorityEmergency, false); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("equal-level request = %+v, want queued", d)
	}
}

// TestMaxDurationExpiryGrantsQueueHead: a burst outliving its bound
// auto-releases server-side and the queue head is granted in the same
// transition; the timer re-arms for the new holder.
func TestMaxDurationExpiryGrantsQueueHead(t *testing.T) {
	expired := make(chan string, 2)
	decisions := make(chan []floor.FloorDecision, 2)
	tokens := 0
	m := newFloorMachine(MachineConfig{
		MaxTalkDuration: 40 * time.Millisecond,
		TokenGen: func() string {
			tokens++
			return fmt.Sprintf("tok-%d", tokens)
		},
		OnExpired: func(holder string, ds []floor.FloorDecision) {
			expired <- holder
			decisions <- ds
		},
	})

	if d := m.Request("u1", 4, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("holder grant = %+v", d)
	}
	if d := m.Request("u2", 4, false); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("queued request = %+v", d)
	}

	select {
	case h := <-expired:
		if h != "u1" {
			t.Fatalf("expired holder = %q, want u1", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("max-duration expiry did not fire within 2s")
	}
	ds := <-decisions
	if len(ds) != 1 || ds[0].UserID != "u2" || ds[0].Outcome != floor.OutcomeGranted || ds[0].Token == "" {
		t.Fatalf("expiry decisions = %+v, want queue-head grant for u2", ds)
	}
	if g, _ := m.Grant(); g.UserID != "u2" || g.Token != ds[0].Token {
		t.Fatalf("holder after expiry = %+v", g)
	}

	// The timer re-armed for u2; with an empty queue the floor goes idle.
	select {
	case h := <-expired:
		if h != "u2" {
			t.Fatalf("second expired holder = %q, want u2", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-armed expiry did not fire within 2s")
	}
	if ds := <-decisions; len(ds) != 0 {
		t.Fatalf("second expiry decisions = %v, want none (empty queue)", ds)
	}
	if m.State() != stateIdle {
		t.Fatalf("state after final expiry = %q, want idle", m.State())
	}
}

// TestEmergencyFloorExemptFromExpiry: emergency floors have no burst bound —
// the exemption is controller-enforced, never client-enforced.
func TestEmergencyFloorExemptFromExpiry(t *testing.T) {
	fired := make(chan string, 1)
	m := newFloorMachine(MachineConfig{
		MaxTalkDuration: 40 * time.Millisecond,
		OnExpired:       func(holder string, _ []floor.FloorDecision) { fired <- holder },
	})

	if d := m.Request("e1", 5, true); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("emergency grant = %+v", d)
	}
	time.Sleep(150 * time.Millisecond)
	if m.State() != stateGranted {
		t.Fatalf("emergency floor state = %q, want granted (exempt)", m.State())
	}
	select {
	case h := <-fired:
		t.Fatalf("emergency floor expired for %s — must be exempt", h)
	default:
	}
}

// TestRevokeAuthority: only the current holder can be revoked, only by a
// level that outranks theirs, and a revoke never re-queues the stripped
// talker.
func TestRevokeAuthority(t *testing.T) {
	m := newTestMachine(0, 0)
	if d := m.Request("u1", 4, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("holder grant = %+v", d)
	}

	if _, err := m.Revoke("u2", "disp", floor.PriorityDispatcher); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("revoke of non-holder: %v, want ErrNotHolder", err)
	}
	if _, err := m.Revoke("u1", "peer", 4); !errors.Is(err, ErrInsufficientAuthority) {
		t.Fatalf("equal-level revoke: %v, want ErrInsufficientAuthority", err)
	}
	if _, err := m.Revoke("u1", "ambient", 2); !errors.Is(err, ErrInsufficientAuthority) {
		t.Fatalf("lower-level revoke: %v, want ErrInsufficientAuthority", err)
	}
	if m.State() != stateGranted {
		t.Fatalf("state after failed revokes = %q, want granted", m.State())
	}

	// Authorized revoke strips the floor and grants the queue head if any.
	// u2 requests at the holder's own level (4): equal levels queue, they
	// never pre-empt — level 5 would pre-empt 4 outright.
	if d := m.Request("u2", 4, false); d.Outcome != floor.OutcomeQueued {
		t.Fatalf("queued request = %+v", d)
	}
	ds, err := m.Revoke("u1", "disp", floor.PriorityDispatcher)
	if err != nil {
		t.Fatalf("authorized revoke: %v", err)
	}
	if len(ds) != 2 || ds[0].UserID != "u1" || ds[0].Reason != "revoked" || ds[1].UserID != "u2" || ds[1].Outcome != floor.OutcomeGranted {
		t.Fatalf("revoke decisions = %+v, want [revoked u1, granted u2]", ds)
	}
	if got := queueIDs(m); len(got) != 0 {
		t.Fatalf("queue after revoke = %v, want empty (talker not re-queued)", got)
	}
}

// TestStopTearsDown: Stop kills the burst timer — a machine stopped with its
// call never fires expiry callbacks afterwards.
func TestStopTearsDown(t *testing.T) {
	fired := make(chan string, 1)
	m := newFloorMachine(MachineConfig{
		MaxTalkDuration: 40 * time.Millisecond,
		OnExpired:       func(holder string, _ []floor.FloorDecision) { fired <- holder },
	})
	if d := m.Request("u1", 4, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("grant = %+v", d)
	}
	m.Stop()
	time.Sleep(120 * time.Millisecond)
	select {
	case h := <-fired:
		t.Fatalf("expiry fired after Stop for %s", h)
	default:
	}
	if m.State() != stateIdle {
		t.Fatalf("state after Stop = %q, want idle", m.State())
	}
}

// queueIDs is a test helper: the queue as a plain ID slice.
func queueIDs(m *floorMachine) []string {
	q := m.Queue()
	ids := make([]string, 0, len(q))
	for _, e := range q {
		ids = append(ids, e.UserID)
	}
	return ids
}
