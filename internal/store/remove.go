package store

import (
	"context"
	"time"
)

// Removing things: an entry's backups (done by the PC), a PC, a person.

// AddPurge asks for an entry's backups to be removed by its PC.
func (s *Store) AddPurge(ctx context.Context, pcID, entry string) error {
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO purges(pc_id, entry, created_at) VALUES(?,?,?)", pcID, entry, unix(s.now()))
	return err
}

// Purges are the entries whose backups a PC still has to remove.
func (s *Store) Purges(ctx context.Context, pcID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT entry FROM purges WHERE pc_id = ? ORDER BY created_at", pcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeDone records that a PC removed an entry's backups: the request and
// the entry's history go.
func (s *Store) PurgeDone(ctx context.Context, pcID, entry string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM purges WHERE pc_id = ? AND entry = ?", pcID, entry)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM backups WHERE pc_id = ? AND entry = ?", pcID, entry); err != nil {
		return err
	}
	return tx.Commit()
}

// Leftover is the history of backups made by an entry that no longer
// exists (deleted, or "manual" ones made by hand), whose backups may still
// be in the repo.
type Leftover struct {
	Entry string    `json:"entry"`
	Runs  int       `json:"runs"`
	Last  time.Time `json:"last"`
	Bytes int64     `json:"bytes"` // size of the last good one
}

// Leftovers lists a PC's backups from entries that are gone, not counting
// ones already queued for removal.
func (s *Store) Leftovers(ctx context.Context, pcID string) ([]Leftover, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.entry, count(*), max(b.finished_at),
			coalesce((SELECT bytes FROM backups x WHERE x.pc_id = b.pc_id AND x.entry = b.entry AND x.ok ORDER BY x.finished_at DESC LIMIT 1), 0)
		FROM backups b
		WHERE b.pc_id = ? AND b.ok
			AND b.entry NOT IN (SELECT id FROM entries WHERE pc_id = ?)
			AND b.entry NOT IN (SELECT entry FROM purges WHERE pc_id = ?)
		GROUP BY b.entry ORDER BY max(b.finished_at) DESC`, pcID, pcID, pcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Leftover{}
	for rows.Next() {
		var l Leftover
		var last int64
		if err := rows.Scan(&l.Entry, &l.Runs, &last, &l.Bytes); err != nil {
			return nil, err
		}
		l.Last = time.Unix(last, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeletePC removes a PC and everything about it (entries, history, join
// codes pointing at it). Its repo on disk is the caller's job.
func (s *Store) DeletePC(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM pcs WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE join_codes SET pc_id = NULL WHERE pc_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM alerts WHERE key LIKE ?", "%"+id+"%"); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUser removes an account and everything that hangs off it (its PCs
// must be deleted first, so their repos can be moved away).
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM alerts WHERE user_id = ?", id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
