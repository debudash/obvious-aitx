package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustCreateUser(t *testing.T, st *Store, username string, role Role, priority int) User {
	t.Helper()
	u, err := st.CreateUser(context.Background(), username, "hash-"+username, "Display "+username, role, priority, "alias-"+username)
	if err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return u
}

func ptr[T any](v T) *T { return &v }

func TestUserLifecycle(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	u := mustCreateUser(t, st, "bravo_2", RoleField, 5)
	if u.ID == "" || u.Username != "bravo_2" || u.PasswordHash != "hash-bravo_2" ||
		u.DisplayName != "Display bravo_2" || u.Role != RoleField || u.Priority != 5 || u.FunctionalAlias != "alias-bravo_2" {
		t.Fatalf("created user mismatch: %+v", u)
	}

	if _, err := st.CreateUser(ctx, "bravo_2", "h", "d", RoleField, 5, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate username = %v, want ErrConflict", err)
	}

	if got, err := st.GetUserByID(ctx, u.ID); err != nil || got.ID != u.ID {
		t.Errorf("GetUserByID = (%+v, %v)", got, err)
	}
	if got, err := st.GetUserByUsername(ctx, "bravo_2"); err != nil || got.ID != u.ID {
		t.Errorf("GetUserByUsername = (%+v, %v)", got, err)
	}
	if _, err := st.GetUserByID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user by id = %v, want ErrNotFound", err)
	}
	if _, err := st.GetUserByUsername(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user by name = %v, want ErrNotFound", err)
	}

	upd, err := st.UpdateUser(ctx, u.ID, UserPatch{
		DisplayName:     ptr("Bravo 2 Actual"),
		Priority:        ptr(6),
		FunctionalAlias: ptr("B2"),
		Role:            ptr(RoleSupervisor),
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if upd.DisplayName != "Bravo 2 Actual" || upd.Priority != 6 || upd.FunctionalAlias != "B2" || upd.Role != RoleSupervisor {
		t.Errorf("patched user mismatch: %+v", upd)
	}
	if _, err := st.UpdateUser(ctx, "missing", UserPatch{Priority: ptr(6)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("patch missing user = %v, want ErrNotFound", err)
	}

	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Errorf("ListUsers = %d users, %v; want 1", len(users), err)
	}
}

func TestGroupLifecycleAndCascade(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	d := mustCreateUser(t, st, "dispatch_1", RoleDispatcher, 10)

	g, err := st.CreateGroup(ctx, "TAC-1", "tac one", d.ID)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.ID == "" || g.Name != "TAC-1" || g.CreatedBy != d.ID {
		t.Errorf("created group mismatch: %+v", g)
	}
	if _, err := st.CreateGroup(ctx, "TAC-1", "", d.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate group name = %v, want ErrConflict", err)
	}
	if got, err := st.GetGroupByName(ctx, "TAC-1"); err != nil || got.ID != g.ID {
		t.Errorf("GetGroupByName = (%+v, %v)", got, err)
	}
	if _, err := st.GetGroupByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing group = %v, want ErrNotFound", err)
	}

	// Membership rows exist before delete; DeleteGroup cascades them away.
	if _, err := st.AddMember(ctx, g.ID, d.ID); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := st.SetAffiliation(ctx, d.ID, g.ID, AffiliationAffiliated); err != nil {
		t.Fatalf("SetAffiliation: %v", err)
	}
	if err := st.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := st.GetGroup(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted group = %v, want ErrNotFound", err)
	}
	if _, err := st.AddMember(ctx, g.ID, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("member of deleted group = %v, want ErrNotFound", err)
	}
	if ids, err := st.AffiliatedUsers(ctx, g.ID); err != nil || len(ids) != 0 {
		t.Errorf("affiliations after group delete = %v, %v; want empty", ids, err)
	}
	groups, err := st.ListGroups(ctx)
	if err != nil || len(groups) != 0 {
		t.Errorf("ListGroups after delete = %d, %v; want 0", len(groups), err)
	}
}

