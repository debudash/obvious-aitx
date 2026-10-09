package api

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

// The dispatcher surface: force-end a call, revoke the active talker,
// remove a participant, and start broadcast calls. Every endpoint is
// dispatcher-only (routed behind the admin middleware — the role claim is
// the authorization) and every action writes an audit row, so dispatch
// oversight is replayable end to end.
//
// MediaHooks carry the media-plane consequences of dispatcher actions
// without importing the SFU here: main.go wires the closures from the live
// SFU. A removed participant's transport is dropped server-side — zero
// further packets — and an ended call tears every leg down. MintRoomToken
// issues the room-scoped media token a call participant presents with
// their MediaOffer (the mint the MediaOffer contract expects clients to
// be able to reach). Nil hooks (as in control-plane-only tests) skip the
// media side; production always wires them.
type MediaHooks struct {
	RemoveParticipant func(callID, userID string) bool
	EndCall           func(callID string) int
	// MintRoomToken issues a room-scoped media token binding this call
	// and this user. Wired from the SFU's RoomTokens at startup; the
	// media-token route answers 503 when unset.
	MintRoomToken func(callID, userID string) (string, error)
}

// broadcastRequest starts a dispatcher announcement call to one group.
type broadcastRequest struct {
	GroupID string `json:"groupId"`
}

