package callcontrol

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"sync/atomic"
)

// stressTokenSeq makes tokens globally unique across the many machines the
// stress tests create (newTestMachine's per-machine counter would collide).
var stressTokenSeq atomic.Int64

// newStressMachine builds a floor machine with default talk duration and a
// globally unique deterministic token sequence.
func newStressMachine(maxQueue int) *floorMachine {
	return newFloorMachine(MachineConfig{
		MaxQueueDepth: maxQueue,
		TokenGen: func() string {
			return fmt.Sprintf("tok-%d", stressTokenSeq.Add(1))
		},
	})
}

// TestStressFiftyRequestersExactlyOneGrant: the acceptance stress test —
// 50 concurrent requesters per call resolve to exactly one grant. All at the
// same priority, so no pre-emption is possible: the first through the lock
// wins, everyone else queues or is denied, positions must be unique, tokens
// must be globally unique, and calls must not interfere.
func TestStressFiftyRequestersExactlyOneGrant(t *testing.T) {
	const calls = 8
	const requesters = 50

	allTokens := map[string]bool{}
	for call := 0; call < calls; call++ {
		m := newStressMachine(0)

		decisions := make([]floor.FloorDecision, requesters)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < requesters; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // all 50 fire in the same instant
				decisions[i] = m.Request(fmt.Sprintf("user-%d", i), floor.PriorityNormal, false)
			}(i)
		}
		close(start)
		wg.Wait()

		grants, queues, denies := 0, 0, 0
		var grantedUser string
		seenPositions := map[int]string{}
		for _, d := range decisions {
			switch d.Outcome {
			case floor.OutcomeGranted:
				grants++
				grantedUser = d.UserID
				if allTokens[d.Token] {
					t.Errorf("call %d: duplicate floor token %s", call, d.Token)
				}
				allTokens[d.Token] = true
			case floor.OutcomeQueued:
				queues++
				if prev, dup := seenPositions[d.QueuePosition]; dup {
					t.Errorf("call %d: queue position %d held by both %s and %s", call, d.QueuePosition, prev, d.UserID)
				}
				seenPositions[d.QueuePosition] = d.UserID
			case floor.OutcomeDenied:
				denies++
			}
		}
		if grants != 1 {
			t.Fatalf("call %d: %d grants under 50 concurrent requesters, want exactly 1", call, grants)
		}
		if queues+denies != requesters-1 {
			t.Fatalf("call %d: %d queued + %d denied ≠ %d non-granted requesters", call, queues, denies, requesters-1)
		}
		if queues > 16 {
			t.Fatalf("call %d: %d queued entries exceed the 16-deep queue", call, queues)
		}
		if g, ok := m.Grant(); !ok || g.UserID != grantedUser {
			t.Fatalf("call %d: holder = %+v, want the single granted user %s", call, g, grantedUser)
		}
	}
}

