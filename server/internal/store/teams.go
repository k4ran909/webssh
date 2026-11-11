package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// migrate brings databases created before teams up to date.
func migrate(db *sql.DB) error {
	cols := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('users')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		cols[n] = true
	}
	rows.Close()
	for col, def := range map[string]string{
		"public_key":      `TEXT NOT NULL DEFAULT ''`,
		"enc_private_key": `TEXT NOT NULL DEFAULT ''`,
		"is_admin":        `INTEGER NOT NULL DEFAULT 0`,
	} {
		if !cols[col] {
			if _, err := db.Exec(`ALTER TABLE users ADD COLUMN ` + col + ` ` + def); err != nil {
				return err
			}
		}
	}
	// The first account administers the gateway.
	_, err = db.Exec(`UPDATE users SET is_admin = 1 WHERE id = (SELECT id FROM users ORDER BY created_at, rowid LIMIT 1)
		AND NOT EXISTS (SELECT 1 FROM users WHERE is_admin = 1)`)
	return err
}

// SetKeypair stores a user's sharing keypair once; it can't be swapped later
// (teammates have wrapped team keys to the old public key).
func (d *DB) SetKeypair(ctx context.Context, userID, pub, encPriv string) error {
	res, err := d.sql.ExecContext(ctx, `UPDATE users SET public_key=?, enc_private_key=? WHERE id=? AND public_key=''`, pub, encPriv, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrExists
	}
	return nil
}

// ReplaceEncPrivateKey re-encrypts the private key (e.g. after recovery the vault key is unchanged, so this is rarely needed).
func (d *DB) ReplaceEncPrivateKey(ctx context.Context, userID, encPriv string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE users SET enc_private_key=? WHERE id=? AND public_key<>''`, encPriv, userID)
	return err
}

type UserSummary struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	TOTP      bool      `json:"totp"`
	IsAdmin   bool      `json:"isAdmin"`
	CreatedAt time.Time `json:"createdAt"`
	LastLogin time.Time `json:"lastLogin"`
}

func (d *DB) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT u.id, u.email, u.totp_secret IS NOT NULL, u.is_admin, u.created_at,
		COALESCE((SELECT MAX(created_at) FROM audit_events a WHERE a.user_id = u.id AND a.action IN ('login', 'account.created', 'account.recovered')), 0)
		FROM users u ORDER BY u.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserSummary{}
	for rows.Next() {
		var u UserSummary
		var created, last int64
		if err := rows.Scan(&u.ID, &u.Email, &u.TOTP, &u.IsAdmin, &created, &last); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0)
		if last > 0 {
			u.LastLogin = time.Unix(last, 0)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) DeleteUser(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// ── teams ──

type Team struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RequireTOTP bool   `json:"requireTotp"`
	// The caller's membership.
	Role       string `json:"role"`
	WrappedKey string `json:"wrappedKey,omitempty"`
	Confirmed  bool   `json:"confirmed"`
}

func (d *DB) CreateTeam(ctx context.Context, id, name, ownerID, wrappedKey string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name, created_at) VALUES (?,?,?)`, id, name, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_members (team_id, user_id, role, wrapped_key, created_at) VALUES (?,?,?,?,?)`,
		id, ownerID, "owner", wrappedKey, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) TeamsForUser(ctx context.Context, userID string) ([]Team, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT t.id, t.name, t.require_totp, m.role, m.wrapped_key
		FROM teams t JOIN team_members m ON m.team_id = t.id WHERE m.user_id = ? ORDER BY t.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.RequireTOTP, &t.Role, &t.WrappedKey); err != nil {
			return nil, err
		}
		t.Confirmed = t.WrappedKey != ""
		out = append(out, t)
	}
	return out, rows.Err()
}

// Membership returns the caller's team row (role and confirmation), or ErrNotFound.
func (d *DB) Membership(ctx context.Context, teamID, userID string) (*Team, error) {
	var t Team
	err := d.sql.QueryRowContext(ctx, `SELECT t.id, t.name, t.require_totp, m.role, m.wrapped_key
		FROM teams t JOIN team_members m ON m.team_id = t.id WHERE t.id = ? AND m.user_id = ?`, teamID, userID).
		Scan(&t.ID, &t.Name, &t.RequireTOTP, &t.Role, &t.WrappedKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	t.Confirmed = t.WrappedKey != ""
	return &t, err
}

type Member struct {
	UserID    string `json:"userId"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Confirmed bool   `json:"confirmed"`
	PublicKey string `json:"publicKey,omitempty"`
	TOTP      bool   `json:"totp"`
}

func (d *DB) Members(ctx context.Context, teamID string) ([]Member, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT u.id, u.email, m.role, m.wrapped_key <> '', u.public_key, u.totp_secret IS NOT NULL
		FROM team_members m JOIN users u ON u.id = m.user_id WHERE m.team_id = ?
		ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'member' THEN 2 ELSE 3 END, u.email`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Role, &m.Confirmed, &m.PublicKey, &m.TOTP); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) AddMember(ctx context.Context, teamID, userID, role, invitedBy string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO team_members (team_id, user_id, role, invited_by, created_at) VALUES (?,?,?,?,?)`,
		teamID, userID, role, invitedBy, time.Now().Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrExists
	}
	return err
}

