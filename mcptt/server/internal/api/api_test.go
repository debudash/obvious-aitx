package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/ws"
)

// ---------------------------------------------------------------------------
// Harness

const testSecret = "0123456789abcdef0123456789abcdef"

type env struct {
	st     *store.Store
	tokens *auth.Tokenizer
	ts     *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tokens := auth.NewTokenizer([]byte(testSecret), time.Hour)
	hub := ws.NewHandler(ws.NewHub(), tokens)
	e := &env{st: st, tokens: tokens}
	e.ts = httptest.NewServer(New(st, tokens, hub, nil, nil))
	t.Cleanup(e.ts.Close)
	return e
}

// user creates an account directly in the store (the API has no public
// account creation by design — register is dispatcher-only).
func (e *env) user(t *testing.T, username string, role store.Role, priority int, alias string) store.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("password-"+username), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	u, err := e.st.CreateUser(context.Background(), username, string(hash), "Display "+username, role, priority, alias)
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

// do runs one request against the server and returns the response plus the
// decoded JSON body (nil for 204s).
func (e *env) do(t *testing.T, method, path, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	switch b := body.(type) {
	case nil:
		reader = strings.NewReader("")
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	var out map[string]any
	if res.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil && res.ContentLength != 0 {
			// Non-JSON or empty bodies are fine; only decode failures on
			// promised JSON are bugs, and callers assert on status first.
			out = nil
		}
	}
	return res, out
}

// doList is do for endpoints that answer with a JSON array.
func (e *env) doList(t *testing.T, method, path, token string) []any {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var out []any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: decode array: %v", method, path, err)
	}
	return out
}

// login mints a token through the real endpoint — every authed test goes
// through the same door a real client does.
func (e *env) login(t *testing.T, username, password string) string {
	t.Helper()
	res, body := e.do(t, "POST", "/api/auth/login", "", map[string]any{
		"username": username, "password": password,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login %s: status %d body %v", username, res.StatusCode, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("login %s: no token in response", username)
	}
	return token
}

func (e *env) loginAs(t *testing.T, u store.User) string {
	t.Helper()
	return e.login(t, u.Username, "password-"+u.Username)
}

// group makes a group with the dispatcher and enrolls one member.
func (e *env) group(t *testing.T, name string, dispatcher store.User, members ...store.User) store.Group {
	t.Helper()
	token := e.loginAs(t, dispatcher)
	res, body := e.do(t, "POST", "/api/groups", token, map[string]any{"name": name, "description": name + " group"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create group %s: status %d body %v", name, res.StatusCode, body)
	}
	var g store.Group
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decode group: %v", err)
	}
	for _, m := range members {
		res, body := e.do(t, "POST", "/api/groups/"+g.ID+"/members", token, map[string]any{"userId": m.ID})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("add member: status %d body %v", res.StatusCode, body)
		}
	}
	return g
}

// ---------------------------------------------------------------------------
// Open routes

func TestHealthzOpen(t *testing.T) {
	e := newEnv(t)
	res, body := e.do(t, "GET", "/healthz", "", nil)
	if res.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz: status %d body %v", res.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// Login / register: the JWT-claims acceptance row

func TestLoginIssuesJWTWithClaims(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "Dispatch 1")

	token := e.login(t, d.Username, "password-"+d.Username)
	claims, err := e.tokens.Verify(token)
	if err != nil {
		t.Fatalf("verify issued token: %v", err)
	}
	if claims.Subject != d.ID {
		t.Errorf("claims.Subject = %q, want user id %q", claims.Subject, d.ID)
	}
	if claims.Role != string(store.RoleDispatcher) {
		t.Errorf("claims.Role = %q, want dispatcher", claims.Role)
	}
	if claims.Priority != 10 {
		t.Errorf("claims.Priority = %d, want 10", claims.Priority)
	}
	if claims.Name != d.Username {
		t.Errorf("claims.Name = %q, want %q", claims.Name, d.Username)
	}
	if claims.FunctionalAlias != "Dispatch 1" {
		t.Errorf("claims.FunctionalAlias = %q, want functional alias carried", claims.FunctionalAlias)
	}
}

