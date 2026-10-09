package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

const testSecret = "0123456789abcdef0123456789abcdef"

func newTokenizer() *Tokenizer {
	return NewTokenizer([]byte(testSecret), time.Hour)
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	tk := newTokenizer()
	now := time.Now()
	tok, err := tk.Issue("u1", "bravo_2", "field", 5, "Bravo 2", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := tk.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "u1" || claims.Name != "bravo_2" || claims.Role != RoleField ||
		claims.Priority != 5 || claims.FunctionalAlias != "Bravo 2" {
		t.Errorf("claims mismatch: %+v", claims)
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != now.Add(time.Hour).Unix() {
		// NumericDate truncates to whole seconds; compare at that grain.
		t.Errorf("ExpiresAt = %v, want issued+1h", claims.ExpiresAt)
	}
}

func TestIssueRejectsPriorityOutsideLadder(t *testing.T) {
	for _, p := range []int{0, 11, -1} {
		if _, err := newTokenizer().Issue("u1", "x", "field", p, "", testNow); err == nil {
			t.Errorf("Issue(priority %d) = nil error, want error", p)
		}
	}
}

func TestVerifyRejections(t *testing.T) {
	tk := newTokenizer()
	valid, err := tk.Issue("u1", "x", "field", 5, "", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Expired: token from a tokenizer whose ttl already elapsed.
	expiredTk := NewTokenizer([]byte(testSecret), -time.Minute)
	expired, err := expiredTk.Issue("u1", "x", "field", 5, "", testNow)
	if err != nil {
		t.Fatalf("Issue expired: %v", err)
	}

	// Wrong secret: same claims signed by another key.
	otherTk := NewTokenizer([]byte("99999999999999999999999999999999"), time.Hour)
	wrongSecret, err := otherTk.Issue("u1", "x", "field", 5, "", time.Now())
	if err != nil {
		t.Fatalf("Issue wrong-secret: %v", err)
	}

	// Right secret, disallowed algorithm (HS512): must be refused by
	// the WithValidMethods allowlist.
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    "mcptt",
		Subject:   "u1",
		IssuedAt:  jwt.NewNumericDate(testNow),
		ExpiresAt: jwt.NewNumericDate(testNow.Add(time.Hour)),
	}, Role: "dispatcher", Priority: 10}
	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign hs512: %v", err)
	}

	cases := map[string]string{
		"expired":      expired,
		"wrong secret": wrongSecret,
		"hs512 alg":    hs512,
		"garbage":      "not-a-token",
		"empty":        "",
	}
	for name, tok := range cases {
		if _, err := tk.Verify(tok); err != ErrUnauthorized {
			t.Errorf("Verify(%s) = %v, want ErrUnauthorized", name, err)
		}
	}
	if _, err := tk.Verify(valid); err != nil {
		t.Errorf("Verify(valid) = %v, want nil", err)
	}
}

// TestRequireAuthMiddleware exercises the 401 paths and the identity
// injection of RequireAuth.
func TestRequireAuthMiddleware(t *testing.T) {
	tk := newTokenizer()
	mw := NewMiddleware(tk)

	var got *Identity
	protected := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := FromContext(r.Context())
		if ok {
			got = id
		}
		w.WriteHeader(http.StatusOK)
	}))

	valid, err := tk.Issue("u1", "bravo_2", "field", 5, "Bravo 2", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"bad scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"bare token", valid, http.StatusUnauthorized},
		{"garbage token", "Bearer garbage", http.StatusUnauthorized},
		{"valid", "Bearer " + valid, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/anything", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			protected.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	// Identity content on the happy path.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+valid)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if got == nil || got.UserID != "u1" || got.Role != "field" || got.Priority != 5 || got.FunctionalAlias != "Bravo 2" {
		t.Errorf("identity = %+v", got)
	}
}

// TestRequireRoleMiddleware verifies the 403 path and that a missing
// identity (RequireAuth not applied) fails closed.
func TestRequireRoleMiddleware(t *testing.T) {
	tk := newTokenizer()
	mw := NewMiddleware(tk)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	guarded := mw.RequireAuth(RequireRole(RoleDispatcher)(ok))
	unguarded := RequireRole(RoleDispatcher)(ok)

	fieldTok, err := tk.Issue("u1", "x", RoleField, 5, "", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	dispatchTok, err := tk.Issue("u2", "y", RoleDispatcher, 10, "", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// No RequireAuth upstream → no identity → fail closed 401.
	rec := httptest.NewRecorder()
	unguarded.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unguarded route status = %d, want 401 (fail closed)", rec.Code)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+fieldTok)
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("field on dispatcher route = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+dispatchTok)
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("dispatcher on dispatcher route = %d, want 200", rec.Code)
	}
}
