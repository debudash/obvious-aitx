package auth

import (
	"context"
	"net/http"
	"strings"
)

type contextKey struct{}

// FromContext returns the Identity the middleware injected, or nil.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(*Identity)
	return id, ok
}

// Middleware wires token verification into the HTTP layer.
type Middleware struct {
	tokens *Tokenizer
}

func NewMiddleware(tokens *Tokenizer) *Middleware { return &Middleware{tokens: tokens} }

// RequireAuth rejects requests without a valid bearer token (401) and
// injects the Identity for downstream handlers.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := m.authenticate(r)
		if !ok {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, id)))
	})
}

// RequireRole chains after RequireAuth and rejects identities whose role is
// not in the allowlist (403).
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := FromContext(r.Context())
			if !ok {
				// RequireAuth not applied on this route — a wiring bug; fail closed.
				unauthorized(w)
				return
			}
			if !allowed[id.Role] {
				httpError(w, http.StatusForbidden, "forbidden: requires role "+strings.Join(roles, " or "))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (m *Middleware) authenticate(r *http.Request) (*Identity, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, false
	}
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || token == "" {
		return nil, false
	}
	claims, err := m.tokens.Verify(token)
	if err != nil {
		return nil, false
	}
	return &Identity{
		UserID:          claims.Subject,
		Username:        claims.Name,
		Role:            claims.Role,
		Priority:        claims.Priority,
		FunctionalAlias: claims.FunctionalAlias,
	}, true
}
