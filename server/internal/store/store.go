// Package store persists accounts, sessions, encrypted vault items and the
// audit log in SQLite (pure Go driver, no cgo).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

type DB struct{ sql *sql.DB }

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One writer at a time; avoids SQLITE_BUSY under concurrent requests.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{db}, nil
}

func (d *DB) Close() error { return d.sql.Close() }

type User struct {
	ID                 string
	Email              string
	KDF                string
	Salt               string
	AuthHash           []byte
	AuthSalt           []byte
	WrappedKey         string
	RecoveryWrappedKey string
	RecoveryHash       []byte
	RecoverySalt       []byte
	TOTPSecret         []byte
	TOTPPending        []byte
	TOTPLastStep       int64
	CreatedAt          time.Time
}

const userCols = `id, email, kdf, salt, auth_hash, auth_salt, wrapped_key, recovery_wrapped_key,
	recovery_hash, recovery_salt, totp_secret, totp_pending, totp_last_step, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	err := row.Scan(&u.ID, &u.Email, &u.KDF, &u.Salt, &u.AuthHash, &u.AuthSalt, &u.WrappedKey, &u.RecoveryWrappedKey,
		&u.RecoveryHash, &u.RecoverySalt, &u.TOTPSecret, &u.TOTPPending, &u.TOTPLastStep, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) CreateUser(ctx context.Context, u *User) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Email, u.KDF, u.Salt, u.AuthHash, u.AuthSalt, u.WrappedKey, u.RecoveryWrappedKey,
		u.RecoveryHash, u.RecoverySalt, u.TOTPSecret, u.TOTPPending, u.TOTPLastStep, u.CreatedAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrExists
	}
	return err
}

func (d *DB) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(d.sql.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

func (d *DB) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(d.sql.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// SetCredentials replaces the password-derived material (password change or recovery).
func (d *DB) SetCredentials(ctx context.Context, id, kdf, salt string, authHash, authSalt []byte, wrappedKey string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE users SET kdf=?, salt=?, auth_hash=?, auth_salt=?, wrapped_key=? WHERE id=?`,
		kdf, salt, authHash, authSalt, wrappedKey, id)
	return err
}

func (d *DB) SetRecovery(ctx context.Context, id, wrappedKey string, hash, salt []byte) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE users SET recovery_wrapped_key=?, recovery_hash=?, recovery_salt=? WHERE id=?`, wrappedKey, hash, salt, id)
	return err
}

func (d *DB) SetTOTPPending(ctx context.Context, id string, enc []byte) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE users SET totp_pending=? WHERE id=?`, enc, id)
	return err
}

// SetTOTP enables (secret != nil) or disables 2FA and clears any pending secret.
func (d *DB) SetTOTP(ctx context.Context, id string, secret []byte, lastStep int64) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE users SET totp_secret=?, totp_pending=NULL, totp_last_step=? WHERE id=?`, secret, lastStep, id)
	return err
}

// UseTOTPStep records a consumed time step; false if it (or a later one) was already used.
func (d *DB) UseTOTPStep(ctx context.Context, id string, step int64) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE users SET totp_last_step=? WHERE id=? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

type Session struct {
	TokenHash []byte
	UserID    string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

func (d *DB) CreateSession(ctx context.Context, s *Session) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, created_at, last_seen, expires_at, ip, user_agent) VALUES (?,?,?,?,?,?,?)`,
		s.TokenHash, s.UserID, s.CreatedAt.Unix(), s.LastSeen.Unix(), s.ExpiresAt.Unix(), s.IP, s.UserAgent)
	return err
}

func (d *DB) Session(ctx context.Context, tokenHash []byte, now time.Time) (*Session, error) {
	var s Session
	var created, seen, expires int64
	err := d.sql.QueryRowContext(ctx, `SELECT token_hash, user_id, created_at, last_seen, expires_at, ip, user_agent
		FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, now.Unix()).
		Scan(&s.TokenHash, &s.UserID, &created, &seen, &expires, &s.IP, &s.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.CreatedAt, s.LastSeen, s.ExpiresAt = time.Unix(created, 0), time.Unix(seen, 0), time.Unix(expires, 0)
	return &s, nil
}

func (d *DB) TouchSession(ctx context.Context, tokenHash []byte, seen, expires time.Time) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE sessions SET last_seen=?, expires_at=? WHERE token_hash=?`, seen.Unix(), expires.Unix(), tokenHash)
	return err
}

func (d *DB) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

// DeleteUserSessions signs a user out everywhere, optionally keeping one session.
func (d *DB) DeleteUserSessions(ctx context.Context, userID string, keep []byte) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash IS NOT ?`, userID, keep)
	return err
}

func (d *DB) PruneSessions(ctx context.Context, now time.Time) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
	return err
}

type Item struct {
	ID       string `json:"id"`
	Data     string `json:"data,omitempty"`
	Revision int64  `json:"revision"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// Items returns everything changed after revision since, tombstones included,
// plus the user's current vault revision.
func (d *DB) Items(ctx context.Context, userID string, since int64) ([]Item, int64, error) {
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT vault_revision FROM users WHERE id=?`, userID).Scan(&rev); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, data, revision, deleted FROM vault_items WHERE user_id=? AND revision > ? ORDER BY revision`, userID, since)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Data, &it.Revision, &it.Deleted); err != nil {
			return nil, 0, err
		}
		items = append(items, it)
	}
	return items, rev, rows.Err()
}

func (d *DB) CountItems(ctx context.Context, userID string) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_items WHERE user_id=? AND deleted=0`, userID).Scan(&n)
	return n, err
}

func (d *DB) ItemExists(ctx context.Context, userID, id string) (bool, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_items WHERE user_id=? AND id=? AND deleted=0`, userID, id).Scan(&n)
	return n > 0, err
}

// PutItem upserts (data != "") or tombstones (data == "") an item and returns its new revision.
func (d *DB) PutItem(ctx context.Context, userID, id, data string) (int64, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rev int64
	if err := tx.QueryRowContext(ctx, `UPDATE users SET vault_revision = vault_revision + 1 WHERE id=? RETURNING vault_revision`, userID).Scan(&rev); err != nil {
		return 0, err
	}
	deleted := data == ""
	if _, err := tx.ExecContext(ctx, `INSERT INTO vault_items (user_id, id, data, revision, deleted, updated_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT (user_id, id) DO UPDATE SET data=excluded.data, revision=excluded.revision, deleted=excluded.deleted, updated_at=excluded.updated_at`,
		userID, id, data, rev, deleted, time.Now().Unix()); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}

type AuditEvent struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	IP        string    `json:"ip"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"createdAt"`
}

func (d *DB) AddAudit(ctx context.Context, userID, action, ip, detail string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO audit_events (user_id, action, ip, detail, created_at) VALUES (?,?,?,?,?)`,
		userID, action, ip, detail, time.Now().Unix())
	return err
}

func (d *DB) Audit(ctx context.Context, userID string, limit int) ([]AuditEvent, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, action, ip, detail, created_at FROM audit_events WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		var ts int64
		if err := rows.Scan(&e.ID, &e.Action, &e.IP, &e.Detail, &ts); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
