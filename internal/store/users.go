package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// User is an account. The key fields are locked values; see package vault.
type User struct {
	ID                 string
	Email              string
	Name               string
	Admin              bool
	KDFSalt            string
	KDFIterations      int
	AuthHash           string
	AccountKey         string // locked with the password's wrap key
	RecoveryAuthHash   string
	RecoveryAccountKey string // locked with the recovery code's wrap key
	StorageLimit       int64  // bytes all their PCs may take together; 0: no limit
	CreatedAt          time.Time
}

// Credentials are the password-dependent fields, replaced on a reset.
type Credentials struct {
	KDFSalt       string
	KDFIterations int
	AuthHash      string
	AccountKey    string
}

const userCols = `id, email, name, is_admin, kdf_salt, kdf_iterations, auth_hash,
	account_key, recovery_auth_hash, recovery_account_key, storage_limit, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Admin, &u.KDFSalt, &u.KDFIterations, &u.AuthHash,
		&u.AccountKey, &u.RecoveryAuthHash, &u.RecoveryAccountKey, &u.StorageLimit, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

// NormalizeEmail trims and lowercases an address.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// UserByEmail finds an account by email.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE email = ?", NormalizeEmail(email)))
}

// UserByID finds an account by id.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
}

// Users lists all accounts, oldest first.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY created_at, email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SetStorageLimit sets how much a person's PCs may store; 0 removes the limit.
func (s *Store) SetStorageLimit(ctx context.Context, userID string, limit int64) error {
	res, err := s.db.ExecContext(ctx, "UPDATE users SET storage_limit = ? WHERE id = ?", max(limit, 0), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountUsers says how many accounts exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

// Errors from CreateUser.
var (
	ErrInviteInvalid = errors.New("this invite link is not valid anymore")
	ErrInviteEmail   = errors.New("this invite is for a different email address")
	ErrEmailTaken    = errors.New("an account with this email already exists")
)

// CreateUser makes an account from an invite and uses the invite up.
func (s *Store) CreateUser(ctx context.Context, inviteToken string, u *User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var invEmail string
	var admin bool
	err = tx.QueryRowContext(ctx, "SELECT email, admin FROM invites WHERE token = ? AND expires_at > ?",
		inviteToken, unix(s.now())).Scan(&invEmail, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInviteInvalid
	}
	if err != nil {
		return err
	}
	u.Email = NormalizeEmail(u.Email)
	if invEmail != "" && invEmail != u.Email {
		return ErrInviteEmail
	}
	u.ID = newID()
	u.Admin = admin
	u.CreatedAt = s.now()
	_, err = tx.ExecContext(ctx, "INSERT INTO users("+userCols+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
		u.ID, u.Email, u.Name, u.Admin, u.KDFSalt, u.KDFIterations, u.AuthHash,
		u.AccountKey, u.RecoveryAuthHash, u.RecoveryAccountKey, u.StorageLimit, unix(u.CreatedAt))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrEmailTaken
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM invites WHERE token = ?", inviteToken); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetCredentials replaces the password-dependent fields and logs the
// account out everywhere.
func (s *Store) ResetCredentials(ctx context.Context, userID string, c Credentials) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET kdf_salt = ?, kdf_iterations = ?, auth_hash = ?, account_key = ?
		WHERE id = ?`, c.KDFSalt, c.KDFIterations, c.AuthHash, c.AccountKey, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionLength is how long a login lasts.
const SessionLength = 30 * 24 * time.Hour

// CreateSession logs a user in and returns the session token.
func (s *Store) CreateSession(ctx context.Context, userID string) (string, error) {
	tok := NewToken()
	now := s.now()
	_, err := s.db.ExecContext(ctx, "INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES(?,?,?,?)",
		hashToken(tok), userID, unix(now), unix(now.Add(SessionLength)))
	return tok, err
}

// SessionUser returns the user a session token belongs to.
func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+prefixCols("u.", userCols)+`
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(token), unix(s.now())))
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hashToken(token))
	return err
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// Invite lets someone make an account.
type Invite struct {
	Token     string
	Email     string // empty: any address
	Admin     bool
	CreatedAt time.Time
	ExpiresAt time.Time
}

// InviteLength is how long an invite link works.
const InviteLength = 7 * 24 * time.Hour

// CreateInvite makes an invite link token.
func (s *Store) CreateInvite(ctx context.Context, email string, admin bool, createdBy string, ttl time.Duration) (*Invite, error) {
	now := s.now()
	inv := &Invite{Token: NewToken(), Email: NormalizeEmail(email), Admin: admin, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	var by any
	if createdBy != "" {
		by = createdBy
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO invites(token, email, admin, created_by, created_at, expires_at) VALUES(?,?,?,?,?,?)",
		inv.Token, inv.Email, inv.Admin, by, unix(inv.CreatedAt), unix(inv.ExpiresAt))
	return inv, err
}

// InviteByToken returns an invite that is still valid.
func (s *Store) InviteByToken(ctx context.Context, token string) (*Invite, error) {
	var inv Invite
	var created, expires int64
	err := s.db.QueryRowContext(ctx, "SELECT token, email, admin, created_at, expires_at FROM invites WHERE token = ? AND expires_at > ?",
		token, unix(s.now())).Scan(&inv.Token, &inv.Email, &inv.Admin, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	inv.CreatedAt, inv.ExpiresAt = time.Unix(created, 0), time.Unix(expires, 0)
	return &inv, nil
}

// Invites lists invites that haven't been used or expired, newest first.
func (s *Store) Invites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT token, email, admin, created_at, expires_at FROM invites WHERE expires_at > ? ORDER BY created_at DESC", unix(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var inv Invite
		var created, expires int64
		if err := rows.Scan(&inv.Token, &inv.Email, &inv.Admin, &created, &expires); err != nil {
			return nil, err
		}
		inv.CreatedAt, inv.ExpiresAt = time.Unix(created, 0), time.Unix(expires, 0)
		out = append(out, inv)
	}
	return out, rows.Err()
}

// DeleteInvite cancels an invite.
func (s *Store) DeleteInvite(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM invites WHERE token = ?", token)
	return err
}

// SetupInvite returns the invite for the first (admin) account, making one
// if needed. It returns ErrNotFound once any account exists.
func (s *Store) SetupInvite(ctx context.Context) (*Invite, error) {
	if n, err := s.CountUsers(ctx); err != nil || n > 0 {
		if err == nil {
			err = ErrNotFound
		}
		return nil, err
	}
	var token string
	err := s.db.QueryRowContext(ctx, "SELECT token FROM invites WHERE admin = 1 AND email = '' AND expires_at > ? ORDER BY expires_at DESC LIMIT 1",
		unix(s.now())).Scan(&token)
	if err == nil {
		return s.InviteByToken(ctx, token)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.CreateInvite(ctx, "", true, "", InviteLength)
}
