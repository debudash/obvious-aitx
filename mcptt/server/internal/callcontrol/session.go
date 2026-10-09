// Package callcontrol owns call sessions and their floor control. The floor
// machine (floormachine.go) is the single arbiter of talk permission; the
// session layer admits participants to calls and routes their floor traffic
// through the machine. Spec basis: Release 18 group / private / broadcast
// call shapes (TS 23.379) with the priority ladder pinned in the system spec.
package callcontrol

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
)

// CallKind distinguishes the three session shapes the spec defines.
type CallKind string

const (
	KindGroup     CallKind = "group"     // pre-arranged group call, affiliated members
	KindPrivate   CallKind = "private"   // one-to-one, with or without floor control
	KindBroadcast CallKind = "broadcast" // one-to-many, dispatcher speaks, others listen
)

// Session-level errors. Floor arbitration errors come from the machine
// (ErrNotHolder, ErrInsufficientAuthority).
var (
	ErrUnknownCall        = errors.New("unknown call")
	ErrAlreadyParticipant = errors.New("already a participant")
	ErrNotAffiliated      = errors.New("user not affiliated with group")
	ErrNotAParty          = errors.New("user is not a party of this private call")
	ErrNotInCall          = errors.New("user has not joined the call")
	ErrNoFloorControl     = errors.New("call runs without floor control")
)

// EventType tags session lifecycle events for the wiring layer (WSS
// broadcast, audit log, media gate).
type EventType string

const (
	EventCallStarted        EventType = "CallStarted"
	EventCallEnded          EventType = "CallEnded"
	EventParticipantJoined  EventType = "ParticipantJoined"
	EventParticipantLeft    EventType = "ParticipantLeft"
	EventParticipantRemoved EventType = "ParticipantRemoved"
	EventFloorDecisions     EventType = "FloorDecisions"
)

// Event is one session change the wiring must fan out. FloorDecisions events
// carry the machine's decisions (grant/deny/queue/revoke/expiry) for the
// affected call; Queue is the post-decision waiting list (the wire's
// FloorGranted.Queue — the foundation decision type stays lean, the wire
// format carries the array). GroupID is set on CallStarted for group and
// broadcast calls; Target is the affected user of a ParticipantRemoved
// event (Actor is the remover).
type Event struct {
	Type      EventType
	CallID    string
	GroupID   string // owning talkgroup; empty for private calls and unresolved calls
	Actor     string
	Target    string
	Kind      CallKind
	Decisions []floor.FloorDecision
	Queue     []string
}

// SessionConfig configures a SessionManager.
type SessionConfig struct {
	MaxTalkDuration time.Duration // per-burst bound, 0 → floor.DefaultMaxTalkDuration
	MaxQueueDepth   int           // per-call queue bound, 0 → 16
	// Affiliation reports whether userID may participate in groupID's
	// calls (backed by the store at wiring time; static in tests). A nil
	// checker denies every group join — secure by default.
	Affiliation func(userID, groupID string) bool
	// Media is the SFU's grant mirror (media.Gate in production). Every
	// floor transition writes it inside the same critical section as the
	// call state it mirrors — the machine under its own lock, the session
	// layer under the manager lock — so relay permission never disagrees
	// with arbitration (spec boundary invariant). Nil in tests without a
	// media plane.
	Media MediaGate
	// TokenGen mints unguessable identifiers: floor tokens and call IDs.
	TokenGen func() string
	// OnEvent receives every session event. Emitted outside the manager
	// lock so handlers may call back into the manager.
	OnEvent func(Event)
}

// SessionInfo is the client-visible state of one call: who is talking, who
// waits, who is present. Late joiners render the speaker immediately from
// this snapshot; the audio itself arrives from the SFU.
type SessionInfo struct {
	CallID        string
	Kind          CallKind
	GroupID       string
	FloorControl  bool
	Emergency     bool
	ImminentPeril bool
	Participants  []string
	Speaker       string
	SpeakerSince  time.Time // zero when idle
	Queue         []floor.QueuedRequest
}

