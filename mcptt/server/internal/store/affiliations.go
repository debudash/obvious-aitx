package store

import (
	"context"
	"time"
)

// Affiliation is the live user ↔ group association state that governs call
// participation (TS 23.280). Membership grants eligibility; affiliation is
// the current association, so it is a separate upserted row.
type Affiliation struct {
	UserID    string
	GroupID   string
	State     string // affiliated | deaffiliated
	ChangedAt time.Time
}

// Affiliation states — the values the affiliations.state column and the WSS
// AffiliationChanged broadcast both carry.
const (
	AffiliationAffiliated   = "affiliated"
	AffiliationDeaffiliated = "deaffiliated"
)

// SetAffiliation upserts the user's association state with a group.
// Callers validate eligibility (membership or dispatcher authority) before
// calling; unknown user/group ids map to ErrNotFound.
func (s *Store) SetAffiliation(ctx context.Context, userID, groupID, state string) (Affiliation, error) {
	if err := s.mustExistGroupAndUser(ctx, groupID, userID); err != nil {
		return Affiliation{}, err
	}
	now := nowRFC3339()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO affiliations (user_id, group_id, state, changed_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (user_id, group_id) DO UPDATE SET state = excluded.state, changed_at = excluded.changed_at`,
		userID, groupID, state, now)
	if err != nil {
		return Affiliation{}, mapErr(err)
	}
	return Affiliation{UserID: userID, GroupID: groupID, State: state, ChangedAt: parseTime(now)}, nil
}

// AffiliationState returns the current association row for a user/group.
func (s *Store) AffiliationState(ctx context.Context, userID, groupID string) (Affiliation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT user_id, group_id, state, changed_at FROM affiliations WHERE user_id = ? AND group_id = ?`, userID, groupID)
	var a Affiliation
	var changed string
	err := row.Scan(&a.UserID, &a.GroupID, &a.State, &changed)
	if err != nil {
		return Affiliation{}, mapErr(err)
	}
	a.ChangedAt = parseTime(changed)
	return a, nil
}

// ListAffiliations returns associations, optionally scoped to one user.
// userID == "" lists every association (dispatcher roster view).
func (s *Store) ListAffiliations(ctx context.Context, userID string) ([]Affiliation, error) {
	q := `SELECT user_id, group_id, state, changed_at FROM affiliations`
	args := []any{}
	if userID != "" {
		q += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	q += ` ORDER BY group_id, user_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []Affiliation{}
	for rows.Next() {
		var a Affiliation
		var changed string
		if err := rows.Scan(&a.UserID, &a.GroupID, &a.State, &changed); err != nil {
			return nil, mapErr(err)
		}
		a.ChangedAt = parseTime(changed)
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

// AffiliatedUsers lists users currently affiliated with a group — the call
// participation set the call-control PR will ring.
func (s *Store) AffiliatedUsers(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM affiliations WHERE group_id = ? AND state = 'affiliated' ORDER BY user_id`, groupID)
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
