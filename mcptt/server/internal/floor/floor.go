// Package floor defines the floor-control contract fixed by the foundation PR:
// the priority enum, the FloorController interface, and the decision types the
// floor-control engine PR will implement and the media plane will consume.
//
// Boundary invariant (spec): the floor controller is the single writer of talk
// permission. No client, dispatcher, or SFU path may transmit a talk burst
// into a call without a floor grant from a FloorController implementation.
package floor

import "time"

// FloorLevel is a talk-burst priority from 1 (ambient) to 10 (net control).
type FloorLevel int

// Level bands per the spec: ambient 1–3, standard users 4–6, supervisors 7–8,
// emergency 9, dispatcher 10. Each named constant anchors the lowest level of
// its band; Band maps any level to its band. (The spec's code sample sketched
// these as a raw iota run; the band comments define the intent, and the locked
// decision pins dispatcher=10 and emergency=9, so the constants below carry
// their band floors explicitly.)
const (
	PriorityAmbient    FloorLevel = 1  // 1–3: routine chat tiers
	PriorityNormal     FloorLevel = 4  // 4–6: standard users
	PrioritySupervisor FloorLevel = 7  // 7–8: supervisors, can pre-empt normal
	PriorityEmergency  FloorLevel = 9  // 9: emergency calls, imminent peril
	PriorityDispatcher FloorLevel = 10 // net control, pre-empts everything

	MinLevel FloorLevel = 1
	MaxLevel FloorLevel = 10
)

// Valid reports whether the level is inside the 1–10 ladder.
func (l FloorLevel) Valid() bool { return l >= MinLevel && l <= MaxLevel }

// Band is the named priority tier a level belongs to.
type Band string

const (
	BandAmbient    Band = "ambient"
	BandNormal     Band = "normal"
	BandSupervisor Band = "supervisor"
	BandEmergency  Band = "emergency"
	BandDispatcher Band = "dispatcher"
)

// BandOf maps a valid level to its tier; invalid levels return "".
func BandOf(l FloorLevel) Band {
	switch {
	case !l.Valid():
		return ""
	case l < PriorityNormal:
		return BandAmbient
	case l < PrioritySupervisor:
		return BandNormal
	case l < PriorityEmergency:
		return BandSupervisor
	case l < PriorityDispatcher:
		return BandEmergency
	default:
		return BandDispatcher
	}
}

// Preempts reports whether a level a outranks an active level b. Strictly
// higher levels pre-empt; equal levels queue.
func Preempts(a, b FloorLevel) bool { return a > b }

// DefaultMaxTalkDuration bounds every burst; emergency floors are exempt.
// Enforced by the controller — never by clients, which cannot be trusted to
// hang up on themselves.
const DefaultMaxTalkDuration = 60 * time.Second

// DecisionOutcome classifies what Request did with a talk-burst request.
type DecisionOutcome string

const (
	OutcomeGranted DecisionOutcome = "granted"
	OutcomeQueued  DecisionOutcome = "queued"
	OutcomeDenied  DecisionOutcome = "denied"
)

// FloorDecision is the outcome of a floor request, release, or revoke.
type FloorDecision struct {
	UserID        string          `json:"userId"`
	Outcome       DecisionOutcome `json:"outcome"`
	Level         FloorLevel      `json:"priority"`
	Emergency     bool            `json:"emergency,omitempty"`
	Token         string          `json:"token,omitempty"` // grants only: the token the SFU accepts media for
	QueuePosition int             `json:"queuePosition,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	// PreemptedUserID is set when this decision displaced an active talker.
	PreemptedUserID string `json:"preemptedUserId,omitempty"`
}

// QueuedRequest is one ordered waiting-list entry, for status displays.
type QueuedRequest struct {
	UserID     string     `json:"userId"`
	Level      FloorLevel `json:"priority"`
	Emergency  bool       `json:"emergency,omitempty"`
	EnqueuedAt time.Time  `json:"enqueuedAt"`
}

// FloorController is the single authoritative arbiter for one call.
// It is safe for concurrent use; each call owns exactly one controller.
type FloorController interface {
	// Request asks to speak. It either grants immediately (returns a grant
	// with the floor token the SFU accepts media for) or enqueues the
	// request; it never blocks the caller.
	Request(userID string, lvl FloorLevel, emergency bool) FloorDecision

	// Release returns the floor to the queue; the head of the queue is
	// granted synchronously during this call.
	Release(userID string) []FloorDecision

	// Revoke strips the floor from a talker (dispatcher / pre-emption).
	Revoke(target string, by string, lvl FloorLevel) ([]FloorDecision, error)

	// Queue returns the ordered waiting list for status displays.
	Queue() []QueuedRequest
}