// SessionManager owns every live call. Safe for concurrent use.
type SessionManager struct {
	mu       sync.Mutex
	calls    map[string]*session
	maxTalk  time.Duration
	maxQueue int
	affil    func(userID, groupID string) bool
	media    MediaGate
	tokenGen func() string
	onEvent  func(Event)
}

// NewSessionManager builds a manager. Zero config fields fall back to spec
// defaults.
func NewSessionManager(cfg SessionConfig) *SessionManager {
	sm := &SessionManager{
		calls:    map[string]*session{},
		maxTalk:  cfg.MaxTalkDuration,
		maxQueue: cfg.MaxQueueDepth,
		affil:    cfg.Affiliation,
		media:    cfg.Media,
		tokenGen: cfg.TokenGen,
		onEvent:  cfg.OnEvent,
	}
	if sm.maxQueue <= 0 {
		sm.maxQueue = 16
	}
	if sm.tokenGen == nil {
		sm.tokenGen = newToken
	}
	if sm.onEvent == nil {
		sm.onEvent = func(Event) {}
	}
	return sm
}

// SetOnEvent replaces the event sink. Startup-only: call it before the
// server accepts traffic (main.go wires it to the api layer's fan-out,
// which needs the built Server). The zero sink swallows events until
// wiring completes.
func (sm *SessionManager) SetOnEvent(fn func(Event)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onEvent = fn
}

// session is one live call. All access is under the manager lock.
type session struct {
	kind          CallKind
	id            string
	groupID       string // group and broadcast calls
	caller        string // private calls
	callee        string // private calls
	floorControl  bool   // false only for direct private calls
	emergency     bool   // escalated to the emergency tier (priority 9)
	imminentPeril bool   // distinct flagged mode: "call about help"
	participants  map[string]*participant
	machine       *floorMachine
	directTokens  map[string]string // direct calls: stable per-participant tokens
}

type participant struct {
	priority floor.FloorLevel
	joinedAt time.Time
}

// StartGroup opens a group call for an affiliated group. The initiator joins
// immediately; affiliated members join afterwards (late entry is the same
// Join path and hears the current speaker).
func (sm *SessionManager) StartGroup(groupID, initiator string, initiatorPriority floor.FloorLevel) (SessionInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.affiliatedLocked(groupID, initiator) {
		return SessionInfo{}, ErrNotAffiliated
	}
	return sm.startLocked(KindGroup, groupID, initiator, initiatorPriority, "", true), nil
}

// StartBroadcast opens a broadcast group call: one dispatcher voice, a
// receive-only floor for everyone else. Only a dispatcher-priority user may
// start one, and they must be affiliated.
func (sm *SessionManager) StartBroadcast(groupID, dispatcher string, dispatcherPriority floor.FloorLevel) (SessionInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if dispatcherPriority < floor.PriorityDispatcher {
		return SessionInfo{}, ErrNoFloorControl
	}
	// A broadcast is by definition a dispatcher announcement: the P10 gate
	// above already restricts it to net control, so the affiliation rule
	// that governs group membership does not apply to the announcer —
	// dispatch announces to any talkgroup it serves.
	return sm.startLocked(KindBroadcast, groupID, dispatcher, dispatcherPriority, "", true), nil
}

// StartPrivate opens a one-to-one call. The callee is rung; they become a
// participant by Join (accept). With floorControl=false the call is a
// Release 18 private call without floor control ("direct"): both parties may
// transmit simultaneously and the floor machine is bypassed.
func (sm *SessionManager) StartPrivate(caller, callee string, callerPriority floor.FloorLevel, floorControl bool) (SessionInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.startLocked(KindPrivate, "", caller, callerPriority, callee, floorControl), nil
}