func TestMembershipRules(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u1 := mustCreateUser(t, st, "bravo_2", RoleField, 5)
	u2 := mustCreateUser(t, st, "charlie_3", RoleField, 5)
	g, err := st.CreateGroup(ctx, "TAC-1", "", u1.ID)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if _, err := st.AddMember(ctx, g.ID, u1.ID); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := st.AddMember(ctx, g.ID, u1.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate member = %v, want ErrConflict", err)
	}
	if ok, err := st.IsMember(ctx, g.ID, u1.ID); err != nil || !ok {
		t.Errorf("IsMember(u1) = %v, %v; want true", ok, err)
	}
	if ok, err := st.IsMember(ctx, g.ID, u2.ID); err != nil || ok {
		t.Errorf("IsMember(u2) = %v, %v; want false", ok, err)
	}
	ids, err := st.MemberIDs(ctx, g.ID)
	if err != nil || len(ids) != 1 || ids[0] != u1.ID {
		t.Errorf("MemberIDs = %v, %v", ids, err)
	}
	groups, err := st.MemberOf(ctx, u1.ID)
	if err != nil || len(groups) != 1 || groups[0].ID != g.ID {
		t.Errorf("MemberOf = %+v, %v", groups, err)
	}

	if err := st.RemoveMember(ctx, g.ID, u1.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if err := st.RemoveMember(ctx, g.ID, u1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("double RemoveMember = %v, want ErrNotFound", err)
	}
	if ok, _ := st.IsMember(ctx, g.ID, u1.ID); ok {
		t.Error("member still present after removal")
	}
}

func TestAffiliations(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, st, "bravo_2", RoleField, 5)
	g, err := st.CreateGroup(ctx, "TAC-1", "", u.ID)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := st.AddMember(ctx, g.ID, u.ID); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	a, err := st.SetAffiliation(ctx, u.ID, g.ID, AffiliationAffiliated)
	if err != nil {
		t.Fatalf("SetAffiliation: %v", err)
	}
	if a.State != AffiliationAffiliated {
		t.Errorf("state = %q, want %q", a.State, AffiliationAffiliated)
	}
	ids, err := st.AffiliatedUsers(ctx, g.ID)
	if err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Errorf("AffiliatedUsers = %v, %v", ids, err)
	}

	// Flip to deaffiliated; the same row updates, not a second row.
	a, err = st.SetAffiliation(ctx, u.ID, g.ID, AffiliationDeaffiliated)
	if err != nil || a.State != AffiliationDeaffiliated {
		t.Fatalf("SetAffiliation flip = (%+v, %v)", a, err)
	}
	ids, _ = st.AffiliatedUsers(ctx, g.ID)
	if len(ids) != 0 {
		t.Errorf("AffiliatedUsers after deaffiliate = %v, want empty", ids)
	}
	rows, err := st.ListAffiliations(ctx, u.ID)
	if err != nil || len(rows) != 1 || rows[0].State != AffiliationDeaffiliated {
		t.Errorf("ListAffiliations = %+v, %v; want 1 deaffiliated row", rows, err)
	}
}

func TestAuditAppendList(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	actor := mustCreateUser(t, st, "dispatch_1", RoleDispatcher, 10)

	entries := []AuditEntry{
		{ActorID: actor.ID, Action: "group.create", TargetType: "group", TargetID: "g1", Detail: `{"name":"TAC-1"}`},
		{ActorID: actor.ID, Action: "membership.add", TargetType: "membership", TargetID: "g1:u1", Detail: `{}`},
		{ActorID: "", Action: "seed", TargetType: "system", TargetID: "", Detail: `{}`},
	}
	for _, e := range entries {
		if err := st.Append(ctx, e); err != nil {
			t.Fatalf("Append %s: %v", e.Action, err)
		}
	}

	rows, err := st.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("ListAudit = %d rows, want 3", len(rows))
	}
	if rows[0].Action != "seed" {
		t.Errorf("newest row = %q, want seed (ListAudit is newest-first)", rows[0].Action)
	}
	last := rows[len(rows)-1]
	if last.ActorID != actor.ID || last.Action != "group.create" || last.TargetType != "group" || last.TargetID != "g1" || last.Detail != `{"name":"TAC-1"}` {
		t.Errorf("audit row mismatch: %+v", last)
	}

	limited, err := st.ListAudit(ctx, 2)
	if err != nil || len(limited) != 2 {
		t.Errorf("ListAudit(limit 2) = %d rows, %v", len(limited), err)
	}
}

// TestConcurrentUserCreation hammers the store from 10 goroutines; the
// single-connection pool must serialize them without SQLITE_BUSY surfacing.
func TestConcurrentUserCreation(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.CreateUser(ctx, fmt.Sprintf("u%02d", i), "h", "d", RoleField, 5, "")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("user %d: %v", i, err)
		}
	}
	users, _ := st.ListUsers(ctx)
	if len(users) != n {
		t.Errorf("ListUsers = %d, want %d", len(users), n)
	}
}