func TestLoginDenyPaths(t *testing.T) {
	e := newEnv(t)
	e.user(t, "field_1", store.RoleField, 5, "")

	cases := []struct {
		name     string
		username string
		password string
		want     int
	}{
		{"wrong password", "field_1", "nope-nope-nope", http.StatusUnauthorized},
		{"unknown user", "ghost", "whatever-password", http.StatusUnauthorized},
		{"empty credentials", "", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := e.do(t, "POST", "/api/auth/login", "", map[string]any{
				"username": tc.username, "password": tc.password,
			})
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (body %v)", res.StatusCode, tc.want, body)
			}
		})
	}
	// Unknown user and wrong password must be indistinguishable.
	res1, _ := e.do(t, "POST", "/api/auth/login", "", map[string]any{"username": "field_1", "password": "nope-nope-nope"})
	res2, _ := e.do(t, "POST", "/api/auth/login", "", map[string]any{"username": "ghost", "password": "nope-nope-nope"})
	if res1.StatusCode != res2.StatusCode {
		t.Errorf("credential failures differ: %d vs %d (user enumeration)", res1.StatusCode, res2.StatusCode)
	}
}

func TestRegisterDenyPaths(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	f := e.user(t, "field_1", store.RoleField, 5, "")
	s := e.user(t, "super_1", store.RoleSupervisor, 8, "")
	dt, ft, st_ := e.loginAs(t, d), e.loginAs(t, f), e.loginAs(t, s)

	cases := []struct {
		name   string
		token  string
		body   map[string]any
		status int
	}{
		{"unauthenticated", "", map[string]any{"username": "x1", "password": "long-enough-pw", "displayName": "X", "role": "field"}, http.StatusUnauthorized},
		{"field token", ft, map[string]any{"username": "x2", "password": "long-enough-pw", "displayName": "X", "role": "field"}, http.StatusForbidden},
		{"supervisor token", st_, map[string]any{"username": "x3", "password": "long-enough-pw", "displayName": "X", "role": "field"}, http.StatusForbidden},
		{"short password", dt, map[string]any{"username": "x4", "password": "short", "displayName": "X", "role": "field"}, http.StatusBadRequest},
		{"bad username", dt, map[string]any{"username": "no", "password": "long-enough-pw", "displayName": "X", "role": "field"}, http.StatusBadRequest},
		{"bad role", dt, map[string]any{"username": "x5", "password": "long-enough-pw", "displayName": "X", "role": "admin"}, http.StatusBadRequest},
		{"priority 11", dt, map[string]any{"username": "x6", "password": "long-enough-pw", "displayName": "X", "role": "field", "priority": 11}, http.StatusBadRequest},
		{"priority 0", dt, map[string]any{"username": "x7", "password": "long-enough-pw", "displayName": "X", "role": "field", "priority": 0}, http.StatusBadRequest},
		{"missing displayName", dt, map[string]any{"username": "x8", "password": "long-enough-pw", "role": "field"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := e.do(t, "POST", "/api/auth/register", tc.token, tc.body)
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d (body %v)", res.StatusCode, tc.status, body)
			}
		})
	}

	// Duplicate username → conflict.
	e.do(t, "POST", "/api/auth/register", dt, map[string]any{"username": "field_1", "password": "long-enough-pw", "displayName": "Dup", "role": "field"})
	res, _ := e.do(t, "POST", "/api/auth/register", dt, map[string]any{"username": "field_1", "password": "long-enough-pw", "displayName": "Dup", "role": "field"})
	if res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate username status = %d, want 409", res.StatusCode)
	}
}