// startLocked creates the call; the initiator/caller is its first
// participant. Caller holds sm.mu.
func (sm *SessionManager) startLocked(kind CallKind, groupID, initiator string, initiatorPriority floor.FloorLevel, callee string, floorControl bool) SessionInfo {
	id := "call_" + sm.tokenGen()
	s := &session{
		kind:         kind,
		id:           id,
		groupID:      groupID,
		caller:       initiator,
		callee:       callee,
		floorControl: floorControl,
		participants: map[string]*participant{},
		directTokens: map[string]string{},
	}
	if floorControl {
		cfg := MachineConfig{
			MaxTalkDuration: sm.maxTalk,
			MaxQueueDepth:   sm.maxQueue,
			TokenGen:        sm.tokenGen,
		}
		// The machine mirrors every transition into the SFU's grant mirror
		// while holding its own lock — the seam that keeps "who holds the
		// floor" and "who may be relayed" from ever disagreeing.
		if sm.media != nil {
			cfg.Gate = boundGate{gate: sm.media, call: id}
		}
		// The machine invokes this after dropping its own lock, so the
		// callback may take the manager lock: no ordering inversion. The
		// mirror writes for expiry already happened inside the machine's
		// critical section (strip + queue-head grant).
		cfg.OnExpired = func(holder string, ds []floor.FloorDecision) {
			sm.onEvent(Event{
				Type: EventFloorDecisions, CallID: id, Actor: holder,
				Kind: kind, GroupID: groupID, Decisions: ds,
			})
		}
		s.machine = newFloorMachine(cfg)
	}
	s.participants[initiator] = &participant{priority: initiatorPriority, joinedAt: time.Now()}
	sm.calls[s.id] = s
	info := sm.snapshotLocked(s)
	sm.onEvent(Event{Type: EventCallStarted, CallID: s.id, Actor: initiator, GroupID: groupID, Kind: kind})
	return info
}

// affiliatedLocked applies the affiliation checker; nil denies. Caller holds
// sm.mu.
func (sm *SessionManager) affiliatedLocked(groupID, userID string) bool {
	return sm.affil != nil && sm.affil(userID, groupID)
}

// Join admits a user to a live call — first join or late entry. The return
// carries the current speaker so a late joiner renders the live talker
// immediately (the SFU starts the audio path separately).
func (sm *SessionManager) Join(callID, userID string, priority floor.FloorLevel) (SessionInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.calls[callID]
	if !ok {
		return SessionInfo{}, ErrUnknownCall
	}
	if _, already := s.participants[userID]; already {
		return SessionInfo{}, ErrAlreadyParticipant
	}
	switch s.kind {
	case KindGroup, KindBroadcast:
		if !sm.affiliatedLocked(s.groupID, userID) {
			return SessionInfo{}, ErrNotAffiliated
		}
	case KindPrivate:
		if userID != s.caller && userID != s.callee {
			return SessionInfo{}, ErrNotAParty
		}
	}
	s.participants[userID] = &participant{priority: priority, joinedAt: time.Now()}
	info := sm.snapshotLocked(s)
	sm.onEvent(Event{Type: EventParticipantJoined, CallID: callID, Actor: userID, Kind: s.kind, GroupID: s.groupID})
	return info, nil
}

// RequestFloor routes a talk-burst request through the call's floor machine.
// The requester must have joined; their effective priority is the one their
// join recorded — client-claimed levels are never trusted. Emergency
// requests upgrade to priority 9 inside the machine. Direct private calls
// grant a stable per-participant token and consult no machine. Broadcast
// listeners are denied with reason "listen-only".
func (sm *SessionManager) RequestFloor(callID, userID string, emergency bool) (floor.FloorDecision, error) {
	sm.mu.Lock()
	s, ok := sm.calls[callID]
	if !ok {
		sm.mu.Unlock()
		return floor.FloorDecision{}, ErrUnknownCall
	}
	p, joined := s.participants[userID]
	if !joined {
		sm.mu.Unlock()
		return floor.FloorDecision{}, ErrNotInCall
	}
	if !s.floorControl {
		d := sm.directGrantLocked(s, userID, p.priority)
		sm.writeGate(callID, []floor.FloorDecision{d}) // same critical section as the token
		sm.mu.Unlock()
		sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: s.kind, GroupID: s.groupID, Decisions: []floor.FloorDecision{d}})
		return d, nil
	}
	if s.kind == KindBroadcast && p.priority < floor.PriorityDispatcher {
		d := floor.FloorDecision{
			UserID: userID, Outcome: floor.OutcomeDenied,
			Level: p.priority, Reason: "listen-only",
		}
		sm.mu.Unlock()
		sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: s.kind, GroupID: s.groupID, Decisions: []floor.FloorDecision{d}})
		return d, nil
	}
	decision := s.machine.Request(userID, p.priority, emergency)
	queue := s.machine.QueueUserIDs()
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: s.kind, GroupID: s.groupID, Decisions: []floor.FloorDecision{decision}, Queue: queue})
	return decision, nil
}

