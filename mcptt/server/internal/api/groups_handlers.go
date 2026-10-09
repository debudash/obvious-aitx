package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/protocol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

type groupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (r groupRequest) validate() []string {
	var problems []string
	if len(r.Name) < 2 || len(r.Name) > 64 {
		problems = append(problems, "name must be 2-64 characters")
	}
	return problems
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.st.ListGroups(r.Context())
	if mapStoreError(w, err) {
		return
	}
	dtos := make([]groupDTO, 0, len(groups))
	for _, g := range groups {
		dtos = append(dtos, toGroupDTO(g))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// handleCreateGroup makes a group and auto-enrolls its creator as a member
// (dispatchers create groups they can immediately affiliate into). The seed
// script manages memberships explicitly for the demo roster.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var req groupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if problems := req.validate(); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid group", "problems": problems})
		return
	}
	g, err := s.st.CreateGroup(r.Context(), req.Name, req.Description, actor.UserID)
	if mapStoreError(w, err) {
		return
	}
	if _, err := s.st.AddMember(r.Context(), g.ID, actor.UserID); err != nil && !errors.Is(err, store.ErrConflict) {
		// Creator enrollment is a convenience; failure is logged, not fatal.
		log.Printf("api: enroll creator %s in %s: %v", actor.UserID, g.ID, err)
	}
	s.audit(r, actor, "group.create", "group", g.ID, map[string]any{"name": g.Name})
	writeJSON(w, http.StatusCreated, toGroupDTO(g))
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.GetGroup(r.Context(), r.PathValue("id"))
	if mapStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toGroupDTO(g))
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var req groupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	patch := store.GroupPatch{Name: &req.Name, Description: &req.Description}
	g, err := s.st.UpdateGroup(r.Context(), r.PathValue("id"), patch)
	if mapStoreError(w, err) {
		return
	}
	s.audit(r, actor, "group.update", "group", g.ID, auditDetailFields(req))
	writeJSON(w, http.StatusOK, toGroupDTO(g))
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	id := r.PathValue("id")
	if err := s.st.DeleteGroup(r.Context(), id); mapStoreError(w, err) {
		return
	}
	s.audit(r, actor, "group.delete", "group", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

type memberRequest struct {
	UserID string `json:"userId"`
}

func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	groupID := r.PathValue("id")
	var req memberRequest
	if err := decodeJSON(w, r, &req); err != nil || req.UserID == "" {
		writeError(w, http.StatusBadRequest, "userId is required")
		return
	}
	// Validate both foreign rows exist so the client gets 404s, not 500s.
	if _, err := s.st.GetGroup(r.Context(), groupID); mapStoreError(w, err) {
		return
	}
	if _, err := s.st.GetUserByID(r.Context(), req.UserID); mapStoreError(w, err) {
		return
	}
	if _, err := s.st.AddMember(r.Context(), groupID, req.UserID); mapStoreError(w, err) {
		return
	}
	s.audit(r, actor, "membership.add", "group", groupID, map[string]any{"userId": req.UserID})
	writeJSON(w, http.StatusCreated, map[string]any{"groupId": groupID, "userId": req.UserID})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	groupID, userID := r.PathValue("id"), r.PathValue("userID")
	if err := s.st.RemoveMember(r.Context(), groupID, userID); mapStoreError(w, err) {
		return
	}
	// Membership removal cascades the affiliation row (store.RemoveMember);
	// broadcast the de-affiliation so rosters stay live.
	s.hub.BroadcastAffiliationChanged(userID, groupID, protocol.AffiliationDeaffiliated)
	s.audit(r, actor, "membership.remove", "group", groupID, map[string]any{"userId": userID})
	w.WriteHeader(http.StatusNoContent)
}
