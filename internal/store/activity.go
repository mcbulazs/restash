package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Notify is what a person wants to hear about, and where.
type Notify struct {
	Topic  string `json:"topic"` // ntfy topic name
	Fail   bool   `json:"fail"`
	Quiet  bool   `json:"quiet"`
	Login  bool   `json:"login"`
	Weekly bool   `json:"weekly"`
}

// NotifySettings returns a person's notification settings.
func (s *Store) NotifySettings(ctx context.Context, userID string) (Notify, error) {
	var n Notify
	err := s.db.QueryRowContext(ctx, "SELECT ntfy_topic, notify_fail, notify_quiet, notify_login, notify_weekly FROM users WHERE id = ?", userID).
		Scan(&n.Topic, &n.Fail, &n.Quiet, &n.Login, &n.Weekly)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// SetNotifySettings stores a person's notification settings.
func (s *Store) SetNotifySettings(ctx context.Context, userID string, n Notify) error {
	_, err := s.db.ExecContext(ctx, "UPDATE users SET ntfy_topic = ?, notify_fail = ?, notify_quiet = ?, notify_login = ?, notify_weekly = ? WHERE id = ?",
		n.Topic, n.Fail, n.Quiet, n.Login, n.Weekly, userID)
	return err
}

// Event is something that happened, for the activity feed. Info is plain
// (names, counts, times); Secret is locked with the owner's account key
// when it names files.
type Event struct {
	ID     int64          `json:"id"`
	UserID string         `json:"userId"`
	PCID   string         `json:"pcId,omitempty"`
	Kind   string         `json:"kind"`
	At     time.Time      `json:"at"`
	Info   map[string]any `json:"info"`
	Secret string         `json:"secret,omitempty"`
}

// AddEvent records an event.
func (s *Store) AddEvent(ctx context.Context, e Event) (int64, error) {
	if e.At.IsZero() {
		e.At = s.now()
	}
	info, err := json.Marshal(e.Info)
	if err != nil {
		return 0, err
	}
	var pc any
	if e.PCID != "" {
		pc = e.PCID
	}
	res, err := s.db.ExecContext(ctx, "INSERT INTO events(user_id, pc_id, kind, at, info, secret) VALUES(?,?,?,?,?,?)",
		e.UserID, pc, e.Kind, unix(e.At), string(info), e.Secret)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateEventInfo replaces an event's info (e.g. when a restore ends).
func (s *Store) UpdateEventInfo(ctx context.Context, id int64, info map[string]any) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE events SET info = ? WHERE id = ?", string(b), id)
	return err
}

// Events lists events since a time, newest first. An empty userID lists
// everyone's.
func (s *Store) Events(ctx context.Context, userID string, since time.Time) ([]Event, error) {
	q := "SELECT id, user_id, coalesce(pc_id, ''), kind, at, info, secret FROM events WHERE at > ?"
	args := []any{unix(since)}
	if userID != "" {
		q += " AND user_id = ?"
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY at DESC, id DESC LIMIT 2000", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at int64
		var info string
		if err := rows.Scan(&e.ID, &e.UserID, &e.PCID, &e.Kind, &at, &info, &e.Secret); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		json.Unmarshal([]byte(info), &e.Info)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SeenDevice records a browser for a user and says whether it is new.
func (s *Store) SeenDevice(ctx context.Context, userID, device string) (bool, error) {
	now := unix(s.now())
	res, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO devices(user_id, device, first_seen, last_seen) VALUES(?,?,?,?)", userID, hashToken(device), now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		_, err = s.db.ExecContext(ctx, "UPDATE devices SET last_seen = ? WHERE user_id = ? AND device = ?", now, userID, hashToken(device))
	}
	return n == 1, err
}

// Alert is an ongoing problem that was already announced.
type Alert struct {
	Key     string
	UserID  string
	Started time.Time
}

// Alerts lists the ongoing problems.
func (s *Store) Alerts(ctx context.Context) (map[string]Alert, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, user_id, started_at FROM alerts")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Alert{}
	for rows.Next() {
		var a Alert
		var at int64
		if err := rows.Scan(&a.Key, &a.UserID, &at); err != nil {
			return nil, err
		}
		a.Started = time.Unix(at, 0)
		out[a.Key] = a
	}
	return out, rows.Err()
}

// StartAlert records a new ongoing problem.
func (s *Store) StartAlert(ctx context.Context, a Alert) error {
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO alerts(key, user_id, started_at) VALUES(?,?,?)", a.Key, a.UserID, unix(a.Started))
	return err
}

// EndAlert forgets a problem that went away.
func (s *Store) EndAlert(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM alerts WHERE key = ?", key)
	return err
}

// EntryRun is an entry's latest run and latest good run.
type EntryRun struct {
	Last   *Backup
	LastOK time.Time
}

// EntryRuns returns, per PC and entry, the latest run and latest good run.
func (s *Store) EntryRuns(ctx context.Context) (map[string]map[string]EntryRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+backupCols+`,
		(SELECT max(finished_at) FROM backups g WHERE g.pc_id = b.pc_id AND g.entry = b.entry AND g.ok)
		FROM backups b WHERE finished_at = (SELECT max(finished_at) FROM backups m WHERE m.pc_id = b.pc_id AND m.entry = b.entry)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]EntryRun{}
	for rows.Next() {
		var b Backup
		var started, finished int64
		var lastOK sql.NullInt64
		if err := rows.Scan(&b.PCID, &b.Entry, &started, &finished, &b.OK, &b.Files, &b.Bytes, &b.Added, &b.Snapshot, &b.Detail, &lastOK); err != nil {
			return nil, err
		}
		b.StartedAt, b.FinishedAt = time.Unix(started, 0), time.Unix(finished, 0)
		if out[b.PCID] == nil {
			out[b.PCID] = map[string]EntryRun{}
		}
		r := EntryRun{Last: &b}
		if lastOK.Valid {
			r.LastOK = time.Unix(lastOK.Int64, 0)
		}
		out[b.PCID][b.Entry] = r
	}
	return out, rows.Err()
}

// Once returns true the first time it's called with a key and value, and
// false after that until the value changes (e.g. once per week).
func (s *Store) Once(ctx context.Context, key, value string) bool {
	var cur string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", "once:"+key).Scan(&cur)
	if err == nil && cur == value {
		return false
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", "once:"+key, value)
	return err == nil
}
