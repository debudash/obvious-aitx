// Package api wires the REST + WSS surface: auth, users, groups, membership,
// affiliations, and the signaling hub mount point.
//
// Authorization model: login is public; account creation (register) and all
// group/membership administration are dispatcher-only; affiliation changes
// are self-service or dispatcher; reads are authenticated.
package api

import (
	"net/http"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/ws"
)

// Server holds the collaborators the handlers share.
type Server struct {
	st     *store.Store
	tokens *auth.Tokenizer
	hub    *ws.Handler
}

// New builds the full HTTP handler.
func New(st *store.Store, tokens *auth.Tokenizer, hub *ws.Handler) http.Handler {
	s := &Server{st: st, tokens: tokens, hub: hub}
	mw := auth.NewMiddleware(tokens)

	// chain applies middleware left-to-right around h.
	chain := func(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		return h
	}
	authed := func(h http.HandlerFunc) http.Handler { return mw.RequireAuth(h) }
	admin := func(h http.HandlerFunc) http.Handler {
		return chain(h, mw.RequireAuth, auth.RequireRole(auth.RoleDispatcher))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.Handle("POST /api/auth/register", admin(s.handleRegister))

	mux.Handle("GET /api/users/me", authed(s.handleMe))
	mux.Handle("GET /api/users", authed(s.handleListUsers))
	mux.Handle("GET /api/users/{id}", authed(s.handleGetUser))
	mux.Handle("PATCH /api/users/{id}", admin(s.handleUpdateUser))

	mux.Handle("GET /api/groups", authed(s.handleListGroups))
	mux.Handle("POST /api/groups", admin(s.handleCreateGroup))
	mux.Handle("GET /api/groups/{id}", authed(s.handleGetGroup))
	mux.Handle("PATCH /api/groups/{id}", admin(s.handleUpdateGroup))
	mux.Handle("DELETE /api/groups/{id}", admin(s.handleDeleteGroup))
	mux.Handle("POST /api/groups/{id}/members", admin(s.handleAddMember))
	mux.Handle("DELETE /api/groups/{id}/members/{userID}", admin(s.handleRemoveMember))

	mux.Handle("POST /api/groups/{id}/affiliations", authed(s.handleAffiliate))
	mux.Handle("DELETE /api/groups/{id}/affiliations", authed(s.handleDeaffiliate))
	mux.Handle("GET /api/affiliations", authed(s.handleListAffiliations))

	// The WS handler verifies its token before upgrading.
	mux.Handle("GET /ws", hub)

	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