func TestRegisterHappyPath(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	dt := e.loginAs(t, d)

	res, body := e.do(t, "POST", "/api/auth/register", dt, map[string]any{
		"username": "new_field", "password": "long-enough-pw", "displayName": "New Field", "role": "field", "priority": 6,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201 (body %v)", res.StatusCode, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("no token in register response")
	}
	claims, err := e.tokens.Verify(token)
	if err != nil {
		t.Fatalf("verify register token: %v", err)
	}
	if claims.Role != string(store.RoleField) || claims.Priority != 6 {
		t.Errorf("register claims = role %q priority %d, want field/6", claims.Role, claims.Priority)
	}
	// The new account can log in with its password.
	e.login(t, "new_field", "long-enough-pw")
}

// ---------------------------------------------------------------------------
// Deny-path matrix: EVERY protected route, unauthenticated and wrong-role.

// authedRoutes are routes any authenticated role may call (status is route
// dependent once authed, so the table only asserts the unauthenticated deny).
var authedRoutes = []struct{ method, path string }{
	{"GET", "/api/users/me"},
	{"GET", "/api/users"},
	{"GET", "/api/users/some-id"},
	{"PATCH", "/api/users/some-id"},
	{"GET", "/api/groups"},
	{"POST", "/api/groups"},
	{"GET", "/api/groups/some-id"},
	{"PATCH", "/api/groups/some-id"},
	{"DELETE", "/api/groups/some-id"},
	{"POST", "/api/groups/some-id/members"},
	{"DELETE", "/api/groups/some-id/members/some-user"},
	{"POST", "/api/groups/some-id/affiliations"},
	{"DELETE", "/api/groups/some-id/affiliations"},
	{"GET", "/api/affiliations"},
}

func TestEveryProtectedRouteDeniesUnauthenticated(t *testing.T) {
	e := newEnv(t)
	for _, route := range authedRoutes {
		res, _ := e.do(t, route.method, route.path, "", nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", route.method, route.path, res.StatusCode)
		}
	}
}

func TestAdminRoutesDenyNonDispatchers(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	f := e.user(t, "field_1", store.RoleField, 5, "")
	s := e.user(t, "super_1", store.RoleSupervisor, 8, "")
	ft, st_ := e.loginAs(t, f), e.loginAs(t, s)
	g := e.group(t, "TAC-1", d, f)

	adminRoutes := []struct{ method, path string }{
		{"POST", "/api/auth/register"},
		{"PATCH", "/api/users/" + f.ID},
		{"POST", "/api/groups"},
		{"PATCH", "/api/groups/" + g.ID},
		{"DELETE", "/api/groups/" + g.ID},
		{"POST", "/api/groups/" + g.ID + "/members"},
		{"DELETE", "/api/groups/" + g.ID + "/members/" + f.ID},
	}
	for _, route := range adminRoutes {
		for name, token := range map[string]string{"field": ft, "supervisor": st_} {
			res, _ := e.do(t, route.method, route.path, token, map[string]any{"userId": f.ID, "name": "X", "description": "x"})
			if res.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d, want 403", route.method, route.path, name, res.StatusCode)
			}
		}
	}
}

