package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Role is the account role for authorization decisions (TS 23.280 common
// services + the spec's priority ladder).
type Role string

const (
	RoleDispatcher Role = "dispatcher" // net control; talks at P10; administers accounts/groups
	RoleSupervisor Role = "supervisor" // P7–8; can pre-empt normal floors
	RoleField      Role = "field"      // P4–6; standard field user
)

// ValidRole reports whether r is one of the three account roles.
func ValidRole(r Role) bool {
	switch r {
	case RoleDispatcher, RoleSupervisor, RoleField:
		return true
	}
	return false
}

// DefaultPriority maps a role to its ladder default (spec: dispatcher 10,
// supervisors 7–8, routine users 4–6). Seeding uses this; dispatchers can
// override per user via the admin API.
func DefaultPriority(r Role) int {
	switch r {
	case RoleDispatcher:
		return 10
	case RoleSupervisor:
		return 8
	case RoleField:
		return 5
	}
	return 0
}

// User is one account. PasswordHash never leaves this package in API
// responses — the API layer maps to its own DTOs.
type User struct {
	ID              string
	Username        string
	PasswordHash    string
	DisplayName     string
	Role            Role
	Priority        int
	FunctionalAlias string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// UserPatch carries optional updates for UpdateUser; nil fields are skipped.
type UserPatch struct {
	DisplayName     *string
	Role            *Role
	Priority        *int
	FunctionalAlias *string
}

// CreateUser inserts a new account; ID and timestamps are assigned here.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, displayName string, role Role, priority int, functionalAlias string) (User, error) {
	u := User{
		ID:              uuid.NewString(),
		Username:        username,
		PasswordHash:    passwordHash,
		DisplayName:     displayName,
		Role:            role,
		Priority:        priority,
		FunctionalAlias: functionalAlias,
	}
	if !ValidRole(u.Role) {
		return User{}, fmt.Errorf("%w: unknown role %q", ErrConflict, u.Role)
	}
	if u.Priority < 1 || u.Priority > 10 {
		return User{}, fmt.Errorf("%w: priority %d outside 1–10", ErrConflict, u.Priority)
	}
	now := nowRFC3339()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (id, username, password_hash, display_name, role, priority, functional_alias, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.DisplayName, string(u.Role), u.Priority, u.FunctionalAlias, now, now)
	if err != nil {
		return User{}, mapErr(err)
	}
	u.CreatedAt = parseTime(now)
	u.UpdatedAt = u.CreatedAt
	return u, nil
}

const userColumns = `id, username, password_hash, display_name, role, priority, functional_alias, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var role, created, updated string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &role, &u.Priority, &u.FunctionalAlias, &created, &updated)
	if err != nil {
		return User{}, mapErr(err)
	}
	u.Role = Role(role)
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	return u, err
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
	return u, err
}

// ListUsers returns every account ordered by username — the roster source.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, mapErr(rows.Err())
}

// UpdateUser applies a patch to one account (admin surface).
func (s *Store) UpdateUser(ctx context.Context, id string, p UserPatch) (User, error) {
	sets, args := []string{}, []any{}
	if p.DisplayName != nil {
		sets, args = append(sets, "display_name = ?"), append(args, *p.DisplayName)
	}
	if p.Role != nil {
		if !ValidRole(*p.Role) {
			return User{}, fmt.Errorf("%w: unknown role %q", ErrConflict, *p.Role)
		}
		sets, args = append(sets, "role = ?"), append(args, string(*p.Role))
	}
	if p.Priority != nil {
		if *p.Priority < 1 || *p.Priority > 10 {
			return User{}, fmt.Errorf("%w: priority %d outside 1–10", ErrConflict, *p.Priority)
		}
		sets, args = append(sets, "priority = ?"), append(args, *p.Priority)
	}
	if p.FunctionalAlias != nil {
		sets, args = append(sets, "functional_alias = ?"), append(args, *p.FunctionalAlias)
	}
	if len(sets) == 0 {
		return s.GetUserByID(ctx, id)
	}
	sets, args = append(sets, "updated_at = ?"), append(args, nowRFC3339())
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, `UPDATE users SET `+joinComma(sets)+` WHERE id = ?`, args...)
	if err != nil {
		return User{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, ErrNotFound
	}
	return s.GetUserByID(ctx, id)
}
