// Package seed creates the demo roster: 1 dispatcher, 2 supervisors, 6 field
// users, and 4 groups (per the locked delivery shape). Run is idempotent —
// re-running an already-seeded database changes nothing, so it is safe to
// mount as a container entrypoint step.
package seed

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/debudash/obvious-aitx/mcptt/server/internal/store"
)

// DemoPassword is the shared demo credential for the seeded roster. It is
// printed by cmd/seed and exists only for the hosted demo deployment.
const DemoPassword = "mcptt-demo-2026"

// Account describes one seeded user.
type Account struct {
	Username        string
	DisplayName     string
	Role            store.Role
	Priority        int
	FunctionalAlias string
}

// GroupSpec describes one seeded group and its member usernames.
type GroupSpec struct {
	Name        string
	Description string
	Members     []string
}

// Roster is a full seed payload.
type Roster struct {
	Accounts []Account
	Groups   []GroupSpec
}

// DemoRoster returns the locked demo roster.
func DemoRoster() Roster {
	accounts := []Account{
		{Username: "dispatcher_1", DisplayName: "Dispatch One", Role: store.RoleDispatcher, Priority: 10, FunctionalAlias: "Dispatch 1"},
		{Username: "supervisor_1", DisplayName: "Reyes, Ana", Role: store.RoleSupervisor, Priority: 8, FunctionalAlias: "Squad 1 Lead"},
		{Username: "supervisor_2", DisplayName: "Okafor, Ben", Role: store.RoleSupervisor, Priority: 8, FunctionalAlias: "Squad 3 Lead"},
		{Username: "bravo_2", DisplayName: "Bravo Unit 2", Role: store.RoleField, Priority: 5, FunctionalAlias: "Bravo 2"},
		{Username: "charlie_3", DisplayName: "Charlie Unit 3", Role: store.RoleField, Priority: 5, FunctionalAlias: "Charlie 3"},
		{Username: "delta_1", DisplayName: "Delta Unit 1", Role: store.RoleField, Priority: 5, FunctionalAlias: "Delta 1"},
		{Username: "echo_4", DisplayName: "Echo Unit 4", Role: store.RoleField, Priority: 5, FunctionalAlias: "Echo 4"},
		{Username: "foxtrot_5", DisplayName: "Foxtrot Unit 5", Role: store.RoleField, Priority: 5, FunctionalAlias: "Foxtrot 5"},
		{Username: "golf_6", DisplayName: "Golf Unit 6", Role: store.RoleField, Priority: 5, FunctionalAlias: "Golf 6"},
	}
	all := usernames(accounts)
	groups := []GroupSpec{
		{Name: "TAC-1", Description: "Tactical channel 1", Members: []string{"bravo_2", "charlie_3", "supervisor_1", "dispatcher_1"}},
		{Name: "TAC-2", Description: "Tactical channel 2", Members: []string{"delta_1", "echo_4", "supervisor_2", "dispatcher_1"}},
		{Name: "Patrol-East", Description: "East sector patrol", Members: []string{"foxtrot_5", "golf_6", "supervisor_1", "dispatcher_1"}},
		{Name: "All-Hands", Description: "Every unit, dispatch included", Members: all},
	}
	return Roster{Accounts: accounts, Groups: groups}
}

func usernames(accounts []Account) []string {
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, a.Username)
	}
	return names
}

// Result reports what Run created versus skipped (already present).
type Result struct {
	CreatedUsers  int
	SkippedUsers  int
	CreatedGroups int
	SkippedGroups int
	Memberships   int
	Affiliations  int
}

// Run applies the roster. Users are keyed by username, groups by name; both
// skip when they already exist. Every group member is enrolled and left
// affiliated, which is the resting state the demo assumes.
func Run(ctx context.Context, st *store.Store, roster Roster) (Result, error) {
	res := Result{}

	ids := map[string]string{} // username → user id
	for _, a := range roster.Accounts {
		u, err := st.GetUserByUsername(ctx, a.Username)
		switch {
		case err == nil:
			ids[a.Username] = u.ID
			res.SkippedUsers++
		case errors.Is(err, store.ErrNotFound):
			hash, hErr := bcrypt.GenerateFromPassword([]byte(DemoPassword), bcrypt.DefaultCost)
			if hErr != nil {
				return res, fmt.Errorf("seed: hash password for %s: %w", a.Username, hErr)
			}
			created, cErr := st.CreateUser(ctx, a.Username, string(hash), a.DisplayName, a.Role, a.Priority, a.FunctionalAlias)
			if cErr != nil {
				return res, fmt.Errorf("seed: create user %s: %w", a.Username, cErr)
			}
			ids[a.Username] = created.ID
			res.CreatedUsers++
		default:
			return res, fmt.Errorf("seed: lookup user %s: %w", a.Username, err)
		}
	}

	groupIDs := map[string]string{}
	for _, g := range roster.Groups {
		existing, err := st.GetGroupByName(ctx, g.Name)
		switch {
		case err == nil:
			groupIDs[g.Name] = existing.ID
			res.SkippedGroups++
		case errors.Is(err, store.ErrNotFound):
			created, cErr := st.CreateGroup(ctx, g.Name, g.Description, ids["dispatcher_1"])
			if cErr != nil {
				return res, fmt.Errorf("seed: create group %s: %w", g.Name, cErr)
			}
			groupIDs[g.Name] = created.ID
			res.CreatedGroups++
		default:
			return res, fmt.Errorf("seed: find group %s: %w", g.Name, err)
		}
		for _, member := range g.Members {
			userID := ids[member]
			if userID == "" {
				return res, fmt.Errorf("seed: group %s references unknown user %s", g.Name, member)
			}
			if _, err := st.AddMember(ctx, groupIDs[g.Name], userID); err != nil && !errors.Is(err, store.ErrConflict) {
				return res, fmt.Errorf("seed: add member %s to %s: %w", member, g.Name, err)
			}
			res.Memberships++
			if _, err := st.SetAffiliation(ctx, userID, groupIDs[g.Name], store.AffiliationAffiliated); err != nil {
				return res, fmt.Errorf("seed: affiliate %s to %s: %w", member, g.Name, err)
			}
			res.Affiliations++
		}
	}
	return res, nil
}
