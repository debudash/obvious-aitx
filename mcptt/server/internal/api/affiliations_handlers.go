package api

import (
	"net/http"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

type affiliationRequest struct {
	UserID string `json:"userId"` // optional: self by default, dispatcher-only to set
}

// resolveTarget returns the user an affiliation change applies to and
// whether the actor may touch them. Self-service by default; only a
// dispatcher may affiliate or de-affiliate someone else.
func resolveTarget(actor *auth.Identity, requested string) (string, bool) {
	if requested == "" || requested == actor.UserID {
		return actor.UserID, true
	}
	return requested, actor.Role == auth.RoleDispatcher
}

// handleAffiliate marks a user affiliated with a group: one mutation, one
// WSS broadcast, one audit row — the round-trip guarantee the acceptance
// criteria name. Non-dispatchers may only self-affiliate to groups they are
// members of; a dispatcher (net control) may affiliate anyone into anything.
func (s *Server) handleAffiliate(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	groupID := r.PathValue("id")
	var req affiliationRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	targetID, allowed := resolveTarget(actor, req.UserID)
	if !allowed {
		writeError(w, http.StatusForbidden, "only dispatchers may manage other users' affiliations")
		return
	}
	g, err := s.st.GetGroup(r.Context(), groupID)
	if mapStoreError(w, err) {
		return
	}
	if _, err := s.st.GetUserByID(r.Context(), targetID); mapStoreError(w, err) {
		return
	}
	if actor.Role != auth.RoleDispatcher {
		member, err := s.st.IsMember(r.Context(), groupID, targetID)
		if err != nil {
			writeInternal(w, err, "membership check")
			return
		}
		if !member {
			writeError(w, http.StatusForbidden, "membership required to affiliate")
			return
		}
	}
	a, err := s.st.SetAffiliation(r.Context(), targetID, g.ID, store.AffiliationAffiliated)
	if mapStoreError(w, err) {
		return
	}
	s.hub.BroadcastAffiliationChanged(targetID, g.ID, protocol.AffiliationAffiliated)
	s.audit(r, actor, "affiliation.set", "group", g.ID, map[string]any{
		"userId": targetID, "state": store.AffiliationAffiliated,
	})
	writeJSON(w, http.StatusOK, toAffiliationDTO(a))
}

// handleDeaffiliate mirrors affiliate for the de-affiliated state. The state
// write is idempotent; the broadcast and audit row always fire.
func (s *Server) handleDeaffiliate(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	groupID := r.PathValue("id")
	var req affiliationRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	targetID, allowed := resolveTarget(actor, req.UserID)
	if !allowed {
		writeError(w, http.StatusForbidden, "only dispatchers may manage other users' affiliations")
		return
	}
	if _, err := s.st.GetGroup(r.Context(), groupID); mapStoreError(w, err) {
		return
	}
	if _, err := s.st.SetAffiliation(r.Context(), targetID, groupID, store.AffiliationDeaffiliated); mapStoreError(w, err) {
		return
	}
	s.hub.BroadcastAffiliationChanged(targetID, groupID, protocol.AffiliationDeaffiliated)
	s.audit(r, actor, "affiliation.set", "group", groupID, map[string]any{
		"userId": targetID, "state": store.AffiliationDeaffiliated,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleListAffiliations serves affiliation state for the roster.
// Non-dispatchers may list only their own rows.
func (s *Server) handleListAffiliations(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	q := r.URL.Query().Get("userId")
	if q != "" && q != actor.UserID && actor.Role != auth.RoleDispatcher {
		writeError(w, http.StatusForbidden, "only dispatchers may list other users' affiliations")
		return
	}
	rows, err := s.st.ListAffiliations(r.Context(), q)
	if mapStoreError(w, err) {
		return
	}
	dtos := make([]affiliationDTO, 0, len(rows))
	for _, a := range rows {
		dtos = append(dtos, toAffiliationDTO(a))
	}
	writeJSON(w, http.StatusOK, dtos)
}