// directGrantLocked mints (once) and restates a participant's direct-call
// token: full-duplex private calls have no floor to arbitrate.
func (sm *SessionManager) directGrantLocked(s *session, userID string, lvl floor.FloorLevel) floor.FloorDecision {
	tok, ok := s.directTokens[userID]
	if !ok {
		tok = "direct_" + sm.tokenGen()
		s.directTokens[userID] = tok
	}
	return floor.FloorDecision{
		UserID: userID, Outcome: floor.OutcomeGranted,
		Level: lvl, Token: tok,
	}
}

// ReleaseFloor releases a talk burst, cancels a queue entry, or drops a
// direct-call token. Releasing the floor auto-grants the queue head; the
// returned decisions are the grants to broadcast.
func (sm *SessionManager) ReleaseFloor(callID, userID string) ([]floor.FloorDecision, error) {
	sm.mu.Lock()
	s, ok := sm.calls[callID]
	if !ok {
		sm.mu.Unlock()
		return nil, ErrUnknownCall
	}
	if _, joined := s.participants[userID]; !joined {
		sm.mu.Unlock()
		return nil, ErrNotInCall
	}
	var decisions []floor.FloorDecision
	if s.floorControl {
		decisions = s.machine.Release(userID) // holder: strip + auto-grant mirrored inside the machine lock
	} else {
		delete(s.directTokens, userID)
		sm.revokeGate(callID, userID)
	}
	kind := s.kind
	sm.mu.Unlock()
	// Always emitted — even with empty decisions — so the wiring echoes the
	// release (and queue cancellation) to every client.
	sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: kind, GroupID: s.groupID, Decisions: decisions})
	return decisions, nil
}

// RevokeFloor strips the floor from the current talker. The revoking
// authority must outrank the holder (dispatchers talk at P10 and outrank
// everything); the machine enforces this. Direct calls have no floor to
// revoke.
func (sm *SessionManager) RevokeFloor(callID, target, by string, byLevel floor.FloorLevel) ([]floor.FloorDecision, error) {
	sm.mu.Lock()
	s, ok := sm.calls[callID]
	if !ok {
		sm.mu.Unlock()
		return nil, ErrUnknownCall
	}
	if !s.floorControl {
		sm.mu.Unlock()
		return nil, ErrNoFloorControl
	}
	decisions, err := s.machine.Revoke(target, by, byLevel)
	kind := s.kind
	sm.mu.Unlock()
	if err == nil && len(decisions) > 0 {
		sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: by, Kind: kind, GroupID: s.groupID, Decisions: decisions})
	}
	return decisions, err
}

