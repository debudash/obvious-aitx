package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// decodeJSON reads a strict JSON body with a size cap; on overflow the
// ResponseWriter is flagged so net/http can answer 413 upstream.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

type registerRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	DisplayName     string `json:"displayName"`
	Role            string `json:"role"`
	Priority        *int   `json:"priority"`
	FunctionalAlias string `json:"functionalAlias"`
}

// validate returns every problem with the registration body; the console
// renders the list and the tests assert each deny path.
func (r registerRequest) validate() []string {
	var problems []string
	if !usernameRe.MatchString(r.Username) {
		problems = append(problems, "username must be 3-32 chars of [a-zA-Z0-9_.-]")
	}
	if len(r.Password) < 8 {
		problems = append(problems, "password must be at least 8 characters")
	}
	if r.DisplayName == "" {
		problems = append(problems, "displayName is required")
	}
	if !store.ValidRole(store.Role(r.Role)) {
		problems = append(problems, "role must be dispatcher, supervisor, or field")
	}
	if r.Priority != nil && (*r.Priority < 1 || *r.Priority > 10) {
		problems = append(problems, "priority must be 1-10")
	}
	return problems
}

// handleRegister creates an account and returns its first token.
// Dispatcher-only (route middleware): account minting is net-control work.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var req registerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if problems := req.validate(); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid registration", "problems": problems})
		return
	}

	role := store.Role(req.Role)
	priority := store.DefaultPriority(role)
	if req.Priority != nil {
		priority = *req.Priority
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeInternal(w, err, "bcrypt")
		return
	}

	u, err := s.st.CreateUser(r.Context(), req.Username, string(hash), req.DisplayName, role, priority, req.FunctionalAlias)
	if mapStoreError(w, err) {
		return
	}

	token, err := s.tokens.Issue(u.ID, u.Username, string(u.Role), u.Priority, u.FunctionalAlias, time.Now())
	if err != nil {
		writeInternal(w, err, "issue token")
		return
	}

	s.audit(r, actor, "user.create", "user", u.ID, map[string]any{
		"username": u.Username, "role": string(u.Role), "priority": u.Priority,
	})
	writeJSON(w, http.StatusCreated, map[string]any{"user": toUserDTO(u), "token": token})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin verifies credentials and issues a token. Every failure is the
// same generic 401 — no user enumeration.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	u, err := s.st.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeInternal(w, err, "login lookup")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := s.tokens.Issue(u.ID, u.Username, string(u.Role), u.Priority, u.FunctionalAlias, time.Now())
	if err != nil {
		writeInternal(w, err, "issue token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(u), "token": token})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	u, err := s.st.GetUserByID(r.Context(), id.UserID)
	if mapStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUserByID(r.Context(), r.PathValue("id"))
	if mapStoreError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.st.ListUsers(r.Context())
	if mapStoreError(w, err) {
		return
	}
	dtos := make([]userDTO, 0, len(users))
	for _, u := range users {
		dtos = append(dtos, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, dtos)
}

type updateUserRequest struct {
	DisplayName     *string `json:"displayName"`
	Role            *string `json:"role"`
	Priority        *int    `json:"priority"`
	FunctionalAlias *string `json:"functionalAlias"`
}

// handleUpdateUser is the admin surface for the priority ladder and
// functional-alias assignment (spec: "all configurable per user via the
// console's admin panel").
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	var req updateUserRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	patch := store.UserPatch{
		DisplayName:     req.DisplayName,
		Priority:        req.Priority,
		FunctionalAlias: req.FunctionalAlias,
	}
	if req.Role != nil {
		if !store.ValidRole(store.Role(*req.Role)) {
			writeError(w, http.StatusBadRequest, "role must be dispatcher, supervisor, or field")
			return
		}
		role := store.Role(*req.Role)
		patch.Role = &role
	}
	if req.Priority != nil && (*req.Priority < int(floor.MinLevel) || *req.Priority > int(floor.MaxLevel)) {
		writeError(w, http.StatusBadRequest, "priority must be 1-10")
		return
	}
	u, err := s.st.UpdateUser(r.Context(), r.PathValue("id"), patch)
	if mapStoreError(w, err) {
		return
	}
	s.audit(r, actor, "user.update", "user", u.ID, auditDetailFields(req))
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

// auditDetailFields converts a request struct into audit detail fields.
func auditDetailFields(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("api: marshal audit fields: %v", err)
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		log.Printf("api: unmarshal audit fields: %v", err)
		return map[string]any{}
	}
	return m
}

// audit writes one audit row for an actor's action; failures are logged, not
// swallowed — the mutation stands but the omission is visible in logs.
func (s *Server) audit(r *http.Request, actor *auth.Identity, action, targetType, targetID string, detail map[string]any) {
	actorID := ""
	if actor != nil {
		actorID = actor.UserID
	}
	if err := s.st.Append(r.Context(), store.AuditEntry{
		ActorID: actorID, Action: action, TargetType: targetType, TargetID: targetID, Detail: auditDetail(detail),
	}); err != nil {
		log.Printf("api: audit write failed for %s: %v", action, err)
	}
}
