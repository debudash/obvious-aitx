// Package callcontrol implements the server-authoritative floor controller
// and call sessions: group, private (with and without floor control), and
// broadcast calls; request/grant/deny/queue/release/revoke arbitration with
// priority pre-emption; and max-duration enforcement.
//
// Boundary invariant (spec): the floor controller is the single writer of
// talk permission. The tokens minted here are the only basis on which the
// media plane may forward a talk burst, and every transition happens
// server-side — clients request, they never decide.
package callcontrol

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// Compile-time proof that the per-call machine implements the foundation
// floor contract.
var _ floor.FloorController = (*floorMachine)(nil)

// DefaultMaxQueueDepth bounds one call's waiting list; a request arriving at
// capacity is denied "busy" (spillover) rather than queued. The spec fixes no
// depth — this bound keeps memory finite and the dispatcher's queue display
// readable. Per-call configurable through MachineConfig.
const DefaultMaxQueueDepth = 16

// Errors returned by Revoke.
var (
	// ErrNotHolder means the target user does not currently hold the floor.
	ErrNotHolder = errors.New("callcontrol: target does not hold the floor")
	// ErrInsufficientAuthority means the revoking level does not outrank the
	// holder's level — only higher authority strips a live talk burst.
	ErrInsufficientAuthority = errors.New("callcontrol: revoking level does not outrank the holder")
)

// floorState names the two server-side call states. Queued, denied, and
// pre-empted are requester-side rows of the spec's floor-state table — what a
// particular user sees — while idle and granted describe the call itself.
type floorState string

const (
	stateIdle    floorState = "idle"
	stateGranted floorState = "granted"
)

// queueEntry is one waiting talk-burst request.
type queueEntry struct {
	userID     string
	level      floor.FloorLevel
	emergency  bool
	enqueuedAt time.Time
}

// grant is the active talk-burst.
type grant struct {
	userID    string
	level     floor.FloorLevel
	emergency bool
	token     string
	since     time.Time
}

// GrantView is a read-only copy of the active grant for wiring and snapshots.
type GrantView struct {
	UserID    string
	Level     floor.FloorLevel
	Emergency bool
	Token     string
	Since     time.Time
}

// MachineConfig carries the tunables for one call's floor machine; zero
// values fall back to the defaults (60 s bursts, 16-deep queue).
type MachineConfig struct {
	// MaxTalkDuration bounds every non-emergency burst (per-group
	// configurable; spec default 60 s). Emergency floors are exempt.
	MaxTalkDuration time.Duration
	MaxQueueDepth   int
	// Now and TokenGen are injection points for tests.
	Now      func() time.Time
	TokenGen func() string
	// Gate is the media-plane mirror (perCallGate): every transition —
	// grant, release, revoke, pre-emption, and expiry — writes it while the
	// machine lock is held, so "who holds the floor" and "who may be
	// relayed" cannot disagree (spec boundary invariant). Nil in tests
	// without a media plane.
	Gate perCallGate
	// OnExpired fires after a max-duration expiry auto-released the floor,
	// with the expired holder and the queue-head grant (if anyone waited).
	// It runs outside the machine lock.
	OnExpired func(expiredHolder string, decisions []floor.FloorDecision)
}

// floorMachine is the authoritative floor arbiter for exactly one call.
// Every public method takes mu; arbitration is in-memory only, so a request
// — including a pre-emption — resolves within one scheduling turn.
type floorMachine struct {
	mu sync.Mutex

	state  floorState
	active grant
	queue  []queueEntry

	maxTalk  time.Duration
	maxQueue int

	now       func() time.Time
	tokenGen  func() string
	gate      perCallGate
	onExpired func(expiredHolder string, decisions []floor.FloorDecision)
	timer     *time.Timer // non-nil while a non-emergency holder is timed
}

