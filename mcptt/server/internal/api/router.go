// Package api wires the REST + WSS surface: auth, users, groups, membership,
// affiliations, emergency alerts, dispatcher call control, and the signaling
// hub mount point.
//
// Authorization model: login is public; account creation (register), all
// group/membership administration, dispatcher call control, and alert
// acknowledgement are dispatcher-only; affiliation changes are self-service
// or dispatcher; emergency calls and alerts are any authenticated user;
// reads are authenticated.
package api

import (
	"net/http"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/ws"
)

// Server holds the collaborators the handlers share.
type Server struct {
	st     *store.Store
	tokens *auth.Tokenizer
	hub    *ws.Handler
	calls  *callcontrol.SessionManager
	media  *MediaHooks
}

// NewServer builds the Server with its collaborators exposed. main.go uses
// it to wire the session manager's event sink (s.HandleCallEvent) before
// any traffic flows; tests and other callers use New.
func NewServer(st *store.Store, tokens *auth.Tokenizer, hub *ws.Handler, calls *callcontrol.SessionManager, media *MediaHooks) *Server {
	return &Server{st: st, tokens: tokens, hub: hub, calls: calls, media: media}
}

// New builds the full HTTP handler in one call — the form tests and
// embedding hosts use. Production wiring that needs the event-sink hook
// uses NewServer + Handler (see main.go).
func New(st *store.Store, tokens *auth.Tokenizer, hub *ws.Handler, calls *callcontrol.SessionManager, media *MediaHooks) http.Handler {
	return NewServer(st, tokens, hub, calls, media).Handler()
}

// Handler builds the HTTP mux. Split from construction so main.go can wire
// the session manager's event sink to s.HandleCallEvent first — a Server
// built inside New alone would have no reachable event fan-out.
func (s *Server) Handler() http.Handler {
	mw := auth.NewMiddleware(s.tokens)

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

	// Emergency: the pre-empting call, the voiceless alert, and the
	// dispatcher acknowledgement loop (ack is dispatcher-only).
	mux.Handle("POST /api/calls/emergency", authed(s.handleEmergencyCall))
	mux.Handle("POST /api/emergency/alerts", authed(s.handleRaiseAlert))
	mux.Handle("GET /api/emergency/alerts", authed(s.handleListAlerts))
	mux.Handle("POST /api/emergency/alerts/{id}/ack", admin(s.handleAckAlert))

	// Media admission: the room-scoped token a call participant presents
	// with their MediaOffer. Minted per call and user; short-lived.
	mux.Handle("POST /api/calls/{id}/media-token", authed(s.handleMediaToken))

	// Dispatch control: force-end, revoke the active talker, remove a
	// participant, start a broadcast. All dispatcher-only by the role
	// claim; every action lands in the audit log.
	mux.Handle("POST /api/dispatch/calls/{id}/end", admin(s.handleDispatchEndCall))
	mux.Handle("POST /api/dispatch/calls/{id}/revoke", admin(s.handleDispatchRevoke))
	mux.Handle("DELETE /api/dispatch/calls/{id}/participants/{userId}", admin(s.handleDispatchRemoveParticipant))
	mux.Handle("POST /api/dispatch/broadcast", admin(s.handleDispatchBroadcast))

	// The WS handler verifies its token before upgrading.
	mux.Handle("GET /ws", s.hub)

	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
