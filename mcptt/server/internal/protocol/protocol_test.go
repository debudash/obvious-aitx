package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTypeConstants pins the wire strings against the spec's code sample —
// the mobile clients and console hardcode these names.
func TestTypeConstants(t *testing.T) {
	cases := map[string]string{
		"FloorRequest":       TypeFloorRequest,
		"FloorGranted":       TypeFloorGranted,
		"FloorDenied":        TypeFloorDenied,
		"FloorReleased":      TypeFloorReleased,
		"FloorPreempted":     TypeFloorPreempt,
		"FloorRevoke":        TypeFloorRevoke,
		"CallStart":          TypeCallStart,
		"CallJoined":         TypeCallJoined,
		"CallEnded":          TypeCallEnded,
		"EmergencyAlert":     TypeEmergencyAlert,
		"PresenceUpdate":     TypePresenceUpdate,
		"AffiliationChanged": TypeAffiliationChanged,
		"MediaOffer":         TypeMediaOffer,
		"MediaAnswer":        TypeMediaAnswer,
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}

func TestParseType(t *testing.T) {
	got, err := ParseType([]byte(`{"type":"FloorRequest","callId":"c1"}`))
	if err != nil || got != TypeFloorRequest {
		t.Errorf("ParseType = (%q, %v), want (FloorRequest, nil)", got, err)
	}
	if _, err := ParseType([]byte("not json")); err == nil {
		t.Error("ParseType(garbage) = nil error, want error")
	}
}

// TestFloorRequestWireFormat pins the exact JSON the spec's sample shows for
// a floor request: type, callId, userId, priority, emergency.
func TestFloorRequestWireFormat(t *testing.T) {
	raw, err := json.Marshal(FloorRequest{Type: TypeFloorRequest, CallID: "call_9f3", UserID: "radio_bravo", Priority: 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"FloorRequest","callId":"call_9f3","userId":"radio_bravo","priority":7,"emergency":false}`
	if string(raw) != want {
		t.Errorf("wire = %s, want %s", raw, want)
	}
	var back FloorRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.CallID != "call_9f3" || back.UserID != "radio_bravo" || back.Priority != 7 {
		t.Errorf("round trip mismatch: %+v", back)
	}
}

func TestFloorGrantedWireFormat(t *testing.T) {
	raw, err := json.Marshal(FloorGranted{Type: TypeFloorGranted, CallID: "call_9f3", UserID: "radio_bravo", Queue: []string{"radio_charlie"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"FloorGranted","callId":"call_9f3","userId":"radio_bravo","queue":["radio_charlie"]}`
	if string(raw) != want {
		t.Errorf("wire = %s, want %s", raw, want)
	}
}

func TestFloorDeniedAndReleasedWireFormat(t *testing.T) {
	denied, err := json.Marshal(FloorDenied{Type: TypeFloorDenied, CallID: "call_9f3", UserID: "radio_charlie", Reason: "busy", QueuePosition: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"type":"FloorDenied"`, `"reason":"busy"`, `"queuePosition":1`} {
		if !strings.Contains(string(denied), key) {
			t.Errorf("FloorDenied missing %s in %s", key, denied)
		}
	}
	released, err := json.Marshal(FloorReleased{Type: TypeFloorReleased, CallID: "call_9f3", UserID: "radio_bravo"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(released) != `{"type":"FloorReleased","callId":"call_9f3","userId":"radio_bravo"}` {
		t.Errorf("FloorReleased wire = %s", released)
	}
}

func TestFloorPreemptedAndRevokeWireFormat(t *testing.T) {
	pre, err := json.Marshal(FloorPreempted{Type: TypeFloorPreempt, CallID: "call_9f3", By: "radio_alpha", Emergency: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"FloorPreempted","callId":"call_9f3","by":"radio_alpha","emergency":true}`
	if string(pre) != want {
		t.Errorf("FloorPreempted wire = %s, want %s", pre, want)
	}
	rev, err := json.Marshal(FloorRevoke{Type: TypeFloorRevoke, CallID: "call_9f3", By: "dispatcher_1", Reason: "net-control"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"type":"FloorRevoke"`, `"by":"dispatcher_1"`, `"reason":"net-control"`} {
		if !strings.Contains(string(rev), key) {
			t.Errorf("FloorRevoke missing %s in %s", key, rev)
		}
	}
}

func TestCallMessagesWireFormat(t *testing.T) {
	start, err := json.Marshal(CallStart{Type: TypeCallStart, CallID: "c1", GroupID: "g1", Kind: CallKindGroup, InitiatorID: "u1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"type":"CallStart"`, `"kind":"group"`, `"initiatorId":"u1"`, `"groupId":"g1"`} {
		if !strings.Contains(string(start), key) {
			t.Errorf("CallStart missing %s in %s", key, start)
		}
	}
	joined, _ := json.Marshal(CallJoined{Type: TypeCallJoined, CallID: "c1", UserID: "u2"})
	if !strings.Contains(string(joined), `"type":"CallJoined"`) {
		t.Errorf("CallJoined wire = %s", joined)
	}
	ended, _ := json.Marshal(CallEnded{Type: TypeCallEnded, CallID: "c1", By: "dispatcher_1"})
	if !strings.Contains(string(ended), `"type":"CallEnded"`) {
		t.Errorf("CallEnded wire = %s", ended)
	}
}

// TestEmergencyAlertWireFormat pins location fields and the voiceless-alert
// shape (no callId).
func TestEmergencyAlertWireFormat(t *testing.T) {
	lat, lon := 47.6205, -122.3493
	raw, err := json.Marshal(EmergencyAlert{
		Type: TypeEmergencyAlert, AlertID: "a1", UserID: "bravo_2",
		Lat: &lat, Lon: &lon, Emergency: true, Note: "smoke",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"type":"EmergencyAlert"`, `"alertId":"a1"`, `"userId":"bravo_2"`, `"lat":47.6205`, `"lon":-122.3493`, `"emergency":true`, `"note":"smoke"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("EmergencyAlert missing %s in %s", key, raw)
		}
	}
	if strings.Contains(string(raw), "callId") {
		t.Errorf("voiceless alert must omit callId, got %s", raw)
	}
	var back EmergencyAlert
	if err := json.Unmarshal(raw, &back); err != nil || back.Lat == nil || *back.Lat != lat {
		t.Errorf("round trip = (%+v, %v)", back, err)
	}
}

func TestPresenceAndAffiliationWireFormat(t *testing.T) {
	p, err := json.Marshal(PresenceUpdate{Type: TypePresenceUpdate, UserID: "u1", State: PresenceOnline, At: 1760000000000})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"PresenceUpdate","userId":"u1","state":"online","at":1760000000000}`
	if string(p) != want {
		t.Errorf("PresenceUpdate wire = %s, want %s", p, want)
	}
	a, err := json.Marshal(AffiliationChanged{Type: TypeAffiliationChanged, UserID: "u1", GroupID: "g1", State: AffiliationAffiliated, At: 1760000000000})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want = `{"type":"AffiliationChanged","userId":"u1","groupId":"g1","state":"affiliated","at":1760000000000}`
	if string(a) != want {
		t.Errorf("AffiliationChanged wire = %s, want %s", a, want)
	}
}
