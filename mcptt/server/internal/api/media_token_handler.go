package api

import (
	"net/http"
	"slices"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
)

// handleMediaToken mints the room-scoped media token one call participant
// presents with their MediaOffer. This is the mint the wire contract
// assumes ("the room-scoped token minted for the call") but no previous
// route exposed — the delivery gap the console's capability flags and the
// iOS client's deferred media leg both document.
//
// The guard is call participation, not bare group affiliation: the session
// manager already enforced affiliation at join (or party-of for private
// calls), so a live participant is exactly "a user affiliated to the
// call" in the runtime sense — while an affiliated user who never joined
// gets no media leg into a call they are not on. The token binds call and
// user and expires (room-token TTL); it never admits any other room.
func (s *Server) handleMediaToken(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.FromContext(r.Context())
	callID := r.PathValue("id")

	snap, err := s.calls.Snapshot(callID)
	if mapCallError(w, err) {
		return
	}
	if !slices.Contains(snap.Participants, actor.UserID) {
		writeError(w, http.StatusForbidden, "not a participant of this call")
		return
	}
	if s.media == nil || s.media.MintRoomToken == nil {
		writeError(w, http.StatusServiceUnavailable, "media plane unavailable")
		return
	}
	token, err := s.media.MintRoomToken(callID, actor.UserID)
	if err != nil {
		writeInternal(w, err, "mint room token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"callId": callID, "token": token})
}