// mapCallError translates call-control sentinels into HTTP statuses — the
// sibling of mapStoreError for the live-session layer.
func mapCallError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, callcontrol.ErrUnknownCall):
		writeError(w, http.StatusNotFound, "unknown call")
	case errors.Is(err, callcontrol.ErrNotAffiliated),
		errors.Is(err, callcontrol.ErrNotInCall),
		errors.Is(err, callcontrol.ErrNotAParty):
		writeError(w, http.StatusForbidden, "not a participant of this call")
	case errors.Is(err, callcontrol.ErrAlreadyParticipant),
		errors.Is(err, callcontrol.ErrNoFloorControl):
		writeError(w, http.StatusConflict, "conflict")
	default:
		log.Printf("api: call error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}

// sessionDTO is the wire form of a call snapshot — what the console's
// active-calls panel and the field clients render.
type sessionDTO struct {
	CallID        string   `json:"callId"`
	Kind          string   `json:"kind"`
	GroupID       string   `json:"groupId,omitempty"`
	FloorControl  bool     `json:"floorControl"`
	Emergency     bool     `json:"emergency"`
	ImminentPeril bool     `json:"imminentPeril"`
	Participants  []string `json:"participants"`
	Speaker       string   `json:"speaker,omitempty"`
	SpeakerSince  string   `json:"speakerSince,omitempty"`
	Queue         []string `json:"queue"`
}

func toSessionDTO(info callcontrol.SessionInfo) sessionDTO {
	q := make([]string, 0, len(info.Queue))
	for _, qr := range info.Queue {
		q = append(q, qr.UserID)
	}
	if info.Participants == nil {
		info.Participants = []string{}
	}
	dto := sessionDTO{
		CallID: info.CallID, Kind: string(info.Kind), GroupID: info.GroupID,
		FloorControl: info.FloorControl, Emergency: info.Emergency,
		ImminentPeril: info.ImminentPeril, Participants: info.Participants,
		Speaker: info.Speaker, Queue: q,
	}
	if !info.SpeakerSince.IsZero() {
		dto.SpeakerSince = info.SpeakerSince.Format(time.RFC3339)
	}
	return dto
}

// protocolAlertAck builds the fan-out frame for an acknowledged alert.
func protocolAlertAck(alertID, by string) protocol.EmergencyAlertAck {
	return protocol.EmergencyAlertAck{
		Type: protocol.TypeEmergencyAlertAck, AlertID: alertID, AcknowledgedBy: by,
	}
}

// handleDispatchEndCall force-ends a live call. Media teardown follows the
// control decision — no orphaned transports keep forwarding after the call
// is gone.
func (s *Server) handleDispatchEndCall(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	callID := r.PathValue("id")
	if err := s.calls.End(callID, actor.UserID); mapCallError(w, err) {
		return
	}
	if s.media != nil && s.media.EndCall != nil {
		s.media.EndCall(callID)
	}
	if err := s.st.Append(r.Context(), store.AuditEntry{
		ActorID: actor.UserID, Action: "dispatch.call.ended",
		TargetType: "call", TargetID: callID, Detail: "{}",
	}); err != nil {
		writeInternal(w, err, "audit dispatch end")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ended"})
}

// handleDispatchRevoke strips the active talker's floor. The current
// speaker is resolved server-side from the call snapshot — the console
// never names the target, so a stale roster cannot revoke the wrong user.
func (s *Server) handleDispatchRevoke(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	callID := r.PathValue("id")
	snap, err := s.calls.Snapshot(callID)
	if mapCallError(w, err) {
		return
	}
	if snap.Speaker == "" {
		writeError(w, http.StatusConflict, "no active speaker to revoke")
		return
	}
	decisions, err := s.calls.RevokeFloor(callID, snap.Speaker, actor.UserID, floor.PriorityDispatcher)
	if mapCallError(w, err) {
		return
	}
	if err := s.st.Append(r.Context(), store.AuditEntry{
		ActorID: actor.UserID, Action: "dispatch.floor.revoked",
		TargetType: "call", TargetID: callID,
		Detail: auditDetail(map[string]any{"target": snap.Speaker, "reason": "net-control"}),
	}); err != nil {
		writeInternal(w, err, "audit dispatch revoke")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": snap.Speaker, "decisions": decisions})
}

// handleDispatchRemoveParticipant strips one party from a live call: floor
// revoked, membership dropped, media leg torn down. The decision stream is
// fanned out by the SessionManager's event sink.
func (s *Server) handleDispatchRemoveParticipant(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	callID := r.PathValue("id")
	target := r.PathValue("userId")
	decisions, err := s.calls.RemoveParticipant(callID, target, actor.UserID, floor.PriorityDispatcher)
	if mapCallError(w, err) {
		return
	}
	if s.media != nil && s.media.RemoveParticipant != nil {
		s.media.RemoveParticipant(callID, target)
	}
	if err := s.st.Append(r.Context(), store.AuditEntry{
		ActorID: actor.UserID, Action: "dispatch.participant.removed",
		TargetType: "call", TargetID: callID,
		Detail: auditDetail(map[string]any{"removed": target}),
	}); err != nil {
		writeInternal(w, err, "audit dispatch remove")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": target, "decisions": decisions})
}

// handleDispatchBroadcast starts a dispatcher announcement call: one to
// many, receive-only floor for everyone else, dispatcher talking at P10.
func (s *Server) handleDispatchBroadcast(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var req broadcastRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.GroupID == "" {
		writeError(w, http.StatusBadRequest, "groupId is required")
		return
	}
	// The announcer is exempt from the affiliation rule, so existence is
	// the remaining guard: never open a broadcast into a group that is
	// not there.
	if _, err := s.st.GetGroup(r.Context(), req.GroupID); mapStoreError(w, err) {
		return
	}
	info, err := s.calls.StartBroadcast(req.GroupID, actor.UserID, floor.PriorityDispatcher)
	if mapCallError(w, err) {
		return
	}
	// The announcement opens with dispatch speaking: the announcer takes
	// the floor immediately (listeners below P10 stay receive-only). A
	// failed grant leaves the call open — dispatch simply retries.
	if d, err := s.calls.RequestFloor(info.CallID, actor.UserID, false); err != nil || d.Outcome != floor.OutcomeGranted {
		log.Printf("api: broadcast announcer floor not granted: outcome=%s err=%v", d.Outcome, err)
	} else if info, err = s.calls.Snapshot(info.CallID); mapCallError(w, err) {
		return
	}
	if err := s.st.Append(r.Context(), store.AuditEntry{
		ActorID: actor.UserID, Action: "dispatch.broadcast.started",
		TargetType: "call", TargetID: info.CallID,
		Detail: auditDetail(map[string]any{"groupId": req.GroupID}),
	}); err != nil {
		writeInternal(w, err, "audit dispatch broadcast")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"call": toSessionDTO(info)})
}
