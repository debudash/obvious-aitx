package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already written; log so the failure is visible.
		log.Printf("api: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// mapStoreError translates store sentinel errors into HTTP statuses.
func mapStoreError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict")
	default:
		log.Printf("api: store error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}

// writeInternal surfaces unexpected handler failures as 500 — never a silent
// catch. Handlers return after calling it.
func writeInternal(w http.ResponseWriter, err error, where string) {
	log.Printf("api: %s: %v", where, err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// auditDetail marshals audit detail; audit failures are logged loudly but do
// not fail the mutation they observed (the mutation is already durable).
func auditDetail(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil {
		log.Printf("api: marshal audit detail: %v", err)
		return "{}"
	}
	return string(b)
}
