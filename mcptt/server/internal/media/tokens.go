package media

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultRoomTokenTTL bounds a room-scoped media token. Short by design —
// the token admits one user to one call's media room, so a leaked token's
// useful life is minutes, not sessions.
const DefaultRoomTokenTTL = 5 * time.Minute

// tokenIssuer distinguishes room tokens from the access tokens minted by
// the auth package: same HS256 secret (the server holds exactly one), a
// different issuer and claim set, so an access token can never join a room
// and a room token can never authenticate a route.
const tokenIssuer = "mcptt-media"

// roomClaims are the media-plane token claims: which call, which user.
type roomClaims struct {
	jwt.RegisteredClaims
	CallID string `json:"callId"`
}

// RoomTokens issues and verifies the short-lived, room-scoped tokens the
// control plane mints per call (spec: "short-lived room-scoped tokens
// minted per call"). The control plane hands a token to a participant when
// it admits them to the call; the SFU refuses any offer that does not
// carry one binding the call and the authenticated user.
type RoomTokens struct {
	secret []byte
	ttl    time.Duration
}

// NewRoomTokens builds a token mint/verify pair for one signing secret.
func NewRoomTokens(secret []byte, ttl time.Duration) *RoomTokens {
	return &RoomTokens{secret: secret, ttl: ttl}
}

// ErrRoomTokenInvalid marks every room-token rejection path (missing,
// malformed, wrong signature, expired).
var ErrRoomTokenInvalid = errors.New("media: room token invalid")

// Issue mints a room token binding userID to callID.
func (t *RoomTokens) Issue(callID, userID string, now time.Time) (string, error) {
	if callID == "" || userID == "" {
		return "", errors.New("media: room token needs a call and a user")
	}
	claims := roomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
		},
		CallID: callID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// Verify parses and validates a room token, returning the call and user it
// binds. Callers must additionally check the pair matches the connection's
// identity and the call being joined — a valid token for another call or
// user must not admit anyone.
func (t *RoomTokens) Verify(token string) (callID, userID string, err error) {
	var claims roomClaims
	tok, err := jwt.ParseWithClaims(token, &claims, func(_ *jwt.Token) (any, error) {
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(tokenIssuer), jwt.WithTimeFunc(time.Now))
	if err != nil || !tok.Valid {
		return "", "", ErrRoomTokenInvalid
	}
	if claims.CallID == "" || claims.Subject == "" {
		return "", "", fmt.Errorf("%w: token missing call or user", ErrRoomTokenInvalid)
	}
	return claims.CallID, claims.Subject, nil
}