func TestWSRejectsUnauthenticated(t *testing.T) {
	e := newEnv(t)
	url := "ws://" + strings.TrimPrefix(e.ts.URL, "http://") + "/ws"

	// Missing token.
	_, res, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("dial without token: expected error")
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing token status = %d, want 401", res.StatusCode)
	}

	// Garbage token.
	_, res, err = websocket.DefaultDialer.Dial(url+"?token=not-a-jwt", nil)
	if err == nil {
		t.Fatal("dial with garbage token: expected error")
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage token status = %d, want 401", res.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Groups, membership, affiliation

func TestGroupLifecycle(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	dt := e.loginAs(t, d)

	res, body := e.do(t, "POST", "/api/groups", dt, map[string]any{"name": "TAC-1", "description": "tac one"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201 (body %v)", res.StatusCode, body)
	}
	gid, _ := body["id"].(string)
	if gid == "" {
		t.Fatal("no group id in create response")
	}

	// Creator auto-enrolled as member.
	res, _ = e.do(t, "GET", "/api/groups/"+gid, dt, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get = %d, want 200", res.StatusCode)
	}

	// Duplicate name → conflict.
	res, _ = e.do(t, "POST", "/api/groups", dt, map[string]any{"name": "TAC-1"})
	if res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate group name = %d, want 409", res.StatusCode)
	}

	// Bad name → 400.
	res, _ = e.do(t, "POST", "/api/groups", dt, map[string]any{"name": "x"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("short group name = %d, want 400", res.StatusCode)
	}

	// Update.
	res, _ = e.do(t, "PATCH", "/api/groups/"+gid, dt, map[string]any{"name": "TAC-1-renamed", "description": "renamed"})
	if res.StatusCode != http.StatusOK {
		t.Errorf("patch group = %d, want 200", res.StatusCode)
	}

	// List includes it.
	found := false
	for _, item := range e.doList(t, "GET", "/api/groups", dt) {
		if m, ok := item.(map[string]any); ok && m["id"] == gid {
			found = true
		}
	}
	if !found {
		t.Error("group missing from list after create")
	}

	// Delete → 204, then 404.
	res, _ = e.do(t, "DELETE", "/api/groups/"+gid, dt, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d, want 204", res.StatusCode)
	}
	res, _ = e.do(t, "GET", "/api/groups/"+gid, dt, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", res.StatusCode)
	}
}

func TestMembershipRules(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	f := e.user(t, "field_1", store.RoleField, 5, "")
	dt := e.loginAs(t, d)
	g := e.group(t, "TAC-1", d)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		status int
	}{
		{"unknown user", "POST", "/api/groups/" + g.ID + "/members", map[string]any{"userId": "nope"}, http.StatusNotFound},
		{"unknown group", "POST", "/api/groups/nope/members", map[string]any{"userId": f.ID}, http.StatusNotFound},
		{"empty body", "POST", "/api/groups/" + g.ID + "/members", map[string]any{}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := e.do(t, tc.method, tc.path, dt, tc.body)
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
		})
	}

	// Add works, duplicate conflicts, remove works, remove again 404.
	res, _ := e.do(t, "POST", "/api/groups/"+g.ID+"/members", dt, map[string]any{"userId": f.ID})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add member = %d, want 201", res.StatusCode)
	}
	res, _ = e.do(t, "POST", "/api/groups/"+g.ID+"/members", dt, map[string]any{"userId": f.ID})
	if res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate member = %d, want 409", res.StatusCode)
	}
	res, _ = e.do(t, "DELETE", "/api/groups/"+g.ID+"/members/"+f.ID, dt, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("remove member = %d, want 204", res.StatusCode)
	}
	res, _ = e.do(t, "DELETE", "/api/groups/"+g.ID+"/members/"+f.ID, dt, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("remove missing member = %d, want 404", res.StatusCode)
	}
}

func TestAffiliationRules(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	f := e.user(t, "field_1", store.RoleField, 5, "")
	other := e.user(t, "field_2", store.RoleField, 5, "")
	dt, ft := e.loginAs(t, d), e.loginAs(t, f)
	g := e.group(t, "TAC-1", d, f) // f is a member; other is not

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		body   any
		status int
	}{
		{"member self-affiliates", "POST", "/api/groups/" + g.ID + "/affiliations", ft, map[string]any{}, http.StatusOK},
		{"non-member self-affiliates", "POST", "/api/groups/" + g.ID + "/affiliations", e.loginAs(t, other), map[string]any{}, http.StatusForbidden},
		{"field affiliates another user", "POST", "/api/groups/" + g.ID + "/affiliations", ft, map[string]any{"userId": other.ID}, http.StatusForbidden},
		{"unknown group", "POST", "/api/groups/nope/affiliations", ft, map[string]any{}, http.StatusNotFound},
		{"list others as field", "GET", "/api/affiliations?userId=" + other.ID, ft, nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := e.do(t, tc.method, tc.path, tc.token, tc.body)
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
		})
	}

	// Dispatcher affiliates someone else (net control).
	res, body := e.do(t, "POST", "/api/groups/"+g.ID+"/affiliations", dt, map[string]any{"userId": other.ID})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dispatcher affiliate = %d, want 200 (body %v)", res.StatusCode, body)
	}
	if body["state"] != store.AffiliationAffiliated {
		t.Errorf("affiliated state = %v, want %q", body["state"], store.AffiliationAffiliated)
	}

	// Self de-affiliate → 204; state flips; dispatcher can de-affiliate others.
	res, _ = e.do(t, "DELETE", "/api/groups/"+g.ID+"/affiliations", ft, map[string]any{})
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("self deaffiliate = %d, want 204", res.StatusCode)
	}
	a, err := e.st.AffiliationState(context.Background(), f.ID, g.ID)
	if err != nil {
		t.Fatalf("affiliation state: %v", err)
	}
	if a.State != store.AffiliationDeaffiliated {
		t.Errorf("state after deaffiliate = %q, want %q", a.State, store.AffiliationDeaffiliated)
	}
	res, _ = e.do(t, "DELETE", "/api/groups/"+g.ID+"/affiliations", dt, map[string]any{"userId": other.ID})
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("dispatcher deaffiliate other = %d, want 204", res.StatusCode)
	}

	// Dispatcher may list everyone; field lists self only.
	res, _ = e.do(t, "GET", "/api/affiliations?userId="+other.ID, dt, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("dispatcher list other = %d, want 200", res.StatusCode)
	}
	res, _ = e.do(t, "GET", "/api/affiliations", ft, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("self list = %d, want 200", res.StatusCode)
	}
}