// ConfirmMember stores the team key wrapped to the member's public key.
func (d *DB) ConfirmMember(ctx context.Context, teamID, userID, wrappedKey string) error {
	res, err := d.sql.ExecContext(ctx, `UPDATE team_members SET wrapped_key=? WHERE team_id=? AND user_id=?`, wrappedKey, teamID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) SetMemberRole(ctx context.Context, teamID, userID, role string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE team_members SET role=? WHERE team_id=? AND user_id=?`, role, teamID, userID)
	return err
}

func (d *DB) RemoveMember(ctx context.Context, teamID, userID string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM team_members WHERE team_id=? AND user_id=?`, teamID, userID)
	return err
}

func (d *DB) CountOwners(ctx context.Context, teamID string) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_members WHERE team_id=? AND role='owner'`, teamID).Scan(&n)
	return n, err
}

func (d *DB) DeleteTeam(ctx context.Context, teamID string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM teams WHERE id=?`, teamID)
	return err
}

func (d *DB) SetTeamRequireTOTP(ctx context.Context, teamID string, on bool) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE teams SET require_totp=? WHERE id=?`, on, teamID)
	return err
}

// ── team vault ──

func (d *DB) TeamItems(ctx context.Context, teamID string, since int64) ([]Item, int64, error) {
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT vault_revision FROM teams WHERE id=?`, teamID).Scan(&rev); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, data, revision, deleted FROM team_items WHERE team_id=? AND revision > ? ORDER BY revision`, teamID, since)
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

func (d *DB) CountTeamItems(ctx context.Context, teamID string) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_items WHERE team_id=? AND deleted=0`, teamID).Scan(&n)
	return n, err
}

func (d *DB) TeamItemExists(ctx context.Context, teamID, id string) (bool, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_items WHERE team_id=? AND id=? AND deleted=0`, teamID, id).Scan(&n)
	return n > 0, err
}

func (d *DB) PutTeamItem(ctx context.Context, teamID, id, data, by string) (int64, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rev int64
	if err := tx.QueryRowContext(ctx, `UPDATE teams SET vault_revision = vault_revision + 1 WHERE id=? RETURNING vault_revision`, teamID).Scan(&rev); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_items (team_id, id, data, revision, deleted, updated_by, updated_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT (team_id, id) DO UPDATE SET data=excluded.data, revision=excluded.revision, deleted=excluded.deleted,
		updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		teamID, id, data, rev, data == "", by, time.Now().Unix()); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}

// ── invites ──

type Invite struct {
	Email     string    `json:"email"`
	TeamID    string    `json:"teamId,omitempty"`
	Role      string    `json:"role,omitempty"`
	CreatedBy string    `json:"-"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (d *DB) CreateInvite(ctx context.Context, tokenHash []byte, inv *Invite) error {
	var team any
	if inv.TeamID != "" {
		team = inv.TeamID
	}
	_, err := d.sql.ExecContext(ctx, `INSERT INTO invites (token_hash, email, team_id, role, created_by, expires_at) VALUES (?,?,?,?,?,?)`,
		tokenHash, inv.Email, team, inv.Role, inv.CreatedBy, inv.ExpiresAt.Unix())
	return err
}

// LookupInvite returns a valid invite without using it up.
func (d *DB) LookupInvite(ctx context.Context, tokenHash []byte, now time.Time) (*Invite, error) {
	var inv Invite
	var team sql.NullString
	var exp int64
	err := d.sql.QueryRowContext(ctx, `SELECT email, team_id, role, created_by, expires_at FROM invites WHERE token_hash=? AND expires_at > ?`,
		tokenHash, now.Unix()).Scan(&inv.Email, &team, &inv.Role, &inv.CreatedBy, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	inv.TeamID, inv.ExpiresAt = team.String, time.Unix(exp, 0)
	return &inv, nil
}

func (d *DB) DeleteInvite(ctx context.Context, tokenHash []byte) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM invites WHERE token_hash=?`, tokenHash)
	return err
}

func (d *DB) PendingInvites(ctx context.Context, teamID string, now time.Time) ([]Invite, error) {
	q := `SELECT email, COALESCE(team_id,''), role, expires_at FROM invites WHERE expires_at > ?`
	args := []any{now.Unix()}
	if teamID != "" {
		q += ` AND team_id = ?`
		args = append(args, teamID)
	}
	rows, err := d.sql.QueryContext(ctx, q+` ORDER BY expires_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		var inv Invite
		var exp int64
		if err := rows.Scan(&inv.Email, &inv.TeamID, &inv.Role, &exp); err != nil {
			return nil, err
		}
		inv.ExpiresAt = time.Unix(exp, 0)
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (d *DB) RevokeInvites(ctx context.Context, email, teamID string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM invites WHERE email=? AND COALESCE(team_id,'')=?`, email, teamID)
	return err
}

// ── settings ──

func (d *DB) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := d.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?,?) ON CONFLICT (key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// AllAudit is the gateway-wide log for admins (optionally one user).
func (d *DB) AllAudit(ctx context.Context, userID string, limit int) ([]AuditEvent, error) {
	q := `SELECT a.id, a.action, a.ip, a.detail, a.created_at, COALESCE(u.email, '') FROM audit_events a LEFT JOIN users u ON u.id = a.user_id`
	args := []any{}
	if userID != "" {
		q += ` WHERE a.user_id = ?`
		args = append(args, userID)
	}
	rows, err := d.sql.QueryContext(ctx, q+` ORDER BY a.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		var ts int64
		if err := rows.Scan(&e.ID, &e.Action, &e.IP, &e.Detail, &ts, &e.Email); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
