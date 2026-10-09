package api

import (
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/ws"
)

// HandleCallEvent fans one call-control event out to every connected
// client. Wired as the SessionManager's OnEvent at startup; the manager
// emits outside its own lock, so calling back is safe. The event→frame
// encoding lives in the wire package (ws.RenderCallEvent) so the dispatch
// path and this event path render identically.
func (s *Server) HandleCallEvent(e callcontrol.Event) {
	for _, msg := range ws.RenderCallEvent(e) {
		s.hub.BroadcastCallEvent(msg)
	}
}
