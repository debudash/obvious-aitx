package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Group is a talk group: a named set of members eligible for affiliation
// (TS 23.280 group management).
type Group struct {
	ID          string
	Name        string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// GroupPatch carries optional updates for UpdateGroup; nil fields are skipped.
type GroupPatch struct {
	Name        *string
	Description *string
}

// CreateGroup inserts a group; ID and timestamps are assigned here.
func (s *Store) CreateGroup(ctx context.Context, name, description, createdBy string) (Group, error) {
	g := Group{
		ID:          uuid.NewString(),
		Name:        name,
		Description: description,
		CreatedBy:   createdBy,
	}
	now := nowRFC3339()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO groups (id, name, description, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		g.ID, g.Name, g.Description, g.CreatedBy, now, now)
	if err != nil {
		return Group{}, mapErr(err)
	}
	g.CreatedAt = parseTime(now)
	g.UpdatedAt = g.CreatedAt
	return g, nil
}

const groupColumns = `id, name, description, created_by, created_at, updated_at`

func scanGroup(row interface{ Scan(...any) error }) (Group, error) {
	var g Group
	var created, updated string
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedBy, &created, &updated)
	if err != nil {
		return Group{}, mapErr(err)
	}
	g.CreatedAt = parseTime(created)
	g.UpdatedAt = parseTime(updated)
	return g, nil
}

func (s *Store) GetGroup(ctx context.Context, id string) (Group, error) {
	g, err := scanGroup(s.db.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM groups WHERE id = ?`, id))
	return g, err
}

func (s *Store) GetGroupByName(ctx context.Context, name string) (Group, error) {
	g, err := scanGroup(s.db.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM groups WHERE name = ?`, name))
	return g, err
}

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupColumns+` FROM groups ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, mapErr(rows.Err())
}

// UpdateGroup applies a patch; membership rows are untouched.
func (s *Store) UpdateGroup(ctx context.Context, id string, p GroupPatch) (Group, error) {
	sets, args := []string{}, []any{}
	if p.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, *p.Name)
	}
	if p.Description != nil {
		sets, args = append(sets, "description = ?"), append(args, *p.Description)
	}
	if len(sets) == 0 {
		return s.GetGroup(ctx, id)
	}
	sets, args = append(sets, "updated_at = ?"), append(args, nowRFC3339())
	args = append(args, id)
	res, err := s.db.ExecContext(ctx, `UPDATE groups SET `+joinComma(sets)+` WHERE id = ?`, args...)
	if err != nil {
		return Group{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Group{}, ErrNotFound
	}
	return s.GetGroup(ctx, id)
}

// DeleteGroup removes the group; memberships and affiliations cascade.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// joinComma is a tiny builder for SET lists; keeps queries readable.
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// groupExists is a helper for membership/affiliation validation paths.
func (s *Store) groupExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM groups WHERE id = ?`, id).Scan(&one)
	if err != nil {
		return false, mapErr(err)
	}
	return one == 1, nil
}
