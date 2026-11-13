package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Recording is a server-side session recording's metadata; the asciicast
// itself is a file under the data directory.
type Recording struct {
	ID      string    `json:"id"`
	UserID  string    `json:"userId"`
	Email   string    `json:"email"`
	Host    string    `json:"host"`
	Login   string    `json:"login"`
	Port    int       `json:"port"`
	Proto   string    `json:"proto"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"` // zero while the session runs
	Bytes   int64     `json:"bytes"`
}

func (d *DB) CreateRecording(ctx context.Context, r Recording) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO recordings (id, user_id, host, login, port, proto, started_at, ended_at, bytes)
		VALUES (?,?,?,?,?,?,?,0,0)`, r.ID, r.UserID, r.Host, r.Login, r.Port, r.Proto, r.Started.Unix())
	return err
}

func (d *DB) FinishRecording(ctx context.Context, id string, bytes int64) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE recordings SET ended_at=?, bytes=? WHERE id=?`, time.Now().Unix(), bytes, id)
	return err
}

const recordingCols = `r.id, r.user_id, COALESCE(u.email, ''), r.host, r.login, r.port, r.proto, r.started_at, r.ended_at, r.bytes`

func scanRecording(row interface{ Scan(...any) error }) (Recording, error) {
	var r Recording
	var started, ended int64
	err := row.Scan(&r.ID, &r.UserID, &r.Email, &r.Host, &r.Login, &r.Port, &r.Proto, &started, &ended, &r.Bytes)
	r.Started = time.Unix(started, 0)
	if ended > 0 {
		r.Ended = time.Unix(ended, 0)
	}
	return r, err
}

// ListRecordings returns the newest recordings first, optionally for one account.
func (d *DB) ListRecordings(ctx context.Context, userID string, limit int) ([]Recording, error) {
	q := `SELECT ` + recordingCols + ` FROM recordings r LEFT JOIN users u ON u.id = r.user_id`
	args := []any{}
	if userID != "" {
		q += ` WHERE r.user_id = ?`
		args = append(args, userID)
	}
	q += ` ORDER BY r.started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recording{}
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) RecordingByID(ctx context.Context, id string) (*Recording, error) {
	r, err := scanRecording(d.sql.QueryRowContext(ctx, `SELECT `+recordingCols+` FROM recordings r LEFT JOIN users u ON u.id = r.user_id WHERE r.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (d *DB) DeleteRecording(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM recordings WHERE id=?`, id)
	return err
}

// RecordingsBefore lists finished recordings that ended before t (for retention).
func (d *DB) RecordingsBefore(ctx context.Context, t time.Time) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id FROM recordings WHERE ended_at > 0 AND ended_at < ?`, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