// TestAffiliationBroadcastRoundTrip is the acceptance-criterion test: an
// affiliation change reaches every connected client within one round trip.
func TestAffiliationBroadcastRoundTrip(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	f := e.user(t, "field_1", store.RoleField, 5, "")
	dt := e.loginAs(t, d)
	g := e.group(t, "TAC-1", d, f)

	// A witness socket (another field user) and the subject's socket.
	witness := e.user(t, "field_2", store.RoleField, 5, "")
	wconn := dialWS(t, e.ts.URL, e.loginAs(t, witness))
	defer wconn.Close()
	fconn := dialWS(t, e.ts.URL, e.loginAs(t, f))
	defer fconn.Close()

	res, _ := e.do(t, "POST", "/api/groups/"+g.ID+"/affiliations", dt, map[string]any{"userId": f.ID})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("affiliate = %d", res.StatusCode)
	}

	for name, conn := range map[string]*websocket.Conn{"witness": wconn, "subject": fconn} {
		msg := readUntil(t, conn, protocolTypeAffiliation)
		if msg["userId"] != f.ID || msg["groupId"] != g.ID || msg["state"] != store.AffiliationAffiliated {
			t.Errorf("%s got %v, want AffiliationChanged(%s, %s, affiliated)", name, msg, f.ID, g.ID)
		}
	}

	// Membership removal cascades a de-affiliation broadcast.
	res, _ = e.do(t, "DELETE", "/api/groups/"+g.ID+"/members/"+f.ID, dt, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove member = %d", res.StatusCode)
	}
	msg := readUntil(t, wconn, protocolTypeAffiliation)
	if msg["userId"] != f.ID || msg["state"] != store.AffiliationDeaffiliated {
		t.Errorf("removal broadcast = %v, want deaffiliated for %s", msg, f.ID)
	}
}

const protocolTypeAffiliation = "AffiliationChanged"

// dialWS connects a real websocket client with a token query param.
func dialWS(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()
	url := "ws://" + strings.TrimPrefix(serverURL, "http://") + "/ws?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readUntil reads with a deadline until a message of the wanted type arrives,
// skipping the presence noise in between.
func readUntil(t *testing.T, conn *websocket.Conn, msgType string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no %s message within 2s", msgType)
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		_, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(p, &m); err != nil {
			t.Fatalf("unmarshal %q: %v", p, err)
		}
		if m["type"] == msgType {
			return m
		}
	}
}

// ---------------------------------------------------------------------------
// Presence + audit

