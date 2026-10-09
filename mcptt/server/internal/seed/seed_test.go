package seed

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestDemoRosterShape pins the locked roster: 1 dispatcher, 2 supervisors,
// 6 field users, 4 groups; every member references a known account.
func TestDemoRosterShape(t *testing.T) {
	r := DemoRoster()
	counts := map[store.Role]int{}
	known := map[string]bool{}
	for _, a := range r.Accounts {
		counts[a.Role]++
		known[a.Username] = true
	}
	if counts[store.RoleDispatcher] != 1 {
		t.Errorf("dispatchers = %d, want 1", counts[store.RoleDispatcher])
	}
	if counts[store.RoleSupervisor] != 2 {
		t.Errorf("supervisors = %d, want 2", counts[store.RoleSupervisor])
	}
	if counts[store.RoleField] != 6 {
		t.Errorf("field users = %d, want 6", counts[store.RoleField])
	}
	if len(r.Groups) != 4 {
		t.Errorf("groups = %d, want 4", len(r.Groups))
	}
	for _, g := range r.Groups {
		for _, m := range g.Members {
			if !known[m] {
				t.Errorf("group %s references unknown member %q", g.Name, m)
			}
		}
	}
	// Priority ladder per role.
	for _, a := range r.Accounts {
		if want := store.DefaultPriority(a.Role); a.Priority != want {
			t.Errorf("%s priority = %d, want role default %d", a.Username, a.Priority, want)
		}
	}
}

func TestRunSeedsAndIsIdempotent(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	r := DemoRoster()

	res, err := Run(ctx, st, r)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CreatedUsers != 9 || res.SkippedUsers != 0 {
		t.Errorf("first run users = created %d skipped %d, want 9/0", res.CreatedUsers, res.SkippedUsers)
	}
	if res.CreatedGroups != 4 || res.SkippedGroups != 0 {
		t.Errorf("first run groups = created %d skipped %d, want 4/0", res.CreatedGroups, res.SkippedGroups)
	}

	// The demo dispatcher can actually log in: bcrypt hash verifies.
	d, err := st.GetUserByUsername(ctx, "dispatcher_1")
	if err != nil {
		t.Fatalf("dispatcher_1 missing: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(d.PasswordHash), []byte(DemoPassword)); err != nil {
		t.Errorf("demo password does not verify: %v", err)
	}

	// Every seeded group member is affiliated — the demo's resting state.
	groups, err := st.ListGroups(ctx)
	if err != nil || len(groups) != 4 {
		t.Fatalf("ListGroups = %d, %v", len(groups), err)
	}
	for _, g := range groups {
		ids, err := st.AffiliatedUsers(ctx, g.ID)
		if err != nil {
			t.Fatalf("AffiliatedUsers(%s): %v", g.Name, err)
		}
		spec := rosterGroup(r, g.Name)
		if len(ids) != len(spec.Members) {
			t.Errorf("group %s = %d affiliated, want %d", g.Name, len(ids), len(spec.Members))
		}
	}

	// Second run: everything skips, nothing duplicates.
	res2, err := Run(ctx, st, r)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res2.CreatedUsers != 0 || res2.SkippedUsers != 9 || res2.CreatedGroups != 0 || res2.SkippedGroups != 4 {
		t.Errorf("second run = %+v, want all skipped", res2)
	}
	users, _ := st.ListUsers(ctx)
	if len(users) != 9 {
		t.Errorf("users after two runs = %d, want 9", len(users))
	}
}

func rosterGroup(r Roster, name string) GroupSpec {
	for _, g := range r.Groups {
		if g.Name == name {
			return g
		}
	}
	return GroupSpec{}
}