func newFloorMachine(cfg MachineConfig) *floorMachine {
	m := &floorMachine{
		state:     stateIdle, // explicit: a Go zero-value string would break every state check
		maxTalk:   cfg.MaxTalkDuration,
		maxQueue:  cfg.MaxQueueDepth,
		now:       cfg.Now,
		tokenGen:  cfg.TokenGen,
		gate:      cfg.Gate,
		onExpired: cfg.OnExpired,
	}
	if m.maxTalk <= 0 {
		m.maxTalk = floor.DefaultMaxTalkDuration
	}
	if m.maxQueue <= 0 {
		m.maxQueue = DefaultMaxQueueDepth
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.tokenGen == nil {
		m.tokenGen = newToken
	}
	return m
}

// Request arbitrates one talk-burst request (floor.FloorController).
//
// The floor is idle → grant. The requester strictly outranks the holder →
// immediate pre-emption; the displaced talker is re-enqueued (spec's
// pre-empted row: "original speaker joins queue if still holding PTT" — a
// client that lets go of PTT cancels the entry with Release). Otherwise the
// request queues in priority order (higher level first, FIFO within a level)
// — or is denied "busy" when the queue is at capacity. Emergency requests
// arbitrate at level 9 and, when granted, are exempt from the max-duration
// timer. A re-press from the holder or an already-queued user is idempotent.
func (m *floorMachine) Request(userID string, lvl floor.FloorLevel, emergency bool) floor.FloorDecision {
	if !lvl.Valid() {
		return deny(userID, lvl, emergency, "invalid-priority", 0)
	}
	if emergency && lvl < floor.PriorityEmergency {
		lvl = floor.PriorityEmergency
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Idempotent re-press from the current holder: restate the grant.
	if m.state == stateGranted && m.active.userID == userID {
		return floor.FloorDecision{
			UserID: userID, Outcome: floor.OutcomeGranted,
			Level: m.active.level, Emergency: m.active.emergency, Token: m.active.token,
		}
	}
	// Already queued: restate the position — unless the new request
	// escalates the stored entry (an emergency press must never be swallowed
	// by the queue); escalated entries drop out and re-contend for the floor.
	if pos := m.queuePositionLocked(userID); pos > 0 {
		e := m.queue[pos-1]
		if !escalates(e, lvl, emergency) {
			return floor.FloorDecision{
				UserID: userID, Outcome: floor.OutcomeQueued,
				Level: e.level, Emergency: e.emergency, QueuePosition: pos,
			}
		}
		m.dropQueuedLocked(userID)
	}

	if m.state == stateIdle {
		return m.grantLocked(userID, lvl, emergency, "")
	}

	if floor.Preempts(lvl, m.active.level) {
		displaced := m.active
		m.stripGrantLocked()
		m.enqueueLocked(queueEntry{
			userID: displaced.userID, level: displaced.level,
			emergency: displaced.emergency, enqueuedAt: m.now(),
		})
		return m.grantLocked(userID, lvl, emergency, displaced.userID)
	}

	if len(m.queue) >= m.maxQueue {
		return deny(userID, lvl, emergency, "busy", len(m.queue)+1)
	}
	m.enqueueLocked(queueEntry{userID: userID, level: lvl, emergency: emergency, enqueuedAt: m.now()})
	return floor.FloorDecision{
		UserID: userID, Outcome: floor.OutcomeQueued,
		Level: lvl, Emergency: emergency, QueuePosition: m.queuePositionLocked(userID),
	}
}

// Release ends userID's involvement in the floor (floor.FloorController): as
// holder it frees the floor and auto-grants the queue head synchronously; as
// a queued requester it cancels the pending entry. The returned decisions are
// the queue-head grants the wiring must publish; the release/cancel echo
// itself is broadcast by the caller.
func (m *floorMachine) Release(userID string) []floor.FloorDecision {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == stateGranted && m.active.userID == userID {
		m.stripGrantLocked()
		return m.autoGrantHeadLocked()
	}
	m.dropQueuedLocked(userID)
	return nil
}

// Revoke strips the floor from the target talker (floor.FloorController).
// Unlike pre-emption, a revoke is a deliberate removal: the stripped talker
// is not re-queued. The queue head, if any, is granted so the call keeps
// flowing. by and lvl identify the revoking authority; lvl must outrank the
// holder's level.
func (m *floorMachine) Revoke(target string, by string, lvl floor.FloorLevel) ([]floor.FloorDecision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != stateGranted || m.active.userID != target {
		return nil, ErrNotHolder
	}
	if !floor.Preempts(lvl, m.active.level) {
		return nil, ErrInsufficientAuthority
	}
	displaced := m.active
	m.stripGrantLocked()
	decisions := []floor.FloorDecision{{
		UserID: displaced.userID, Outcome: floor.OutcomeDenied,
		Level: displaced.level, Reason: "revoked",
	}}
	return append(decisions, m.autoGrantHeadLocked()...), nil
}

// Queue returns the ordered waiting list for status displays
// (floor.FloorController). Higher levels first, FIFO within a level.
func (m *floorMachine) Queue() []floor.QueuedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]floor.QueuedRequest, 0, len(m.queue))
	for _, e := range m.queue {
		out = append(out, floor.QueuedRequest{
			UserID: e.userID, Level: e.level, Emergency: e.emergency, EnqueuedAt: e.enqueuedAt,
		})
	}
	return out
}

// QueueUserIDs lists the queued user IDs in queue order (wire format for
// FloorGranted.Queue).
func (m *floorMachine) QueueUserIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.queue))
	for _, e := range m.queue {
		out = append(out, e.userID)
	}
	return out
}

