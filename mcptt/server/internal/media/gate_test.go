package media

import (
	"testing"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

func grantFor(userID string) []floor.FloorDecision {
	return []floor.FloorDecision{{
		UserID:  userID,
		Outcome: floor.OutcomeGranted,
		Level:   floor.PriorityNormal,
		Token:   "floor-tok",
	}}
}

func TestGateGrantEnablesRelay(t *testing.T) {
	g := NewGate()
	if g.Granted("c1", "alice") {
		t.Fatal("nobody is granted before any decision")
	}
	g.Apply("c1", grantFor("alice"))
	if !g.Granted("c1", "alice") {
		t.Fatal("granted user not relayed")
	}
	if g.Granted("c2", "alice") {
		t.Fatal("grant leaked to another call")
	}
	if g.Granted("c1", "bob") {
		t.Fatal("non-holder granted")
	}
}

// TestGatePreemptionDropsDisplacedTalker mirrors how pre-emption reaches
// the media plane: the controller's grant decision names PreemptedUserID,
// and the displaced talker must lose relay permission in the same step.
func TestGatePreemptionDropsDisplacedTalker(t *testing.T) {
	g := NewGate()
	g.Apply("c1", grantFor("alice"))

	g.Apply("c1", []floor.FloorDecision{{
		UserID:          "tango",
		Outcome:         floor.OutcomeGranted,
		Level:           floor.PriorityEmergency,
		Emergency:       true,
		PreemptedUserID: "alice",
	}})
	if g.Granted("c1", "alice") {
		t.Fatal("pre-empted talker still relayed")
	}
	if !g.Granted("c1", "tango") {
		t.Fatal("pre-empting talker not granted")
	}
}

// TestGateDirectModeHoldsTwoGrants — direct private calls run without
// floor control; both speakers legitimately hold grants at once.
func TestGateDirectModeHoldsTwoGrants(t *testing.T) {
	g := NewGate()
	g.Apply("c1", grantFor("alice"))
	g.Apply("c1", grantFor("bob"))
	if !g.Granted("c1", "alice") || !g.Granted("c1", "bob") {
		t.Fatal("direct mode lost one of the two grants")
	}
}

func TestGateRevokeUserAndClear(t *testing.T) {
	g := NewGate()
	g.Apply("c1", grantFor("alice"))

	g.RevokeUser("c1", "alice")
	if g.Granted("c1", "alice") {
		t.Fatal("revoked user still relayed")
	}

	g.Apply("c1", grantFor("alice"))
	g.Clear("c1")
	if g.Granted("c1", "alice") {
		t.Fatal("cleared call still relays")
	}
	// All operations on unknown calls are safe no-ops.
	g.RevokeUser("ghost", "alice")
	g.Clear("ghost")
	if g.Granted("ghost", "alice") {
		t.Fatal("ghost call granted")
	}
}

// TestGateConcurrentAccess exercises the hot path under the race detector.
func TestGateConcurrentAccess(t *testing.T) {
	g := NewGate()
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 1000; j++ {
				g.Apply("c", grantFor("u"))
				g.Granted("c", "u")
				g.RevokeUser("c", "u")
				g.Clear("c")
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
