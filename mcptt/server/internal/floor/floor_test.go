package floor

import (
	"encoding/json"
	"testing"
	"time"
)

// TestLadderConstants pins the locked priority ladder: dispatcher=10,
// emergency=9, supervisors 7–8, routine 4–6, ambient 1–3. A slice, not a
// map: MinLevel shares its value with PriorityAmbient, which would collide
// as duplicate map keys.
func TestLadderConstants(t *testing.T) {
	cases := []struct {
		got  FloorLevel
		want FloorLevel
	}{
		{PriorityAmbient, 1},
		{PriorityNormal, 4},
		{PrioritySupervisor, 7},
		{PriorityEmergency, 9},
		{PriorityDispatcher, 10},
		{MinLevel, 1},
		{MaxLevel, 10},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("constant = %d, want %d", tc.got, tc.want)
		}
	}
	if DefaultMaxTalkDuration != 60*time.Second {
		t.Errorf("DefaultMaxTalkDuration = %v, want 60s", DefaultMaxTalkDuration)
	}
}

func TestValid(t *testing.T) {
	cases := map[FloorLevel]bool{
		0: false, 1: true, 5: true, 10: true, 11: false, -3: false,
	}
	for level, want := range cases {
		if got := level.Valid(); got != want {
			t.Errorf("FloorLevel(%d).Valid() = %v, want %v", level, got, want)
		}
	}
}

func TestBandOf(t *testing.T) {
	cases := []struct {
		level FloorLevel
		want  Band
	}{
		{1, BandAmbient}, {2, BandAmbient}, {3, BandAmbient},
		{4, BandNormal}, {5, BandNormal}, {6, BandNormal},
		{7, BandSupervisor}, {8, BandSupervisor},
		{9, BandEmergency},
		{10, BandDispatcher},
		{0, ""}, {11, ""}, {-2, ""},
	}
	for _, tc := range cases {
		if got := BandOf(tc.level); got != tc.want {
			t.Errorf("BandOf(%d) = %q, want %q", tc.level, got, tc.want)
		}
	}
}

func TestPreempts(t *testing.T) {
	cases := []struct {
		a, b FloorLevel
		want bool
	}{
		{PriorityDispatcher, PriorityEmergency, true},
		{PriorityEmergency, PriorityNormal, true},
		{PrioritySupervisor, PriorityNormal, true},
		{PriorityNormal, PriorityAmbient, true},
		{PriorityNormal, PriorityNormal, false}, // equal levels queue, never pre-empt
		{PriorityNormal, PrioritySupervisor, false},
		{PriorityAmbient, PriorityDispatcher, false},
	}
	for _, tc := range cases {
		if got := Preempts(tc.a, tc.b); got != tc.want {
			t.Errorf("Preempts(%d, %d) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestDecisionJSONShape pins the FloorDecision JSON keys the clients will
// consume (priority, not "level"; userId; outcome).
func TestDecisionJSONShape(t *testing.T) {
	raw, err := json.Marshal(FloorDecision{UserID: "u1", Outcome: OutcomeQueued, Level: 7, QueuePosition: 2, Reason: "busy"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"userId":"u1"`, `"outcome":"queued"`, `"priority":7`, `"queuePosition":2`, `"reason":"busy"`} {
		if !jsonContains(string(raw), key) {
			t.Errorf("FloorDecision missing %s in %s", key, raw)
		}
	}
}

func jsonContains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