// Grant copies the active grant, if any. The media plane matches the holder's
// media against Token — a rotated or cleared token stops forwarding.
func (m *floorMachine) Grant() (GrantView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != stateGranted {
		return GrantView{}, false
	}
	return GrantView{
		UserID: m.active.userID, Level: m.active.level, Emergency: m.active.emergency,
		Token: m.active.token, Since: m.active.since,
	}, true
}

// State reports the call-level floor state ("idle" or "granted").
func (m *floorMachine) State() floorState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Stop tears the machine down with its call: the burst timer dies and the
// state clears. Late decisions from torn-down sessions are rejected by the
// registry's ended flag.
func (m *floorMachine) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stripGrantLocked()
	m.queue = nil
}

// grantLocked installs a grant and arms the max-duration timer for
// non-emergency bursts. Callers hold mu. The mirror write happens here, in
// the machine's critical section: a pre-empting grant carries
// PreemptedUserID, so Apply drops the displaced talker's relay permission
// and admits the new one in the same step.
func (m *floorMachine) grantLocked(userID string, lvl floor.FloorLevel, emergency bool, preempted string) floor.FloorDecision {
	m.state = stateGranted
	m.active = grant{userID: userID, level: lvl, emergency: emergency, token: m.tokenGen(), since: m.now()}
	if !emergency {
		m.timer = time.AfterFunc(m.maxTalk, m.onExpireTimer)
	}
	d := floor.FloorDecision{
		UserID: userID, Outcome: floor.OutcomeGranted, Level: lvl,
		Emergency: emergency, Token: m.active.token, PreemptedUserID: preempted,
	}
	if m.gate != nil {
		m.gate.apply([]floor.FloorDecision{d})
	}
	return d
}

// stripGrantLocked ends the active burst: the timer stops, the token
// clears, and the media mirror drops the holder — a release, revoke,
// expiry, or pre-emption always cuts relay permission in the same critical
// section that ends the grant, so media keyed to the old token stops
// forwarding immediately.
func (m *floorMachine) stripGrantLocked() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if m.state == stateGranted && m.gate != nil {
		m.gate.revoke(m.active.userID)
	}
	m.active = grant{}
	m.state = stateIdle
}

// autoGrantHeadLocked pops the queue head and grants it; empty queue → idle.
func (m *floorMachine) autoGrantHeadLocked() []floor.FloorDecision {
	if len(m.queue) == 0 {
		return nil
	}
	head := m.queue[0]
	m.queue = m.queue[1:]
	d := m.grantLocked(head.userID, head.level, head.emergency, "")
	return []floor.FloorDecision{d}
}

// enqueueLocked inserts by priority: higher levels first, FIFO within a
// level (insert before the first strictly-lower entry).
func (m *floorMachine) enqueueLocked(e queueEntry) {
	i := 0
	for i < len(m.queue) && m.queue[i].level >= e.level {
		i++
	}
	m.queue = append(m.queue, queueEntry{})
	copy(m.queue[i+1:], m.queue[i:])
	m.queue[i] = e
}

// escalates reports whether a re-request outranks the user's queued entry —
// a strictly higher level, or an emergency flag the stored entry lacks.
func escalates(e queueEntry, lvl floor.FloorLevel, emergency bool) bool {
	return lvl > e.level || (emergency && !e.emergency)
}

func (m *floorMachine) dropQueuedLocked(userID string) bool {
	for i, e := range m.queue {
		if e.userID == userID {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return true
		}
	}
	return false
}

func (m *floorMachine) queuePositionLocked(userID string) int {
	for i, e := range m.queue {
		if e.userID == userID {
			return i + 1
		}
	}
	return 0
}

// onExpireTimer runs on the timer goroutine when a burst outlives its bound:
// the floor auto-releases and the queue head, if any, is granted. The
// registry's callback publishes the transitions after the lock is dropped.
func (m *floorMachine) onExpireTimer() {
	m.mu.Lock()
	// Emergency floors are exempt; a stale timer (released mid-flight) is a
	// no-op because the state already moved on.
	if m.state != stateGranted || m.active.emergency {
		m.mu.Unlock()
		return
	}
	expired := m.active.userID
	m.stripGrantLocked()
	decisions := m.autoGrantHeadLocked()
	m.mu.Unlock()

	if m.onExpired != nil {
		m.onExpired(expired, decisions)
	}
}

func deny(userID string, lvl floor.FloorLevel, emergency bool, reason string, queuePosition int) floor.FloorDecision {
	return floor.FloorDecision{
		UserID: userID, Outcome: floor.OutcomeDenied, Level: lvl,
		Emergency: emergency, Reason: reason, QueuePosition: queuePosition,
	}
}

// newToken mints the per-grant floor token the SFU accepts media for. It
// panics on entropy failure: a floor controller that cannot mint tokens must
// fail loudly, never issue predictable ones.
func newToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic("callcontrol: token entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
