package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Alert lifecycle. An alert lands active and clears only on dispatcher
// acknowledgement — the acknowledgement loop the spec's emergency flow
// pins ("clears only when acknowledged"). Acknowledged alerts are kept so
// dispatch's emergency rail can archive them with location and timestamps.
const (
	AlertActive       = "active"
	AlertAcknowledged = "acknowledged"

	// AlertKind distinguishes "call for help" from "call about help":
	// imminent peril reuses the emergency priority tier under its own
	// flag so the audit log can tell the two apart.
	AlertKindEmergency     = "emergency"
	AlertKindImminentPeril = "imminent_peril"
)

// Alert is one emergency alert row: voiceless, or attached to a live call
// via CallID. Location is client-reported (lat/lon WGS-84); a device
// without a fix stores NULL for both.
type Alert struct {
	ID             string
	UserID         string
	CallID         string
	Kind           string
	Lat            *float64
	Lon            *float64
	Note           string
	Status         string
	AcknowledgedBy string
	AcknowledgedAt time.Time
	CreatedAt      time.Time
}

// CreateAlert inserts one emergency alert; ID and timestamps are assigned
// here.
func (s *Store) CreateAlert(ctx context.Context, userID, callID, kind string, lat, lon *float64, note string) (Alert, error) {
	if kind != AlertKindEmergency && kind != AlertKindImminentPeril {
		return Alert{}, fmt.Errorf("%w: unknown alert kind %q", ErrConflict, kind)
	}
	if (lat == nil) != (lon == nil) {
		return Alert{}, fmt.Errorf("%w: alert location needs both lat and lon", ErrConflict)
	}
	a := Alert{
		ID:     uuid.NewString(),
		UserID: userID,
		CallID: callID,
		Kind:   kind,
		Lat:    lat,
		Lon:    lon,
		Note:   note,
		Status: AlertActive,
	}
	var lonArg any
	if lon != nil {
		lonArg = *lon
	}
	var latArg any
	if lat != nil {
		latArg = *lat
	}
	now := nowRFC3339()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO emergency_alerts (id, user_id, call_id, kind, lat, lon, note, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserID, a.CallID, a.Kind, latArg, lonArg, a.Note, a.Status, now)
	if err != nil {
		return Alert{}, mapErr(err)
	}
	a.CreatedAt = parseTime(now)
	return a, nil
}

const alertColumns = `id, user_id, call_id, kind, lat, lon, note, status, acknowledged_by, acknowledged_at, created_at`

// GetAlert reads one alert by ID.
func (s *Store) GetAlert(ctx context.Context, id string) (Alert, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+alertColumns+` FROM emergency_alerts WHERE id = ?`, id)
	return scanAlert(row)
}

// ListAlerts returns alerts newest first. status filters to one lifecycle
// state; "" lists every alert (active and archive).
func (s *Store) ListAlerts(ctx context.Context, status string) ([]Alert, error) {
	q := `SELECT ` + alertColumns + ` FROM emergency_alerts`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

// ErrAlertNotActive — acknowledging an already-acknowledged alert is a
// conflict, not a silent overwrite: the ack loop must stay honest about
// which dispatcher cleared it.
var ErrAlertNotActive = errors.New("store: alert not active")

// AcknowledgeAlert clears an active alert, stamping who acknowledged it.
// An alert already acknowledged returns ErrAlertNotActive.
func (s *Store) AcknowledgeAlert(ctx context.Context, id, by string) (Alert, error) {
	now := nowRFC3339()
	res, err := s.db.ExecContext(ctx, `
UPDATE emergency_alerts
SET status = ?, acknowledged_by = ?, acknowledged_at = ?
WHERE id = ? AND status = ?`, AlertAcknowledged, by, now, id, AlertActive)
	if err != nil {
		return Alert{}, mapErr(err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Alert{}, mapErr(err)
	} else if n == 0 {
		// Either unknown ID or already acknowledged.
		if _, err := s.GetAlert(ctx, id); err != nil {
			return Alert{}, mapErr(err)
		}
		return Alert{}, ErrAlertNotActive
	}
	return s.GetAlert(ctx, id)
}

type rowScanner interface{ Scan(...any) error }

func scanAlert(row rowScanner) (Alert, error) {
	var a Alert
	var lat, lon, ackBy, ackAt any
	var created string
	if err := row.Scan(&a.ID, &a.UserID, &a.CallID, &a.Kind, &lat, &lon, &a.Note, &a.Status, &ackBy, &ackAt, &created); err != nil {
		return Alert{}, mapErr(err)
	}
	a.CreatedAt = parseTime(created)
	if lat != nil {
		v := lat.(float64)
		a.Lat = &v
	}
	if lon != nil {
		v := lon.(float64)
		a.Lon = &v
	}
	if ackBy != nil {
		a.AcknowledgedBy = ackBy.(string)
	}
	if ackAt != nil {
		a.AcknowledgedAt = parseTime(ackAt.(string))
	}
	return a, nil
}
