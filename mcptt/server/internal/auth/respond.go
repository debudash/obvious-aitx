package auth

import (
	"encoding/json"
	"net/http"
)

// JSON error/responder helpers for the auth middleware. The api package has
// its own richer responder; auth cannot import api (import cycle), so the two
// small writers it needs live here.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="mcptt"`)
	httpError(w, http.StatusUnauthorized, "unauthorized")
}
