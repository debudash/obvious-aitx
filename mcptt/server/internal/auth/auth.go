// Package auth issues and verifies the JWT access tokens every protected
// route and the /ws socket require, and provides the auth middleware.
//
// Claims carry the identity the whole system authorizes on: role
// (dispatcher/supervisor/field) and the 1–10 priority ladder level the floor
// controller will arbitrate with — plus the functional alias ("Squad 3 Lead")
// so clients can render role identities without a second lookup.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Role values mirror store.Role without importing the store layer.
const (
	RoleDispatcher = "dispatcher"
	RoleSupervisor = "supervisor"
	RoleField      = "field"
)

// Claims are the MCPTT access-token claims.
type Claims struct {
	jwt.RegisteredClaims
	Name            string `json:"name"`
	Role            string `json:"role"`
	Priority        int    `json:"priority"`
	FunctionalAlias string `json:"functionalAlias,omitempty"`
}

// Identity is the authenticated principal handlers see via context.
type Identity struct {
	UserID          string
	Username        string
	Role            string
	Priority        int
	FunctionalAlias string
}

// Tokenizer issues and verifies HS256 tokens for one signing secret.
type Tokenizer struct {
	secret []byte
	ttl    time.Duration
	issuer string
}

func NewTokenizer(secret []byte, ttl time.Duration) *Tokenizer {
	return &Tokenizer{secret: secret, ttl: ttl, issuer: "mcptt"}
}

// Issue mint a token for one user. priority must be inside 1–10.
func (t *Tokenizer) Issue(userID, username, role string, priority int, functionalAlias string, now time.Time) (string, error) {
	if priority < 1 || priority > 10 {
		return "", fmt.Errorf("auth: priority %d outside 1–10", priority)
	}
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    t.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
		},
		Name:            username,
		Role:            role,
		Priority:        priority,
		FunctionalAlias: functionalAlias,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// ErrUnauthorized marks every token-rejection path (missing, malformed,
// wrong signature, expired) — the middleware maps it to 401 uniformly.
var ErrUnauthorized = errors.New("auth: unauthorized")

// Verify parses and validates a token, returning its claims.
func (t *Tokenizer) Verify(token string) (Claims, error) {
	var claims Claims
	tok, err := jwt.ParseWithClaims(token, &claims, func(_ *jwt.Token) (any, error) {
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(t.issuer), jwt.WithTimeFunc(time.Now))
	if err != nil || !tok.Valid {
		return Claims{}, ErrUnauthorized
	}
	return claims, nil
}
