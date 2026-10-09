// Package protocol fixes the WSS JSON message contract shared by the server,
// dispatcher console, and mobile clients. Field names match the spec's code
// sample exactly; later PRs (floor engine, media, emergency) consume these
// types verbatim.
package protocol

import "encoding/json"

// Client → server and server → client message types. The floor set mirrors
// 3GPP TS 24.379 floor-control semantics (request/grant/deny/release/
// pre-empt/revoke) carried as WebSocket-JSON instead of SIP — the spec's
// documented wire-protocol deviation.
const (
	TypeFloorRequest  = "FloorRequest"
	TypeFloorGranted  = "FloorGranted"
	TypeFloorDenied   = "FloorDenied"
	TypeFloorReleased = "FloorReleased"
	TypeFloorPreempt  = "FloorPreempted"
	TypeFloorRevoke   = "FloorRevoke"

	TypeCallStart  = "CallStart"
	TypeCallJoined = "CallJoined"
	TypeCallEnded  = "CallEnded"

	TypeEmergencyAlert = "EmergencyAlert"

	// Foundation-live types: presence and affiliation fan-out on /ws.
	TypePresenceUpdate     = "PresenceUpdate"
	TypeAffiliationChanged = "AffiliationChanged"
)

// Envelope is the outer frame of every WSS message; each typed message
// embeds it and carries its own Type value.
type Envelope struct {
	Type string `json:"type"`
}

// ParseType extracts the message type without fully decoding the payload.
func ParseType(raw []byte) (string, error) {
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", err
	}
	return e.Type, nil
}

// FloorRequest — client → server: ask for the talk floor.
type FloorRequest struct {
	Type      string `json:"type"`
	CallID    string `json:"callId"`
	UserID    string `json:"userId"`
	Priority  int    `json:"priority"`
	Emergency bool   `json:"emergency"`
}

// FloorGranted — server → all participants: arbitration result; queue lists
// user IDs waiting in priority order.
type FloorGranted struct {
	Type   string   `json:"type"`
	CallID string   `json:"callId"`
	UserID string   `json:"userId"`
	Queue  []string `json:"queue"`
}

// FloorDenied — server → requester: rejected, with queue position when
// the request was spillover and reason ("busy", "not-affiliated", ...).
type FloorDenied struct {
	Type          string `json:"type"`
	CallID        string `json:"callId"`
	UserID        string `json:"userId"`
	Reason        string `json:"reason"`
	QueuePosition int    `json:"queuePosition"`
}

// FloorReleased — client → server after releasing PTT (echoed to all).
type FloorReleased struct {
	Type   string `json:"type"`
	CallID string `json:"callId"`
	UserID string `json:"userId"`
}

// FloorPreempted — server → all: a higher-priority floor took the call
// mid-burst (emergency or dispatcher net control).
type FloorPreempted struct {
	Type      string `json:"type"`
	CallID    string `json:"callId"`
	By        string `json:"by"`
	Emergency bool   `json:"emergency"`
}

// FloorRevoke — dispatcher → server: strip the current talker's floor.
type FloorRevoke struct {
	Type   string `json:"type"`
	CallID string `json:"callId"`
	By     string `json:"by"`
	Reason string `json:"reason"`
}

// CallKind distinguishes the three call entry shapes.
type CallKind string

const (
	CallKindGroup     CallKind = "group"
	CallKindPrivate   CallKind = "private"
	CallKindBroadcast CallKind = "broadcast"
)

// CallStart — client → server: open a call (pre-arranged group, private, or
// broadcast). The server validates affiliation and announces to targets.
type CallStart struct {
	Type        string   `json:"type"`
	CallID      string   `json:"callId"`
	GroupID     string   `json:"groupId,omitempty"`
	Kind        CallKind `json:"kind"`
	InitiatorID string   `json:"initiatorId"`
}

// CallJoined — server → participants: a party joined (late entry).
type CallJoined struct {
	Type   string `json:"type"`
	CallID string `json:"callId"`
	UserID string `json:"userId"`
}

// CallEnded — server → participants: call torn down.
type CallEnded struct {
	Type   string `json:"type"`
	CallID string `json:"callId"`
	By     string `json:"by"`
}

// EmergencyAlert — client → server: one-tap alert (voiceless) or the alert
// that accompanies an emergency call. Location is client-reported
// (lat/lon WGS-84) and omitted when the device has no fix.
type EmergencyAlert struct {
	Type      string   `json:"type"`
	AlertID   string   `json:"alertId"`
	UserID    string   `json:"userId"`
	CallID    string   `json:"callId,omitempty"` // empty for a voiceless alert
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
	Emergency bool     `json:"emergency"`
	Note      string   `json:"note,omitempty"`
}

// PresenceState values for PresenceUpdate.
const (
	PresenceOnline  = "online"
	PresenceOffline = "offline"
)

// PresenceUpdate — server → all connected clients: a user connected to or
// dropped from /ws.
type PresenceUpdate struct {
	Type   string `json:"type"`
	UserID string `json:"userId"`
	State  string `json:"state"`
	At     int64  `json:"at"` // unix millis
}

// AffiliationState values for AffiliationChanged (TS 23.280 affiliation).
const (
	AffiliationAffiliated   = "affiliated"
	AffiliationDeaffiliated = "deaffiliated"
)

// AffiliationChanged — server → all connected clients: a user's affiliation
// with a group changed. Dispatched within the same round trip as the REST
// mutation that caused it.
type AffiliationChanged struct {
	Type    string `json:"type"`
	UserID  string `json:"userId"`
	GroupID string `json:"groupId"`
	State   string `json:"state"`
	At      int64  `json:"at"` // unix millis
}
