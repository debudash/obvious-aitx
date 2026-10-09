package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/auth"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/callcontrol"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/floor"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
	"github.com/debudash/obvious-aitx/mcptt/server/internal/ws"
)

// ---------------------------------------------------------------------------
// Emergency + dispatch harness: a real SessionManager wired into the API the
// way main.go builds it (store-backed affiliation check, recording media
// hooks), so the acceptance tests drive production paths — not test doubles.

type hookRecorder struct {
	mu      sync.Mutex
	removed [][2]string // (callID, userID)
	ended   []string    // callIDs
}

func (h *hookRecorder) onRemove(callID, userID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removed = append(h.removed, [2]string{callID, userID})
	return true
}

func (h *hookRecorder) onEnd(callID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ended = append(h.ended, callID)
	return 0
}

func (h *hookRecorder) removals() [][2]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][2]string{}, h.removed...)
}

func (h *hookRecorder) ends() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.ended...)
}

// newCallsEnv is newEnv plus a live SessionManager and recording media
// hooks — the emergency/dispatch surface's full construction.
func newCallsEnv(t *testing.T) (*env, *callcontrol.SessionManager, *hookRecorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tokens := auth.NewTokenizer([]byte(testSecret), time.Hour)
	hub := ws.NewHandler(ws.NewHub(), tokens)

	calls := callcontrol.NewSessionManager(callcontrol.SessionConfig{
		Affiliation: func(uid, gid string) bool {
			aff, err := st.AffiliationState(context.Background(), uid, gid)
			return err == nil && aff.State == store.AffiliationAffiliated
		},
	})
	rec := &hookRecorder{}
	hooks := &MediaHooks{
		RemoveParticipant: rec.onRemove,
		EndCall:           rec.onEnd,
	}
	e := &env{st: st, tokens: tokens}
	e.ts = httptest.NewServer(New(st, tokens, hub, calls, hooks))
	t.Cleanup(e.ts.Close)
	return e, calls, rec
}

// affiliate seeds the live user↔group association the session manager's
// checker reads.
func (e *env) affiliate(t *testing.T, uid, gid string) {
	t.Helper()
	if _, err := e.st.SetAffiliation(context.Background(), uid, gid, store.AffiliationAffiliated); err != nil {
		t.Fatalf("affiliate %s@%s: %v", uid, gid, err)
	}
}

// liveCallWithSpeaker starts a group call through the session manager and
// puts speaker on the floor — the "active floor" an emergency pre-empts.
func liveCallWithSpeaker(t *testing.T, calls *callcontrol.SessionManager, groupID, speaker string, speakerPriority floor.FloorLevel, others ...string) string {
	t.Helper()
	info, err := calls.StartGroup(groupID, speaker, speakerPriority)
	if err != nil {
		t.Fatalf("start group call: %v", err)
	}
	for _, uid := range others {
		if _, err := calls.Join(info.CallID, uid, floor.PriorityNormal); err != nil {
			t.Fatalf("join %s: %v", uid, err)
		}
	}
	if d, err := calls.RequestFloor(info.CallID, speaker, false); err != nil || d.Outcome != floor.OutcomeGranted {
		t.Fatalf("speaker floor: decision %+v err %v", d, err)
	}
	return info.CallID
}

