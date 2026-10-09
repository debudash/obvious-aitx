package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

// The emergency surface: the pre-empting emergency call, the voiceless
// alert, the dispatcher acknowledgement loop, and the audit rows that make
// every transition replayable. Priority ladder: an emergency call runs at
// floor.PriorityEmergency (9); a dispatcher's broadcast/controls outrank it
// at 10 (net control), matching the spec's console contract.

type emergencyCallRequest struct {
	CallID        string   `json:"callId"`        // escalate a live call
	GroupID       string   `json:"groupId"`       // or start a new emergency group call
	ImminentPeril bool     `json:"imminentPeril"` // "call about help" flag
	Lat           *float64 `json:"lat"`
	Lon           *float64 `json:"lon"`
	Note          string   `json:"note"`
}

type alertDTO struct {
	ID             string   `json:"id"`
	UserID         string   `json:"userId"`
	CallID         string   `json:"callId,omitempty"`
	Kind           string   `json:"kind"`
	Lat            *float64 `json:"lat,omitempty"`
	Lon            *float64 `json:"lon,omitempty"`
	Note           string   `json:"note,omitempty"`
	Status         string   `json:"status"`
	AcknowledgedBy string   `json:"acknowledgedBy,omitempty"`
	CreatedAt      string   `json:"createdAt"`
}

