package media

import (
	"sync"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// Gate is the media plane's in-process mirror of floor state.
//
// Single-writer discipline (spec boundary invariant): the control plane is
// the only writer, and it writes exclusively what its FloorController
// produced — Apply for the decision list a controller call returned,
// RevokeUser when a release, revoke, or removal leaves nobody talking,
// Clear at call teardown. The SFU only reads, once per received packet,
// and never grants anything by itself.
//
// The mirror is deliberately not shared state with the controller: the
// controller owns arbitration, the mirror answers "may the SFU relay this
// user right now", and the control plane keeps them consistent in the same
// critical section as its own call state.
type Gate struct {
	mu     sync.RWMutex
	grants map[string]map[string]grant // callID → userID → grant
}

// grant is one user's permission to be relayed on one call.
type grant struct {
	token     string // the floor token the controller issued with the grant
	emergency bool
}

func NewGate() *Gate {
	return &Gate{grants: make(map[string]map[string]grant)}
}

// Apply applies the decisions one FloorController call returned. A granted
// decision adds or replaces that user's grant; when it names
// PreemptedUserID, the displaced talker loses relay permission in the same
// step — together those two effects are how pre-emption reaches the media
// plane. Queued and denied decisions change nothing.
//
// Arbitrated calls hold exactly one grant because the controller emits
// exactly that shape (a grant, or a grant that names the talker it
// displaced); direct private calls — the floor-control-free private mode —
// legitimately hold one grant per speaker.
func (g *Gate) Apply(callID string, decisions []floor.FloorDecision) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range decisions {
		if d.Outcome != floor.OutcomeGranted {
			continue
		}
		call := g.grants[callID]
		if call == nil {
			call = make(map[string]grant)
			g.grants[callID] = call
		}
		if d.PreemptedUserID != "" {
			delete(call, d.PreemptedUserID)
		}
		call[d.UserID] = grant{token: d.Token, emergency: d.Emergency}
	}
}

// RevokeUser drops any grant userID held on callID — a PTT release with an
// empty queue, a dispatcher revoke with no successor, a removal.
func (g *Gate) RevokeUser(callID, userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if call := g.grants[callID]; call != nil {
		delete(call, userID)
		if len(call) == 0 {
			delete(g.grants, callID)
		}
	}
}

// Clear forgets every grant for a call (teardown).
func (g *Gate) Clear(callID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.grants, callID)
}

// Granted reports whether userID may be relayed on callID right now.
// Hot path: called once per received RTP packet.
func (g *Gate) Granted(callID, userID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.grants[callID][userID]
	return ok
}