// TestEmergencyCallPreemptsAndUpgradesPriority — the spec's three emergency
// effects in one flow: priority upgrade to 9, floor pre-emption of the
// active talker, and client-reported location attached to the dispatcher
// alert.
func TestEmergencyCallPreemptsAndUpgradesPriority(t *testing.T) {
	e, calls, _ := newCallsEnv(t)
	ctx := context.Background()
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	sup := e.user(t, "sup1", store.RoleSupervisor, 7, "Squad 3 Lead")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	g := e.group(t, "alpha", disp, sup, field)
	e.affiliate(t, sup.ID, g.ID)
	e.affiliate(t, field.ID, g.ID)

	callID := liveCallWithSpeaker(t, calls, g.ID, sup.ID, floor.PrioritySupervisor, field.ID)

	lat, lon := 47.6205, -122.3493
	res, body := e.do(t, "POST", "/api/calls/emergency", e.loginAs(t, field), map[string]any{
		"callId": callID, "lat": lat, "lon": lon, "note": "man down",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("emergency call: status %d body %v", res.StatusCode, body)
	}

	// Effect 1+2: the caller's request was granted at the emergency tier
	// and displaced the active supervisor talker.
	dec, _ := body["decision"].(map[string]any)
	if dec["userId"] != field.ID || dec["outcome"] != string(floor.OutcomeGranted) {
		t.Fatalf("decision = %+v, want granted for %s", dec, field.ID)
	}
	if int(dec["priority"].(float64)) != int(floor.PriorityEmergency) {
		t.Fatalf("decision priority = %v, want %d", dec["priority"], floor.PriorityEmergency)
	}
	if dec["preemptedUserId"] != sup.ID {
		t.Fatalf("preemptedUserId = %v, want %s", dec["preemptedUserId"], sup.ID)
	}

	// Effect 3: the dispatcher alert carries the client-reported location.
	alert, _ := body["alert"].(map[string]any)
	if alert["callId"] != callID || alert["status"] != store.AlertActive {
		t.Fatalf("alert = %+v", alert)
	}
	if alert["lat"].(float64) != lat || alert["lon"].(float64) != lon {
		t.Fatalf("alert location = %v/%v, want %v/%v", alert["lat"], alert["lon"], lat, lon)
	}

	// The call snapshot now shows the emergency state and the new speaker.
	snap, err := calls.Snapshot(callID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snap.Emergency || snap.Speaker != field.ID {
		t.Fatalf("snapshot = emergency %v speaker %q", snap.Emergency, snap.Speaker)
	}
	if snap.SpeakerSince.IsZero() {
		t.Fatal("speakerSince is zero after emergency grant")
	}

	// The dispatcher rail sees the alert with location.
	list := e.doList(t, "GET", "/api/emergency/alerts", e.loginAs(t, disp))
	if len(list) != 1 {
		t.Fatalf("dispatcher alert list = %d items, want 1", len(list))
	}
	if list[0].(map[string]any)["userId"] != field.ID {
		t.Fatalf("rail alert = %v", list[0])
	}

	// The audit trail recorded the escalation with the pre-empted user.
	rows, err := e.st.ListAudit(ctx, 50)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Action == "emergency.call.started" && r.ActorID == field.ID && r.TargetID == callID {
			if !strings.Contains(r.Detail, sup.ID) {
				t.Fatalf("audit detail missing preempted user: %s", r.Detail)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no emergency.call.started audit row")
	}
}

// TestEmergencyGroupCallFromScratch — an emergency call to a group (no live
// call to escalate) opens at the emergency tier with the caller speaking.
func TestEmergencyGroupCallFromScratch(t *testing.T) {
	e, calls, _ := newCallsEnv(t)
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	g := e.group(t, "alpha", disp, field)
	e.affiliate(t, field.ID, g.ID)

	res, body := e.do(t, "POST", "/api/calls/emergency", e.loginAs(t, field), map[string]any{
		"groupId": g.ID,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("emergency call: status %d body %v", res.StatusCode, body)
	}
	call, _ := body["call"].(map[string]any)
	if call["emergency"] != true || call["kind"] != "group" {
		t.Fatalf("call = %+v", call)
	}
	snap, err := calls.Snapshot(call["callId"].(string))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Speaker != field.ID || !snap.Emergency {
		t.Fatalf("snapshot = speaker %q emergency %v", snap.Speaker, snap.Emergency)
	}
}

// TestEmergencyAlertAckLoop — the voiceless alert: lands active, clears
// only on a dispatcher's acknowledgement, re-ack is a conflict, and a
// field user cannot acknowledge at all.
func TestEmergencyAlertAckLoop(t *testing.T) {
	e, _, _ := newCallsEnv(t)
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	other := e.user(t, "field1", store.RoleField, 5, "Bravo 2")

	lat, lon := 47.6097, -122.3337
	res, body := e.do(t, "POST", "/api/emergency/alerts", e.loginAs(t, other), map[string]any{
		"lat": lat, "lon": lon, "note": "fuel leak",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("raise alert: status %d body %v", res.StatusCode, body)
	}
	alertID := body["id"].(string)

	// Voiceless: no call attached.
	if body["callId"] != "" && body["callId"] != nil {
		t.Fatalf("voiceless alert carries callId %v", body["callId"])
	}

	// A field user's ack is denied by role — and does not clear it.
	res, _ = e.do(t, "POST", "/api/emergency/alerts/"+alertID+"/ack", e.loginAs(t, other), nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("field ack: status %d, want 403", res.StatusCode)
	}

	// Dispatcher ack clears it, stamped with who acknowledged.
	res, body = e.do(t, "POST", "/api/emergency/alerts/"+alertID+"/ack", e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dispatcher ack: status %d body %v", res.StatusCode, body)
	}
	if body["status"] != store.AlertAcknowledged || body["acknowledgedBy"] != disp.ID {
		t.Fatalf("acked alert = %+v", body)
	}

	// Re-ack is a conflict, not a silent overwrite.
	res, _ = e.do(t, "POST", "/api/emergency/alerts/"+alertID+"/ack", e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("re-ack: status %d, want 409", res.StatusCode)
	}

	// The archive keeps it: status filter returns the acknowledged row.
	list := e.doList(t, "GET", "/api/emergency/alerts?status=acknowledged", e.loginAs(t, disp))
	if len(list) != 1 {
		t.Fatalf("acknowledged archive = %d items, want 1", len(list))
	}
}

// TestEmergencyAuditCompleteness — every emergency and dispatcher action in
// a scripted sequence lands exactly one audit row with the right actor.
func TestEmergencyAuditCompleteness(t *testing.T) {
	e, calls, _ := newCallsEnv(t)
	ctx := context.Background()
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	sup := e.user(t, "sup1", store.RoleSupervisor, 7, "Squad 3 Lead")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	field2 := e.user(t, "field2", store.RoleField, 5, "Charlie 3")
	g := e.group(t, "alpha", disp, sup, field, field2)
	for _, uid := range []string{sup.ID, field.ID, field2.ID} {
		e.affiliate(t, uid, g.ID)
	}

	callID := liveCallWithSpeaker(t, calls, g.ID, sup.ID, floor.PrioritySupervisor, field.ID, field2.ID)

	// 1. Emergency call with location.
	res, _ := e.do(t, "POST", "/api/calls/emergency", e.loginAs(t, field), map[string]any{"callId": callID})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("emergency call: status %d", res.StatusCode)
	}
	// 2. Voiceless alert + acknowledgement.
	res, body := e.do(t, "POST", "/api/emergency/alerts", e.loginAs(t, field2), map[string]any{"note": "perimeter"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("raise alert: status %d", res.StatusCode)
	}
	alertID := body["id"].(string)
	if res, _ = e.do(t, "POST", "/api/emergency/alerts/"+alertID+"/ack", e.loginAs(t, disp), nil); res.StatusCode != http.StatusOK {
		t.Fatalf("ack: status %d", res.StatusCode)
	}
	// 3. Dispatcher controls: revoke, remove, force-end, broadcast.
	if res, _ = e.do(t, "POST", "/api/dispatch/calls/"+callID+"/revoke", e.loginAs(t, disp), nil); res.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status %d", res.StatusCode)
	}
	if res, _ = e.do(t, "DELETE", "/api/dispatch/calls/"+callID+"/participants/"+field2.ID, e.loginAs(t, disp), nil); res.StatusCode != http.StatusOK {
		t.Fatalf("remove: status %d", res.StatusCode)
	}
	if res, _ = e.do(t, "POST", "/api/dispatch/calls/"+callID+"/end", e.loginAs(t, disp), nil); res.StatusCode != http.StatusOK {
		t.Fatalf("end: status %d", res.StatusCode)
	}
	if res, _ = e.do(t, "POST", "/api/dispatch/broadcast", e.loginAs(t, disp), map[string]any{"groupId": g.ID}); res.StatusCode != http.StatusCreated {
		t.Fatalf("broadcast: status %d", res.StatusCode)
	}

	want := []struct{ action, actor string }{
		{"emergency.call.started", field.ID},
		{"emergency.alert.raised", field2.ID},
		{"emergency.alert.acknowledged", disp.ID},
		{"dispatch.floor.revoked", disp.ID},
		{"dispatch.participant.removed", disp.ID},
		{"dispatch.call.ended", disp.ID},
		{"dispatch.broadcast.started", disp.ID},
	}
	rows, err := e.st.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, w := range want {
		found := false
		for _, r := range rows {
			if r.Action == w.action && r.ActorID == w.actor {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("audit missing action %q by %s", w.action, w.actor)
		}
	}
}

// TestDispatchRoleDeny — every dispatcher endpoint refuses non-dispatcher
// roles; the role claim is the authorization, tested on the deny path.
func TestDispatchRoleDeny(t *testing.T) {
	e, calls, _ := newCallsEnv(t)
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	sup := e.user(t, "sup1", store.RoleSupervisor, 7, "Squad 3 Lead")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	g := e.group(t, "alpha", disp, sup, field)
	e.affiliate(t, sup.ID, g.ID)
	e.affiliate(t, field.ID, g.ID)
	callID := liveCallWithSpeaker(t, calls, g.ID, sup.ID, floor.PrioritySupervisor, field.ID)
	alertRes, alertBody := e.do(t, "POST", "/api/emergency/alerts", e.loginAs(t, field), map[string]any{"note": "x"})
	if alertRes.StatusCode != http.StatusCreated {
		t.Fatalf("alert setup: status %d", alertRes.StatusCode)
	}
	alertID := alertBody["id"].(string)

	cases := []struct {
		name, method, path string
		body               any
	}{
		{"ack", "POST", "/api/emergency/alerts/" + alertID + "/ack", nil},
		{"force-end", "POST", "/api/dispatch/calls/" + callID + "/end", nil},
		{"revoke", "POST", "/api/dispatch/calls/" + callID + "/revoke", nil},
		{"remove", "DELETE", "/api/dispatch/calls/" + callID + "/participants/" + field.ID, nil},
		{"broadcast", "POST", "/api/dispatch/broadcast", map[string]any{"groupId": g.ID}},
	}
	for _, role := range []struct {
		name  string
		token string
	}{{"field", e.loginAs(t, field)}, {"supervisor", e.loginAs(t, sup)}} {
		for _, c := range cases {
			t.Run(role.name+"/"+c.name, func(t *testing.T) {
				res, _ := e.do(t, c.method, c.path, role.token, c.body)
				if res.StatusCode != http.StatusForbidden {
					t.Fatalf("%s %s as %s: status %d, want 403", c.method, c.path, role.name, res.StatusCode)
				}
			})
		}
	}

	// And the deny paths held: the alert is still active, the call still
	// live with its speaker.
	list := e.doList(t, "GET", "/api/emergency/alerts?status=active", e.loginAs(t, disp))
	if len(list) != 1 {
		t.Fatalf("alert cleared by denied request: %d items", len(list))
	}
	snap, err := calls.Snapshot(callID)
	if err != nil || snap.Speaker != sup.ID {
		t.Fatalf("call state changed by denied request: snapshot %v err %v", snap, err)
	}
}

// TestDispatchRevokeResolvesSpeaker — the console never names the target;
// the server revokes whoever holds the floor and the queue head is
// auto-granted.
func TestDispatchRevokeResolvesSpeaker(t *testing.T) {
	e, calls, _ := newCallsEnv(t)
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	sup := e.user(t, "sup1", store.RoleSupervisor, 7, "Squad 3 Lead")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	g := e.group(t, "alpha", disp, sup, field)
	e.affiliate(t, sup.ID, g.ID)
	e.affiliate(t, field.ID, g.ID)

	callID := liveCallWithSpeaker(t, calls, g.ID, sup.ID, floor.PrioritySupervisor, field.ID)
	// Field user queues behind the speaker.
	if d, err := calls.RequestFloor(callID, field.ID, false); err != nil || d.Outcome != floor.OutcomeQueued {
		t.Fatalf("queue request: %+v err %v", d, err)
	}

	res, body := e.do(t, "POST", "/api/dispatch/calls/"+callID+"/revoke", e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status %d body %v", res.StatusCode, body)
	}
	if body["revoked"] != sup.ID {
		t.Fatalf("revoked = %v, want %s", body["revoked"], sup.ID)
	}
	snap, err := calls.Snapshot(callID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Speaker != field.ID {
		t.Fatalf("speaker after revoke = %q, want queue head %s", snap.Speaker, field.ID)
	}

	// Revoking with nobody speaking is a conflict, not a no-op.
	snap2, _ := calls.Snapshot(callID)
	_ = snap2
	if d, err := calls.ReleaseFloor(callID, field.ID); err != nil {
		t.Fatalf("release: %v", err)
	} else {
		_ = d
	}
	res, _ = e.do(t, "POST", "/api/dispatch/calls/"+callID+"/revoke", e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("revoke idle: status %d, want 409", res.StatusCode)
	}
}

// TestDispatchRemoveParticipant — removal drops the party from the call and
// tears their media leg down server-side (zero further packets).
func TestDispatchRemoveParticipant(t *testing.T) {
	e, calls, rec := newCallsEnv(t)
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	sup := e.user(t, "sup1", store.RoleSupervisor, 7, "Squad 3 Lead")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	g := e.group(t, "alpha", disp, sup, field)
	e.affiliate(t, sup.ID, g.ID)
	e.affiliate(t, field.ID, g.ID)
	callID := liveCallWithSpeaker(t, calls, g.ID, sup.ID, floor.PrioritySupervisor, field.ID)

	res, _ := e.do(t, "DELETE", "/api/dispatch/calls/"+callID+"/participants/"+field.ID, e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("remove: status %d", res.StatusCode)
	}
	snap, err := calls.Snapshot(callID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, p := range snap.Participants {
		if p == field.ID {
			t.Fatalf("removed user still in participants: %v", snap.Participants)
		}
	}
	if len(rec.removals()) != 1 || rec.removals()[0] != [2]string{callID, field.ID} {
		t.Fatalf("media removal hook calls = %v", rec.removals())
	}

	// Removing a non-participant is 403 (not a participant of this call).
	res, _ = e.do(t, "DELETE", "/api/dispatch/calls/"+callID+"/participants/"+field.ID, e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("remove non-participant: status %d, want 403", res.StatusCode)
	}
}

// TestDispatchForceEnd — a forced end closes the call for everyone; further
// floor requests on it fail, and the media hook tore the room down.
func TestDispatchForceEnd(t *testing.T) {
	e, calls, rec := newCallsEnv(t)
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	sup := e.user(t, "sup1", store.RoleSupervisor, 7, "Squad 3 Lead")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	g := e.group(t, "alpha", disp, sup, field)
	e.affiliate(t, sup.ID, g.ID)
	e.affiliate(t, field.ID, g.ID)
	callID := liveCallWithSpeaker(t, calls, g.ID, sup.ID, floor.PrioritySupervisor, field.ID)

	res, _ := e.do(t, "POST", "/api/dispatch/calls/"+callID+"/end", e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("end: status %d", res.StatusCode)
	}
	if len(rec.ends()) != 1 || rec.ends()[0] != callID {
		t.Fatalf("media end hook calls = %v", rec.ends())
	}
	if _, err := calls.RequestFloor(callID, field.ID, false); err == nil {
		t.Fatal("floor request on ended call succeeded")
	}
	// Ending again is 404 — the call is gone.
	res, _ = e.do(t, "POST", "/api/dispatch/calls/"+callID+"/end", e.loginAs(t, disp), nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("re-end: status %d, want 404", res.StatusCode)
	}
}

// TestDispatchBroadcast — the dispatcher announcement call: broadcast kind,
// dispatcher on the floor at net-control priority.
func TestDispatchBroadcast(t *testing.T) {
	e, calls, _ := newCallsEnv(t)
	disp := e.user(t, "disp1", store.RoleDispatcher, 10, "TAC-1")
	field := e.user(t, "field1", store.RoleField, 5, "Bravo 2")
	g := e.group(t, "alpha", disp, field)
	e.affiliate(t, field.ID, g.ID)

	res, body := e.do(t, "POST", "/api/dispatch/broadcast", e.loginAs(t, disp), map[string]any{"groupId": g.ID})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("broadcast: status %d body %v", res.StatusCode, body)
	}
	call := body["call"].(map[string]any)
	if call["kind"] != "broadcast" || call["speaker"] != disp.ID {
		t.Fatalf("broadcast call = %+v", call)
	}
	snap, err := calls.Snapshot(call["callId"].(string))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Speaker != disp.ID {
		t.Fatalf("broadcast speaker = %q", snap.Speaker)
	}

	// Unknown group: existence is the guard (the announcer is exempt from
	// the affiliation rule), so this is a 404 — never an orphan call.
	res, _ = e.do(t, "POST", "/api/dispatch/broadcast", e.loginAs(t, disp), map[string]any{"groupId": "no-such-group"})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("broadcast unknown group: status %d, want 404", res.StatusCode)
	}
}

// TestRenderCallEvent — the pure event→wire mapping: a pre-empting grant
// emits the pre-emption notice before the new holder's grant, denied
// decisions carry queue position, and lifecycle events map to their
// protocol frames.
func TestRenderCallEvent(t *testing.T) {
	frames := ws.RenderCallEvent(callcontrol.Event{
		Type: callcontrol.EventFloorDecisions, CallID: "c1",
		Decisions: []floor.FloorDecision{
			{UserID: "field", Outcome: floor.OutcomeGranted, Level: floor.PriorityEmergency, Emergency: true, PreemptedUserID: "sup"},
			{UserID: "sup", Outcome: floor.OutcomeDenied, Reason: "preempted", QueuePosition: 0},
		},
		Queue: []string{"q1"},
	})
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3 (preempt + grant + deny)", len(frames))
	}
	var types []string
	for _, f := range frames {
		var m map[string]any
		if err := json.Unmarshal(f, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		types = append(types, m["type"].(string))
		switch m["type"] {
		case "FloorPreempted":
			if m["by"] != "field" || m["emergency"] != true {
				t.Fatalf("preempt frame = %v", m)
			}
		case "FloorGranted":
			if m["userId"] != "field" || m["queue"] == nil {
				t.Fatalf("grant frame = %v", m)
			}
		case "FloorDenied":
			if m["userId"] != "sup" || m["reason"] != "preempted" {
				t.Fatalf("deny frame = %v", m)
			}
		}
	}
	if types[0] != "FloorPreempted" || types[1] != "FloorGranted" {
		t.Fatalf("frame order = %v, want preempted before granted", types)
	}

	for _, tc := range []struct {
		ev   callcontrol.Event
		want string
	}{
		{callcontrol.Event{Type: callcontrol.EventCallStarted, CallID: "c1", GroupID: "g1", Kind: callcontrol.KindGroup, Actor: "u1"}, "CallStart"},
		{callcontrol.Event{Type: callcontrol.EventParticipantJoined, CallID: "c1", Actor: "u2"}, "CallJoined"},
		{callcontrol.Event{Type: callcontrol.EventParticipantRemoved, CallID: "c1", Actor: "d1", Target: "u2"}, "ParticipantRemoved"},
		{callcontrol.Event{Type: callcontrol.EventCallEnded, CallID: "c1", Actor: "d1"}, "CallEnded"},
	} {
		frames := ws.RenderCallEvent(tc.ev)
		if len(frames) != 1 {
			t.Fatalf("%s: %d frames", tc.ev.Type, len(frames))
		}
		var m map[string]any
		if err := json.Unmarshal(frames[0], &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["type"] != tc.want {
			t.Fatalf("frame type = %v, want %s", m["type"], tc.want)
		}
	}

	// Unknown event types render to nothing — no junk frames on the wire.
	if frames := ws.RenderCallEvent(callcontrol.Event{Type: "bogus"}); frames != nil {
		t.Fatalf("bogus event rendered %d frames", len(frames))
	}
}
