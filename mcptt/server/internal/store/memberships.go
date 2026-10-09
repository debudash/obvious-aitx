package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Membership is the admin-managed eligibility row: a user is IN a group and
// may therefore affiliate with it. Affiliation is the live participation
// state (TS 23.280); membership governs who may hold it.
type Membership struct {
	GroupID   string
	UserID    string
	CreatedAt time.Time
}

// mustExistGroupAndUser maps dangling references to ErrNotFound instead of
// leaking a raw FOREIGN KEY constraint error to callers.
func (s *Store) mustExistGroupAndUser(ctx context.Context, groupID, userID string) error {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM groups WHERE id = ?`, groupID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return mapErr(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return mapErr(err)
	}
	return nil
}

// AddMember grants eligibility; duplicate rows map to ErrConflict and
// unknown group/user ids to ErrNotFound.
func (s *Store) AddMember(ctx context.Context, groupID, userID string) (Membership, error) {
	if err := s.mustExistGroupAndUser(ctx, groupID, userID); err != nil {
		return Membership{}, err
	}
	now := nowRFC3339()
	_, err := s.db.ExecContext(ctx, `INSERT INTO memberships (group_id, user_id, created_at) VALUES (?, ?, ?)`,
		groupID, userID, now)
	if err != nil {
		return Membership{}, mapErr(err)
	}
	return Membership{GroupID: groupID, UserID: userID, CreatedAt: parseTime(now)}, nil
}

// RemoveMember revokes eligibility and de-affiliates the user from the group
// in the same write — an unaffiliated-now-ineligible user must not keep an
// affiliated row.
func (s *Store) RemoveMember(ctx context.Context, groupID, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM memberships WHERE group_id = ? AND user_id = ?`, groupID, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM affiliations WHERE group_id = ? AND user_id = ?`, groupID, userID); err != nil {
		return mapErr(err)
	}
	return nil
}

// IsMember reports whether the user holds eligibility in the group.
func (s *Store) IsMember(ctx context.Context, groupID, userID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM memberships WHERE group_id = ? AND user_id = ?`, groupID, userID).Scan(&one)
	if err != nil {
		if errors.Is(mapErr(err), ErrNotFound) {
			return false, nil
		}
		return false, mapErr(err)
	}
	return one == 1, nil
}

// MemberIDs lists the users eligible for a group.
func (s *Store) MemberIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM memberships WHERE group_id = ? ORDER BY created_at`, groupID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, mapErr(err)
		}
		ids = append(ids, id)
	}
	return ids, mapErr(rows.Err())
}

// MemberOf lists the groups a user is eligible for.
func (s *Store) MemberOf(ctx context.Context, userID string) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT g.id, g.name, g.description, g.created_by, g.created_at, g.updated_at
FROM groups g JOIN memberships m ON m.group_id = g.id
WHERE m.user_id = ? ORDER BY g.name`, userID)
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