// TestPreemptionWithinOneSchedulingTurn: a P10 request mid-burst pre-empts
// synchronously — by the time Request returns, the state transition is
// complete: the P10 user holds a fresh token and the displaced talker is
// re-queued. No callback, no later turn.
func TestPreemptionWithinOneSchedulingTurn(t *testing.T) {
	m := newStressMachine(0)

	if d := m.Request("field", 4, false); d.Outcome != floor.OutcomeGranted {
		t.Fatalf("initial grant = %+v", d)
	}
	g, _ := m.Grant()
	oldToken := g.Token

	done := make(chan floor.FloorDecision, 1)
	go func() {
		done <- m.Request("net", floor.PriorityDispatcher, false)
	}()

	select {
	case d := <-done:
		if d.Outcome != floor.OutcomeGranted || d.PreemptedUserID != "field" {
			t.Fatalf("pre-empting decision = %+v", d)
		}
		// Still inside the request's scheduling turn: the grant is already
		// visible, the displaced talker is already queued, and the old token
		// is dead.
		if g, ok := m.Grant(); !ok || g.UserID != "net" || g.Token != d.Token {
			t.Fatalf("holder not rotated within the request turn: %+v ok=%v", g, ok)
		}
		if g, _ := m.Grant(); g.Token == oldToken {
			t.Fatal("old floor token still live after pre-emption")
		}
		if got := queueIDs(m); len(got) != 1 || got[0] != "field" {
			t.Fatalf("displaced talker not re-queued: %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pre-empting request did not return within one scheduling turn")
	}
}

// TestStressMixedPriorityPreemption: 50 concurrent requesters spread across
// the whole ladder. Strictly-higher arrivals legitimately pre-empt, so the
// decision log holds a grant chain (grants ≥ 1) — the invariants that must
// hold are: the final holder is one of the five P10 requesters, no user is
// granted twice, the queue lists each user at most once, and every
// non-granted requester is queued or denied.
func TestStressMixedPriorityPreemption(t *testing.T) {
	const requesters = 50
	m := newStressMachine(0)

	ladder := func(i int) floor.FloorLevel {
		return floor.FloorLevel(1 + i%10) // every level 1..10 appears
	}

	decisions := make([]floor.FloorDecision, requesters)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < requesters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			decisions[i] = m.Request(fmt.Sprintf("user-%d", i), ladder(i), false)
		}(i)
	}
	close(start)
	wg.Wait()

	grants := 0
	grantedUsers := map[string]bool{}
	for _, d := range decisions {
		if d.Outcome != floor.OutcomeGranted {
			continue
		}
		grants++
		if grantedUsers[d.UserID] {
			t.Fatalf("user %s granted more than once in the contention window", d.UserID)
		}
		grantedUsers[d.UserID] = true
	}
	if grants < 1 {
		t.Fatal("no grants under mixed-priority contention")
	}
	g, ok := m.Grant()
	if !ok || g.Level != floor.PriorityDispatcher {
		t.Fatalf("holder = %+v ok=%v, want a P10 requester", g, ok)
	}
	if !grantedUsers[g.UserID] {
		t.Fatalf("final holder %s never received a grant decision", g.UserID)
	}
	// Conservation: every requester ends up exactly once — holding, queued,
	// or denied. (A granted user who was pre-empted was re-queued by the
	// machine; a denied user was never enqueued.)
	holderID := g.UserID
	inQueue := map[string]bool{}
	for _, id := range queueIDs(m) {
		if inQueue[id] {
			t.Fatalf("user %s queued more than once: %v", id, queueIDs(m))
		}
		if id == holderID {
			t.Fatalf("holder %s also sits in the queue: %v", id, queueIDs(m))
		}
		inQueue[id] = true
	}
	for i, d := range decisions {
		id := fmt.Sprintf("user-%d", i)
		if id == holderID || inQueue[id] {
			continue
		}
		if d.Outcome != floor.OutcomeDenied {
			t.Fatalf("user %s unaccounted: decision %+v, holder %s, queue %v", id, d, holderID, queueIDs(m))
		}
	}
}

// TestStressRequestReleaseRelay: 50 goroutines churn request→hold→release
// bursts at equal priority with a queue deep enough for everyone (64 > 49,
// so no busy denials). Grants relay through the queue head; a holder keeps
// the floor until they release; when everyone stops, the floor is idle and
// the queue empty.
func TestStressRequestReleaseRelay(t *testing.T) {
	const talkers = 50
	const bursts = 20
	m := newStressMachine(64)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < talkers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("user-%d", i)
			<-start
			for b := 0; b < bursts; b++ {
				d := m.Request(id, 4, false)
				switch d.Outcome {
				case floor.OutcomeGranted:
					// hold until our own release
				case floor.OutcomeQueued:
					if !waitForHolder(m, id, 5*time.Second) {
						t.Errorf("%s queued but never granted (burst %d)", id, b)
						return
					}
				default:
					t.Errorf("%s unexpected outcome %s (burst %d)", id, d.Outcome, b)
					return
				}
				if g, ok := m.Grant(); !ok || g.UserID != id {
					t.Errorf("%s lost the floor before releasing: %+v ok=%v", id, g, ok)
					return
				}
				m.Release(id)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if m.State() != stateIdle {
		t.Fatalf("final state = %q, want idle after all bursts released", m.State())
	}
	if _, ok := m.Grant(); ok {
		t.Fatal("grant survives after every talker released")
	}
	if got := queueIDs(m); len(got) != 0 {
		t.Fatalf("queue residue = %v, want empty", got)
	}
}

// waitForHolder polls until userID holds the floor or the deadline passes.
func waitForHolder(m *floorMachine, userID string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if g, ok := m.Grant(); ok && g.UserID == userID {
			return true
		}
		time.Sleep(200 * time.Microsecond)
	}
	return false
}
