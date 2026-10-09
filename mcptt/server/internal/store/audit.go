package store

import (
	"context"
	"time"
)

// AuditEntry is one append-only audit trail record. Every state-mutating
// admin action lands here (who, what, on which target, with what detail).
type AuditEntry struct {
	ActorID    string // empty for system actions (seeding)
	Action     string
	TargetType string
	TargetID   string
	Detail     string // JSON object text
	CreatedAt  time.Time
}

// Append writes one audit record.
func (s *Store) Append(ctx context.Context, e AuditEntry) error {
	// System actions (seeding) carry no actor; the schema models that as
	// NULL, and an empty string would violate the actor FK.
	var actor any
	if e.ActorID != "" {
		actor = e.ActorID
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO audit_log (actor_id, action, target_type, target_id, detail, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		actor, e.Action, e.TargetType, e.TargetID, e.Detail, nowRFC3339())
	return mapErr(err)
}

// AuditRow is a read-back row for the console's audit view.
type AuditRow struct {
	ID         int64
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	Detail     string
	CreatedAt  time.Time
}

// ListAudit returns the newest entries first, capped at limit (0 = 100).
func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(actor_id, ''), action, target_type, target_id, detail, created_at
FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		var created string
		if err := rows.Scan(&r.ID, &r.ActorID, &r.Action, &r.TargetType, &r.TargetID, &r.Detail, &created); err != nil {
			return nil, mapErr(err)
		}
		r.CreatedAt = parseTime(created)
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// parseTime decodes the RFC3339 text the store writes; the zero time on
// decode failure is acceptable for display paths but must never happen on
// write paths, which format in the same package.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