func toAlertDTO(a store.Alert) alertDTO {
	return alertDTO{
		ID: a.ID, UserID: a.UserID, CallID: a.CallID, Kind: a.Kind,
		Lat: a.Lat, Lon: a.Lon, Note: a.Note, Status: a.Status,
		AcknowledgedBy: a.AcknowledgedBy, CreatedAt: a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// alertKind maps the request's peril flag to the stored kind so the audit
// log can tell "call for help" from "call about help".
func alertKind(imminentPeril bool) string {
	if imminentPeril {
		return store.AlertKindImminentPeril
	}
	return store.AlertKindEmergency
}

// handleEmergencyCall starts a new emergency group call or escalates a live
// call the caller is on. The server-side effects the spec pins — priority
// upgrade to 9, floor pre-emption, location attached to the dispatcher
// alert — all happen here regardless of client goodwill, and each lands in
// the audit log.
func (s *Server) handleEmergencyCall(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var req emergencyCallRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if (req.CallID == "") == (req.GroupID == "") {
		writeError(w, http.StatusBadRequest, "exactly one of callId or groupId is required")
		return
	}
	ctx := r.Context()

	// The store's priority is authoritative: an admin may have changed it
	// since the token was issued.
	u, err := s.st.GetUserByID(ctx, actor.UserID)
	if mapStoreError(w, err) {
		return
	}

	var info callcontrol.SessionInfo
	var decision floor.FloorDecision
	if req.CallID != "" {
		if !s.callParticipant(r, req.CallID, actor.UserID) {
			writeError(w, http.StatusForbidden, "only call participants may escalate a call")
			return
		}
		decision, info, err = s.calls.EscalateEmergency(req.CallID, actor.UserID, req.ImminentPeril)
		if mapCallError(w, err) {
			return
		}
	} else {
		info, err = s.calls.StartGroup(req.GroupID, actor.UserID, floor.FloorLevel(u.Priority))
		if mapCallError(w, err) {
			return
		}
		decision, info, err = s.calls.EscalateEmergency(info.CallID, actor.UserID, req.ImminentPeril)
		if mapCallError(w, err) {
			return
		}
	}

	alert, err := s.st.CreateAlert(ctx, actor.UserID, info.CallID, alertKind(req.ImminentPeril), req.Lat, req.Lon, req.Note)
	if mapStoreError(w, err) {
		return
	}
	if err := s.st.Append(ctx, store.AuditEntry{
		ActorID:    actor.UserID,
		Action:     "emergency.call.started",
		TargetType: "call",
		TargetID:   info.CallID,
		Detail: auditDetail(map[string]any{
			"groupId": info.GroupID, "imminentPeril": req.ImminentPeril,
			"preempted": decision.PreemptedUserID, "alertId": alert.ID,
		}),
	}); err != nil {
		writeInternal(w, err, "audit emergency call")
		return
	}
	s.broadcastAlert(alert)
	writeJSON(w, http.StatusCreated, map[string]any{
		"call": toSessionDTO(info), "decision": decision, "alert": toAlertDTO(alert),
	})
}

// handleRaiseAlert records the voiceless path: one-tap alert with
// client-reported location, no call. It lands active on every dispatcher's
// rail and clears only on acknowledgement.
func (s *Server) handleRaiseAlert(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var req emergencyCallRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.CallID != "" || req.GroupID != "" {
		writeError(w, http.StatusBadRequest, "voiceless alerts take no call or group")
		return
	}
	alert, err := s.st.CreateAlert(r.Context(), actor.UserID, "", alertKind(req.ImminentPeril), req.Lat, req.Lon, req.Note)
	if mapStoreError(w, err) {
		return
	}
	if err := s.st.Append(r.Context(), store.AuditEntry{
		ActorID: actor.UserID, Action: "emergency.alert.raised",
		TargetType: "alert", TargetID: alert.ID,
		Detail: auditDetail(map[string]any{"imminentPeril": req.ImminentPeril, "located": req.Lat != nil}),
	}); err != nil {
		writeInternal(w, err, "audit alert")
		return
	}
	s.broadcastAlert(alert)
	writeJSON(w, http.StatusCreated, toAlertDTO(alert))
}

// handleListAlerts serves the emergency rail. Dispatchers see every alert
// (active plus archive, filtered by ?status=); everyone else sees only
// their own history — the mobile Alerts screen's data source.
func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	alerts, err := s.st.ListAlerts(r.Context(), r.URL.Query().Get("status"))
	if mapStoreError(w, err) {
		return
	}
	if actor.Role != auth.RoleDispatcher {
		mine := make([]alertDTO, 0, len(alerts))
		for _, a := range alerts {
			if a.UserID == actor.UserID {
				mine = append(mine, toAlertDTO(a))
			}
		}
		writeJSON(w, http.StatusOK, mine)
		return
	}
	out := make([]alertDTO, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, toAlertDTO(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAckAlert closes the acknowledgement loop: an active alert clears
// only on a dispatcher's ack, stamped with who cleared it; re-acking is a
// conflict, not a silent overwrite.
func (s *Server) handleAckAlert(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	alert, err := s.st.AcknowledgeAlert(r.Context(), r.PathValue("id"), actor.UserID)
	if err == store.ErrAlertNotActive {
		writeError(w, http.StatusConflict, "alert already acknowledged")
		return
	}
	if mapStoreError(w, err) {
		return
	}
	if err := s.st.Append(r.Context(), store.AuditEntry{
		ActorID: actor.UserID, Action: "emergency.alert.acknowledged",
		TargetType: "alert", TargetID: alert.ID,
		Detail: auditDetail(map[string]any{"raisedBy": alert.UserID, "callId": alert.CallID}),
	}); err != nil {
		writeInternal(w, err, "audit alert ack")
		return
	}
	msg, err := json.Marshal(protocolAlertAck(alert.ID, actor.UserID))
	if err != nil {
		log.Printf("api: marshal alert ack: %v", err)
	} else {
		s.hub.BroadcastCallEvent(msg)
	}
	writeJSON(w, http.StatusOK, toAlertDTO(alert))
}

// callParticipant reports whether uid is on the live call — the guard for
// escalating an existing call. Unknown calls are not participants.
func (s *Server) callParticipant(r *http.Request, callID, uid string) bool {
	snap, err := s.calls.Snapshot(callID)
	if err != nil {
		return false
	}
	for _, p := range snap.Participants {
		if p == uid {
			return true
		}
	}
	return false
}

// broadcastAlert pushes one alert to every connected client as an
// EmergencyAlert frame — the dispatcher rail's realtime feed.
func (s *Server) broadcastAlert(a store.Alert) {
	msg, err := json.Marshal(protocol.EmergencyAlert{
		Type: protocol.TypeEmergencyAlert, AlertID: a.ID, UserID: a.UserID,
		CallID: a.CallID, Lat: a.Lat, Lon: a.Lon,
		Emergency:     a.Kind == store.AlertKindEmergency,
		ImminentPeril: a.Kind == store.AlertKindImminentPeril,
		Note:          a.Note,
	})
	if err != nil {
		log.Printf("api: marshal alert: %v", err)
		return
	}
	s.hub.BroadcastCallEvent(msg)
}