// RemoveParticipant ejects a user from a call (dispatcher action). The
// caller's level must outrank the target's; a holding target is revoked
// (their floor is stripped, not re-queued) and a queued entry is cancelled
// before removal.
func (sm *SessionManager) RemoveParticipant(callID, target, by string, byLevel floor.FloorLevel) ([]floor.FloorDecision, error) {
	sm.mu.Lock()
	s, ok := sm.calls[callID]
	if !ok {
		sm.mu.Unlock()
		return nil, ErrUnknownCall
	}
	p, joined := s.participants[target]
	if !joined {
		sm.mu.Unlock()
		return nil, ErrNotInCall
	}
	if !floor.Preempts(byLevel, p.priority) {
		sm.mu.Unlock()
		return nil, ErrInsufficientAuthority
	}
	var decisions []floor.FloorDecision
	if s.floorControl {
		if g, holding := s.machine.Grant(); holding && g.UserID == target {
			decisions, _ = s.machine.Revoke(target, by, byLevel) // strip + auto-grant mirrored inside the machine lock
		} else {
			s.machine.Release(target) // cancel any queued entry
		}
	}
	delete(s.directTokens, target)
	sm.revokeGate(callID, target) // direct-token grants have no machine to strip them
	delete(s.participants, target)
	kind := s.kind
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventParticipantRemoved, CallID: callID, Actor: by, Target: target, Kind: kind, GroupID: s.groupID, Decisions: decisions})
	return decisions, nil
}

// EscalateEmergency upgrades a live call to the emergency tier (spec:
// "Emergency flag upgrades call priority to 9 and pre-empts the active
// floor"). Three effects resolve in this one call, none depending on
// client goodwill: the user's arbitration priority for the call becomes 9
// for its remaining life; a pre-empting emergency floor request is issued
// immediately — the machine strips any lower-priority talker in the same
// scheduling turn (or queues behind net control, whose P10 outranks
// emergency); and the call is marked emergency so dispatch renders it as
// one. Imminent peril reuses the emergency tier under a distinct flag so
// the audit log can tell "call for help" from "call about help".
//
// The returned decision is the machine's arbitration result: a grant
// (idle floor or pre-emption), a queue entry (net control holds the
// floor), or a listen-only denial (the caller is a broadcast listener —
// the dispatcher's announcement voice cannot be pre-empted by a P9
// listener; the alert the API layer attaches still reaches dispatch).
// Direct (floor-control-free) private calls have no floor to pre-empt;
// their grant is restated and the escalation's effect is the alert and
// the priority/state change alone.
func (sm *SessionManager) EscalateEmergency(callID, userID string, imminentPeril bool) (floor.FloorDecision, SessionInfo, error) {
	sm.mu.Lock()
	s, ok := sm.calls[callID]
	if !ok {
		sm.mu.Unlock()
		return floor.FloorDecision{}, SessionInfo{}, ErrUnknownCall
	}
	p, joined := s.participants[userID]
	if !joined {
		sm.mu.Unlock()
		return floor.FloorDecision{}, SessionInfo{}, ErrNotInCall
	}

	s.emergency = true
	if imminentPeril {
		s.imminentPeril = true
	}
	p.priority = floor.PriorityEmergency // every later burst arbitrates at 9

	var decision floor.FloorDecision
	var queue []string
	switch {
	case !s.floorControl:
		// Full-duplex direct call: no floor to pre-empt. Restate the
		// direct grant so the mirror and the wire carry the escalated
		// user's permission.
		decision = sm.directGrantLocked(s, userID, p.priority)
		sm.writeGate(callID, []floor.FloorDecision{decision})
	case s.kind == KindBroadcast && p.priority < floor.PriorityDispatcher:
		// A broadcast listener cannot strip the dispatcher's voice (P9
		// loses to the P10 announcement floor); dispatch still sees the
		// alert the API layer records.
		decision = floor.FloorDecision{
			UserID: userID, Outcome: floor.OutcomeDenied,
			Level: p.priority, Emergency: true, Reason: "listen-only",
		}
	default:
		decision = s.machine.Request(userID, p.priority, true)
		queue = s.machine.QueueUserIDs()
	}

	info := sm.snapshotLocked(s)
	kind := s.kind
	sm.mu.Unlock()
	sm.onEvent(Event{
		Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: kind,
		Decisions: []floor.FloorDecision{decision}, Queue: queue,
	})
	return decision, info, nil
}

