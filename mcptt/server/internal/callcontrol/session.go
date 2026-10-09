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
// format carries the array).
type Event struct {
	Type      EventType
	CallID    string
	Actor     string
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
	CallID       string
	Kind         CallKind
	GroupID      string
	FloorControl bool
	Participants []string
	Speaker      string
	SpeakerSince time.Time // zero when idle
	Queue        []floor.QueuedRequest
}

// SessionManager owns every live call. Safe for concurrent use.
type SessionManager struct {
	mu       sync.Mutex
	calls    map[string]*session
	maxTalk  time.Duration
	maxQueue int
	affil    func(userID, groupID string) bool
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

// session is one live call. All access is under the manager lock.
type session struct {
	kind         CallKind
	id           string
	groupID      string // group and broadcast calls
	caller       string // private calls
	callee       string // private calls
	floorControl bool   // false only for direct private calls
	participants map[string]*participant
	machine      *floorMachine
	directTokens map[string]string // direct calls: stable per-participant tokens
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
	if !sm.affiliatedLocked(groupID, dispatcher) {
		return SessionInfo{}, ErrNotAffiliated
	}
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
		s.machine = newFloorMachine(MachineConfig{
			MaxTalkDuration: sm.maxTalk,
			MaxQueueDepth:   sm.maxQueue,
			TokenGen:        sm.tokenGen,
			// The machine invokes this after dropping its own lock, so the
			// callback may take the manager lock: no ordering inversion.
			OnExpired: func(holder string, ds []floor.FloorDecision) {
				sm.onEvent(Event{
					Type: EventFloorDecisions, CallID: id, Actor: holder,
					Kind: kind, Decisions: ds,
				})
			},
		})
	}
	s.participants[initiator] = &participant{priority: initiatorPriority, joinedAt: time.Now()}
	sm.calls[s.id] = s
	info := sm.snapshotLocked(s)
	sm.onEvent(Event{Type: EventCallStarted, CallID: s.id, Actor: initiator, Kind: kind})
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
	sm.onEvent(Event{Type: EventParticipantJoined, CallID: callID, Actor: userID, Kind: s.kind})
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
		sm.mu.Unlock()
		sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: s.kind, Decisions: []floor.FloorDecision{d}})
		return d, nil
	}
	if s.kind == KindBroadcast && p.priority < floor.PriorityDispatcher {
		d := floor.FloorDecision{
			UserID: userID, Outcome: floor.OutcomeDenied,
			Level: p.priority, Reason: "listen-only",
		}
		sm.mu.Unlock()
		sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: s.kind, Decisions: []floor.FloorDecision{d}})
		return d, nil
	}
	decision := s.machine.Request(userID, p.priority, emergency)
	queue := s.machine.QueueUserIDs()
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: s.kind, Decisions: []floor.FloorDecision{decision}, Queue: queue})
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
		decisions = s.machine.Release(userID)
	} else {
		delete(s.directTokens, userID)
	}
	sm.mu.Unlock()
	if len(decisions) > 0 {
		sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: userID, Kind: s.kind, Decisions: decisions})
	}
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
		sm.onEvent(Event{Type: EventFloorDecisions, CallID: callID, Actor: by, Kind: kind, Decisions: decisions})
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
			decisions, _ = s.machine.Revoke(target, by, byLevel)
		} else {
			s.machine.Release(target) // cancel any queued entry
		}
	}
	delete(s.directTokens, target)
	delete(s.participants, target)
	kind := s.kind
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventParticipantRemoved, CallID: callID, Actor: by, Kind: kind, Decisions: decisions})
	return decisions, nil
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
		decisions = s.machine.Release(userID) // holder: auto-grants the queue head
	}
	delete(s.directTokens, userID)
	delete(s.participants, userID)
	ended := s.kind == KindPrivate || len(s.participants) == 0
	kind := s.kind
	if ended {
		if s.floorControl {
			s.machine.Stop()
		}
		delete(sm.calls, callID)
	}
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventParticipantLeft, CallID: callID, Actor: userID, Kind: kind, Decisions: decisions})
	if ended {
		sm.onEvent(Event{Type: EventCallEnded, CallID: callID, Actor: userID, Kind: kind})
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
	kind := s.kind
	delete(sm.calls, callID)
	sm.mu.Unlock()
	sm.onEvent(Event{Type: EventCallEnded, CallID: callID, Actor: by, Kind: kind})
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
		CallID:       s.id,
		Kind:         s.kind,
		GroupID:      s.groupID,
		FloorControl: s.floorControl,
		Participants: make([]string, 0, len(s.participants)),
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