func TestPresenceBroadcastOnConnectAndDisconnect(t *testing.T) {
	e := newEnv(t)
	f := e.user(t, "field_1", store.RoleField, 5, "")
	witness := e.user(t, "field_2", store.RoleField, 5, "")

	wconn := dialWS(t, e.ts.URL, e.loginAs(t, witness))
	defer wconn.Close()

	// The witness's own connect also emits presence; filter to the subject.
	fconn := dialWS(t, e.ts.URL, e.loginAs(t, f))
	msg := readPresence(t, wconn, f.ID, "online")
	if msg["userId"] != f.ID || msg["state"] != "online" {
		t.Errorf("online presence = %v, want %s online", msg, f.Username)
	}

	_ = fconn.Close()
	msg = readPresence(t, wconn, f.ID, "offline")
	if msg["userId"] != f.ID || msg["state"] != "offline" {
		t.Errorf("offline presence = %v, want %s offline", msg, f.Username)
	}
}

// readPresence skips unrelated traffic until one user's presence flips to
// the wanted state, within a 2s deadline.
func readPresence(t *testing.T, conn *websocket.Conn, userID, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no PresenceUpdate(%s, %s) within 2s", userID, state)
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		_, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(p, &m); err != nil {
			t.Fatalf("unmarshal %q: %v", p, err)
		}
		if m["type"] == "PresenceUpdate" && m["userId"] == userID && m["state"] == state {
			return m
		}
	}
}

func TestAuditRowsWritten(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "")
	f := e.user(t, "field_1", store.RoleField, 5, "")
	dt := e.loginAs(t, d)
	g := e.group(t, "TAC-1", d, f)

	_, _ = e.do(t, "POST", "/api/groups/"+g.ID+"/affiliations", dt, map[string]any{"userId": f.ID})
	_, _ = e.do(t, "PATCH", "/api/users/"+f.ID, dt, map[string]any{"priority": 6})

	rows, err := e.st.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Action] = true
	}
	for _, want := range []string{"group.create", "membership.add", "affiliation.set", "user.update"} {
		if !got[want] {
			t.Errorf("audit log missing %q (rows: %d)", want, len(rows))
		}
	}
	_ = g
}

// ---------------------------------------------------------------------------
// Users read surface

func TestUserReadEndpoints(t *testing.T) {
	e := newEnv(t)
	d := e.user(t, "dispatch_1", store.RoleDispatcher, 10, "Dispatch 1")
	f := e.user(t, "field_1", store.RoleField, 5, "Bravo 2")
	ft, dt := e.loginAs(t, f), e.loginAs(t, d)

	res, me := e.do(t, "GET", "/api/users/me", ft, nil)
	if res.StatusCode != http.StatusOK || me["username"] != f.Username {
		t.Fatalf("me = %d %v, want 200 %s", res.StatusCode, me, f.Username)
	}
	if me["id"] != f.ID {
		t.Errorf("me.id = %v, want %s (must be the store id, not the token subject leak)", me["id"], f.ID)
	}
	res, _ = e.do(t, "GET", "/api/users/"+f.ID, dt, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("get user = %d, want 200", res.StatusCode)
	}
	res, _ = e.do(t, "GET", "/api/users/nope", dt, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("get unknown user = %d, want 404", res.StatusCode)
	}
	users := e.doList(t, "GET", "/api/users", ft)
	if len(users) != 2 {
		t.Errorf("list users = %d entries, want 2 (no password hash fields)", len(users))
	}

	// Update: alias + priority, then deny invalid.
	res, body := e.do(t, "PATCH", "/api/users/"+f.ID, dt, map[string]any{"priority": 6, "functionalAlias": "Bravo 2 Actual"})
	if res.StatusCode != http.StatusOK || body["priority"] != float64(6) {
		t.Errorf("patch user = %d %v, want 200 priority 6", res.StatusCode, body)
	}
	res, _ = e.do(t, "PATCH", "/api/users/"+f.ID, dt, map[string]any{"priority": 11})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("patch priority 11 = %d, want 400", res.StatusCode)
	}
	res, _ = e.do(t, "PATCH", "/api/users/"+f.ID, dt, map[string]any{"role": "admin"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("patch bad role = %d, want 400", res.StatusCode)
	}
	res, _ = e.do(t, "PATCH", "/api/users/nope", dt, map[string]any{"priority": 6})
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("patch unknown user = %d, want 404", res.StatusCode)
	}
}