// Leave records a participant leaving. A holder's departure auto-grants the
// queue head; a private call ends when either party leaves, and a group or
// broadcast call ends when the last participant leaves.
func (sm *SessionManager) Leave(callID, userID string) ([]floor.FloorDecision, error) {
	sm.mu.Lock()
	s, ok := sm.calls[callID]
	if !ok {
		sm.mu.Unlock()
		return nil, ErrUnknownCall
	}
	if _, joined := s.participants[userID]; !joined {
		sm.mu.Unlock()
		return nil, ErrNotInCall
	}
	var decisions []floor.FloorDecision
	if s.floorControl {
		decisions = s.machine.Release(userID) // holder: strip + auto-grant mirrored inside the machine lock
	}
	delete(s.directTokens, userID)
	sm.revokeGate(callID, userID)
	delete(s.participants, userID)
	ended := s.kind == KindPrivate || len(s.participants) == 0
	kind := s.kind
	if ended {
		if s.floorControl {
			s.machine.Stop()
		}
		sm.clearGate(callID) // teardown: the mirror forgets the call
		delete(sm.calls, callID)
	}
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventParticipantLeft, CallID: callID, Actor: userID, Kind: kind, GroupID: s.groupID, Decisions: decisions})
	if ended {
		sm.onEvent(Event{Type: EventCallEnded, CallID: callID, Actor: userID, Kind: kind, GroupID: s.groupID})
	}
	return decisions, nil
}

// End force-closes a call (dispatcher action). The machine is stopped with
// the call; no expiry can fire afterwards.
func (sm *SessionManager) End(callID, by string) error {
	sm.mu.Lock()
	s, ok := sm.calls[callID]
	if !ok {
		sm.mu.Unlock()
		return ErrUnknownCall
	}
	if s.floorControl {
		s.machine.Stop()
	}
	sm.clearGate(callID) // teardown: the mirror forgets the call
	kind := s.kind
	delete(sm.calls, callID)
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventCallEnded, CallID: callID, Actor: by, Kind: kind, GroupID: s.groupID})
	return nil
}

// Snapshot returns the current state of a call (roster rendering, late
// entry).
func (sm *SessionManager) Snapshot(callID string) (SessionInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.calls[callID]
	if !ok {
		return SessionInfo{}, ErrUnknownCall
	}
	return sm.snapshotLocked(s), nil
}

// snapshotLocked renders client-visible state. Caller holds sm.mu.
func (sm *SessionManager) snapshotLocked(s *session) SessionInfo {
	info := SessionInfo{
		CallID:        s.id,
		Kind:          s.kind,
		GroupID:       s.groupID,
		FloorControl:  s.floorControl,
		Emergency:     s.emergency,
		ImminentPeril: s.imminentPeril,
		Participants:  make([]string, 0, len(s.participants)),
	}
	for uid := range s.participants {
		info.Participants = append(info.Participants, uid)
	}
	sort.Strings(info.Participants)
	if s.floorControl {
		if g, ok := s.machine.Grant(); ok {
			info.Speaker = g.UserID
			info.SpeakerSince = g.Since
		}
		info.Queue = s.machine.Queue()
	}
	return info
}

// Count returns the number of live calls (diagnostics).
func (sm *SessionManager) Count() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return len(sm.calls)
}

// GroupOf reports a live call's talkgroup: the resolver the carrier RX
// bridge uses to apply the [carrier] groups allowlist. Private calls and
// unknown calls resolve to no group. Never blocks on the manager mutex:
// the manager invokes some session-event callbacks with its own lock held
// (call starts), so this returns "no group" on contention instead of
// deadlocking a callback that re-enters it.
func (sm *SessionManager) GroupOf(callID string) (string, bool) {
	// TryLock, not Lock: the manager emits some events (call starts) with
	// its own lock held, and the carrier bridge's event callback resolves
	// groups as a fallback. A blocking lookup there would self-deadlock;
	// on contention the caller treats the call as unflagged, which the
	// event's own GroupID field makes irrelevant for flagged calls.
	if !sm.mu.TryLock() {
		return "", false
	}
	defer sm.mu.Unlock()
	s, ok := sm.calls[callID]
	if !ok || s.kind == KindPrivate || s.groupID == "" {
		return "", false
	}
	return s.groupID, true
}
