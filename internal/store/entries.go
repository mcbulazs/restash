package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mcbulazs/restash/internal/schedule"
)

// Entry is a set of folders on a PC with its own rules. Only the schedule is
// readable here; Config is locked with the PC's entry key.
type Entry struct {
	ID        string
	PCID      string
	Schedule  schedule.Schedule
	Config    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const entryCols = "id, pc_id, kind, minute, weekday, config, created_at, updated_at"

func scanEntry(row interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	var created, updated int64
	err := row.Scan(&e.ID, &e.PCID, &e.Schedule.Kind, &e.Schedule.Minute, &e.Schedule.Weekday, &e.Config, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.CreatedAt, e.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &e, nil
}

// Entries lists a PC's entries, oldest first.
func (s *Store) Entries(ctx context.Context, pcID string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+entryCols+" FROM entries WHERE pc_id = ? ORDER BY created_at, id", pcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// AllEntries lists every entry, by PC.
func (s *Store) AllEntries(ctx context.Context) (map[string][]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+entryCols+" FROM entries ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out[e.PCID] = append(out[e.PCID], *e)
	}
	return out, rows.Err()
}

// CreateEntry adds an entry to a PC. The id is chosen by the caller, because
// the locked config is bound to it.
func (s *Store) CreateEntry(ctx context.Context, e *Entry) error {
	now := s.now()
	e.CreatedAt, e.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, "INSERT INTO entries("+entryCols+") VALUES(?,?,?,?,?,?,?,?)",
		e.ID, e.PCID, e.Schedule.Kind, e.Schedule.Minute, e.Schedule.Weekday, e.Config, unix(now), unix(now))
	return err
}

// UpdateEntry replaces an entry's schedule and config.
func (s *Store) UpdateEntry(ctx context.Context, e *Entry) error {
	e.UpdatedAt = s.now()
	res, err := s.db.ExecContext(ctx, "UPDATE entries SET kind = ?, minute = ?, weekday = ?, config = ?, updated_at = ? WHERE id = ? AND pc_id = ?",
		e.Schedule.Kind, e.Schedule.Minute, e.Schedule.Weekday, e.Config, unix(e.UpdatedAt), e.ID, e.PCID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEntry removes an entry. Its backups stay in the repo.
func (s *Store) DeleteEntry(ctx context.Context, pcID, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM entries WHERE id = ? AND pc_id = ?", id, pcID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// NewEntryID makes an id for a new entry.
func NewEntryID() string { return newID() }
