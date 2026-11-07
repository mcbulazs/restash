package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// PC is a computer that backs up to restash.
type PC struct {
	ID           string
	UserID       string
	Name         string
	OS           string
	Arch         string
	AgentVersion string
	PCKey        string // restic key locked with the account key; empty until set up
	CreatedAt    time.Time
	LastSeen     *time.Time
}

const pcCols = "id, user_id, name, os, arch, agent_version, coalesce(pc_key, ''), created_at, last_seen, token_hash"

func scanPC(row interface{ Scan(...any) error }) (*PC, string, error) {
	var p PC
	var created int64
	var seen sql.NullInt64
	var tokenHash string
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.OS, &p.Arch, &p.AgentVersion, &p.PCKey, &created, &seen, &tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	p.CreatedAt = time.Unix(created, 0)
	p.LastSeen = fromUnix(seen)
	return &p, tokenHash, nil
}

// Join codes: 6 characters without look-alikes (no 0, O, 1, I).
const (
	joinAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	joinLength   = 6
	// JoinCodeLength is how long a join code works.
	JoinCodeLength = time.Hour
)

// NormalizeJoinCode uppercases and drops anything that isn't part of a code.
func NormalizeJoinCode(code string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(code) {
		if strings.ContainsRune(joinAlphabet, r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// CreateJoinCode makes a one-time code that links a new PC to the user.
func (s *Store) CreateJoinCode(ctx context.Context, userID string) (string, time.Time, error) {
	b := make([]byte, joinLength)
	rand.Read(b)
	for i := range b {
		b[i] = joinAlphabet[b[i]&31]
	}
	code := string(b)
	now := s.now()
	exp := now.Add(JoinCodeLength)
	_, err := s.db.ExecContext(ctx, "INSERT INTO join_codes(code_hash, user_id, created_at, expires_at) VALUES(?,?,?,?)",
		hashToken(code), userID, unix(now), unix(exp))
	return code, exp, err
}

// JoinStatus says what happened to a user's join code: the PC that used it,
// or nil if it is still waiting. Expired codes give ErrNotFound.
func (s *Store) JoinStatus(ctx context.Context, userID, code string) (*PC, error) {
	var pcID sql.NullString
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT pc_id, expires_at FROM join_codes WHERE code_hash = ? AND user_id = ?",
		hashToken(NormalizeJoinCode(code)), userID).Scan(&pcID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if pcID.Valid {
		return s.PCByID(ctx, pcID.String)
	}
	if expires <= unix(s.now()) {
		return nil, ErrNotFound
	}
	return nil, nil
}

// Join uses up a join code and adds the PC. It returns the PC and the
// secret token the agent uses from then on.
func (s *Store) Join(ctx context.Context, code string, p PC) (*PC, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	hash := hashToken(NormalizeJoinCode(code))
	err = tx.QueryRowContext(ctx, "SELECT user_id FROM join_codes WHERE code_hash = ? AND pc_id IS NULL AND expires_at > ?",
		hash, unix(s.now())).Scan(&p.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	p.ID = newID()
	p.CreatedAt = s.now()
	token := NewToken()
	_, err = tx.ExecContext(ctx, `INSERT INTO pcs(id, user_id, name, os, arch, agent_version, token_hash, created_at, last_seen)
		VALUES(?,?,?,?,?,?,?,?,?)`, p.ID, p.UserID, p.Name, p.OS, p.Arch, p.AgentVersion, hashToken(token), unix(p.CreatedAt), unix(p.CreatedAt))
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE join_codes SET pc_id = ? WHERE code_hash = ?", p.ID, hash); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	now := p.CreatedAt
	p.LastSeen = &now
	return &p, token, nil
}

// PCByID finds a PC.
func (s *Store) PCByID(ctx context.Context, id string) (*PC, error) {
	p, _, err := scanPC(s.db.QueryRowContext(ctx, "SELECT "+pcCols+" FROM pcs WHERE id = ?", id))
	return p, err
}

// AuthPC checks an agent's token and returns its PC.
func (s *Store) AuthPC(ctx context.Context, id, token string) (*PC, error) {
	p, hash, err := scanPC(s.db.QueryRowContext(ctx, "SELECT "+pcCols+" FROM pcs WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(hash)) != 1 {
		return nil, ErrNotFound
	}
	return p, nil
}

// PCs lists PCs, oldest first. An empty userID lists everyone's.
func (s *Store) PCs(ctx context.Context, userID string) ([]PC, error) {
	q := "SELECT " + pcCols + " FROM pcs"
	var args []any
	if userID != "" {
		q += " WHERE user_id = ?"
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY created_at", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PC
	for rows.Next() {
		p, _, err := scanPC(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ErrKeySet means the PC already has a key; it can't be replaced.
var ErrKeySet = errors.New("this PC already has a key")

// SetPCKey stores the PC's locked restic key. It can be set only once, so a
// stolen agent token can't swap it.
func (s *Store) SetPCKey(ctx context.Context, id, sealed string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE pcs SET pc_key = ? WHERE id = ? AND pc_key IS NULL", sealed, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrKeySet
	}
	return nil
}

// TouchPC records that the agent was online now.
func (s *Store) TouchPC(ctx context.Context, id, version string) error {
	q, args := "UPDATE pcs SET last_seen = ? WHERE id = ?", []any{unix(s.now()), id}
	if version != "" {
		q, args = "UPDATE pcs SET last_seen = ?, agent_version = ? WHERE id = ?", []any{unix(s.now()), version, id}
	}
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// Backup is the result of one backup run.
type Backup struct {
	PCID       string
	Entry      string
	StartedAt  time.Time
	FinishedAt time.Time
	OK         bool
	Files      int64
	Bytes      int64
	Added      int64
	Snapshot   string
}

// AddBackup records a backup run.
func (s *Store) AddBackup(ctx context.Context, b Backup) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backups(pc_id, entry, started_at, finished_at, ok, files, bytes, added, snapshot)
		VALUES(?,?,?,?,?,?,?,?,?)`, b.PCID, b.Entry, unix(b.StartedAt), unix(b.FinishedAt), b.OK, b.Files, b.Bytes, b.Added, b.Snapshot)
	return err
}

// BackupsSince returns runs that finished after since, per PC, oldest first.
func (s *Store) BackupsSince(ctx context.Context, since time.Time) (map[string][]Backup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pc_id, entry, started_at, finished_at, ok, files, bytes, added, snapshot
		FROM backups WHERE finished_at > ? ORDER BY finished_at`, unix(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Backup{}
	for rows.Next() {
		var b Backup
		var started, finished int64
		if err := rows.Scan(&b.PCID, &b.Entry, &started, &finished, &b.OK, &b.Files, &b.Bytes, &b.Added, &b.Snapshot); err != nil {
			return nil, err
		}
		b.StartedAt, b.FinishedAt = time.Unix(started, 0), time.Unix(finished, 0)
		out[b.PCID] = append(out[b.PCID], b)
	}
	return out, rows.Err()
}

// LastBackups returns each PC's latest run and latest successful run.
func (s *Store) LastBackups(ctx context.Context) (last, lastOK map[string]time.Time, err error) {
	last, lastOK = map[string]time.Time{}, map[string]time.Time{}
	rows, err := s.db.QueryContext(ctx, `SELECT pc_id, max(finished_at), max(CASE WHEN ok THEN finished_at END) FROM backups GROUP BY pc_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var a int64
		var b sql.NullInt64
		if err := rows.Scan(&id, &a, &b); err != nil {
			return nil, nil, err
		}
		last[id] = time.Unix(a, 0)
		if b.Valid {
			lastOK[id] = time.Unix(b.Int64, 0)
		}
	}
	return last, lastOK, rows.Err()
}
