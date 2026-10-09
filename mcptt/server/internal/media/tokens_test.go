package media

import (
	"errors"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
)

const testSecret = "test-secret-0123456789abcdef0123456789abcdef"

func newTestRoomTokens() *RoomTokens {
	return NewRoomTokens([]byte(testSecret), time.Minute)
}

func TestRoomTokensRoundTrip(t *testing.T) {
	tokens := newTestRoomTokens()
	now := time.Now()

	tok, err := tokens.Issue("call_1", "alice", now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	callID, userID, err := tokens.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if callID != "call_1" || userID != "alice" {
		t.Errorf("verify = (%q, %q), want (call_1, alice)", callID, userID)
	}
}

func TestRoomTokensRejectExpired(t *testing.T) {
	tokens := newTestRoomTokens()
	tok, err := tokens.Issue("call_1", "alice", time.Now().Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, _, err := tokens.Verify(tok); !errors.Is(err, ErrRoomTokenInvalid) {
		t.Errorf("expired token verify err = %v, want ErrRoomTokenInvalid", err)
	}
}

func TestRoomTokensRejectWrongSecret(t *testing.T) {
	tok, err := newTestRoomTokens().Issue("call_1", "alice", time.Now())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	other := NewRoomTokens([]byte("another-secret-0123456789abcdef012345"), time.Minute)
	if _, _, err := other.Verify(tok); !errors.Is(err, ErrRoomTokenInvalid) {
		t.Errorf("wrong-secret verify err = %v, want ErrRoomTokenInvalid", err)
	}
}

func TestRoomTokensRejectGarbage(t *testing.T) {
	if _, _, err := newTestRoomTokens().Verify("not-a-token"); !errors.Is(err, ErrRoomTokenInvalid) {
		t.Errorf("garbage verify err = %v, want ErrRoomTokenInvalid", err)
	}
}

func TestRoomTokensIssueRejectsEmptyBinding(t *testing.T) {
	tokens := newTestRoomTokens()
	if _, err := tokens.Issue("", "alice", time.Now()); err == nil {
		t.Error("Issue without callID must fail")
	}
	if _, err := tokens.Issue("call_1", "", time.Now()); err == nil {
		t.Error("Issue without userID must fail")
	}
}

// TestAccessTokenIsNotARoomToken pins the claim-set separation: an access
// token from the auth package must never be accepted by the media plane,
// and vice versa (the issuers differ).
func TestAccessTokenIsNotARoomToken(t *testing.T) {
	now := time.Now()
	access := auth.NewTokenizer([]byte(testSecret), time.Hour)
	room := newTestRoomTokens()

	accessTok, err := access.Issue("alice", "Alice", auth.RoleField, 4, "", now)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}
	if _, _, err := room.Verify(accessTok); !errors.Is(err, ErrRoomTokenInvalid) {
		t.Errorf("media accepted an access token: err = %v", err)
	}

	roomTok, err := room.Issue("call_1", "alice", now)
	if err != nil {
		t.Fatalf("issue room: %v", err)
	}
	if _, err := access.Verify(roomTok); err == nil {
		t.Error("auth accepted a room token")
	}
}

// TestSFURejectsMismatchedRoomToken pins the binding check: a valid token
// for another call or user must not admit anyone.
func TestSFURejectsMismatchedRoomToken(t *testing.T) {
	tokens := newTestRoomTokens()
	sfu := NewSFU(NewGate(), tokens)

	tokA, err := tokens.Issue("call_1", "alice", time.Now())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := sfu.authorize("call_2", "alice", tokA); !errors.Is(err, ErrRoomTokenInvalid) {
		t.Errorf("token for call_1 admitted call_2: err = %v", err)
	}
	if err := sfu.authorize("call_1", "bob", tokA); !errors.Is(err, ErrRoomTokenInvalid) {
		t.Errorf("alice's token admitted bob: err = %v", err)
	}
	if err := sfu.authorize("call_1", "alice", tokA); err != nil {
		t.Errorf("matching token rejected: %v", err)
	}
}
