// gate.go — the media-plane mirror seam. The spec's boundary invariant:
// the floor controller is the single writer of talk permission, and the
// SFU's grant mirror must never disagree with the machine that arbitrates.
// Every floor transition therefore writes the gate inside the same
// critical section as the call state it mirrors — the machine writes under
// its own lock, the session layer under the manager lock — so a client can
// never hold relay permission the controller did not just grant.
package callcontrol

import (
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// MediaGate is the SFU's grant mirror the control plane writes.
// media.Gate implements it; the interface keeps callcontrol decoupled from
// the WebRTC stack. A nil MediaGate (tests without a media plane) disables
// mirroring.
//
// Write discipline (media.Gate's contract, made binding here): Apply only
// with decisions a FloorController call just returned, RevokeUser when a
// release, revoke, or removal leaves — or strips — a talker, Clear at call
// teardown.
type MediaGate interface {
	Apply(callID string, decisions []floor.FloorDecision)
	RevokeUser(callID, userID string)
	Clear(callID string)
}

// perCallGate is the machine-side seam for one call: the floor machine
// writes every transition into it while holding the machine lock.
type perCallGate interface {
	apply(decisions []floor.FloorDecision)
	revoke(userID string)
}

// boundGate pins the mirror writes to one call so the machine never needs
// to know its own call identity. A boundGate over a nil MediaGate is never
// constructed (see startLocked); the nil check lives in the machine.
type boundGate struct {
	gate MediaGate
	call string
}

func (b boundGate) apply(decisions []floor.FloorDecision) { b.gate.Apply(b.call, decisions) }
func (b boundGate) revoke(userID string)                  { b.gate.RevokeUser(b.call, userID) }

// writeGate applies decisions to the gate when mirroring is wired; the
// session layer calls it while holding the manager lock.
func (sm *SessionManager) writeGate(callID string, decisions []floor.FloorDecision) {
	if sm.media != nil {
		sm.media.Apply(callID, decisions)
	}
}

// revokeGate drops one user's mirror entry when mirroring is wired; the
// session layer calls it while holding the manager lock.
func (sm *SessionManager) revokeGate(callID, userID string) {
	if sm.media != nil {
		sm.media.RevokeUser(callID, userID)
	}
}

// clearGate forgets a call's mirror entries at teardown when mirroring is
// wired; the session layer calls it while holding the manager lock.
func (sm *SessionManager) clearGate(callID string) {
	if sm.media != nil {
		sm.media.Clear(callID)
	}
}
