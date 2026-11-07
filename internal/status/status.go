// Package status decides whether a PC's backups are fine: an entry is
// failed (its last run failed), late (a run is overdue while the PC was
// on), or the whole PC is quiet (no good backup for days).
package status

import (
	"time"

	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/store"
)

// QuietAfter is how long without a good backup makes a PC quiet.
const QuietAfter = 3 * 24 * time.Hour

// Grace is how overdue a run may be before it counts as late.
const Grace = time.Hour

// Problem kinds, worst first.
const (
	Failed = "failed"
	Quiet  = "quiet"
	Late   = "late"
)

// Problem is one thing wrong.
type Problem struct {
	Kind  string    `json:"kind"`
	PCID  string    `json:"pcId"`
	Entry string    `json:"entry,omitempty"` // empty for quiet
	Since time.Time `json:"since"`
}

// Key identifies a problem across checks, so it is announced once.
func (p Problem) Key() string { return p.Kind + ":" + p.PCID + ":" + p.Entry }

// PC is the state of one PC.
type PC struct {
	Problem  string    `json:"problem"` // worst kind, or "" if all is well
	Problems []Problem `json:"problems"`
	LastOK   time.Time `json:"lastOk"`
}

func location(pc store.PC) *time.Location {
	if pc.Timezone != "" {
		if loc, err := time.LoadLocation(pc.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// Evaluate works out a PC's state from its entries and their runs.
func Evaluate(pc store.PC, entries []store.Entry, runs map[string]store.EntryRun, now time.Time) PC {
	var out PC
	loc := location(pc)
	scheduled := false
	var firstEntry time.Time
	var late []Problem
	for _, e := range entries {
		r := runs[e.ID]
		if r.LastOK.After(out.LastOK) {
			out.LastOK = r.LastOK
		}
		if r.Last != nil && !r.Last.OK {
			out.Problems = append(out.Problems, Problem{Kind: Failed, PCID: pc.ID, Entry: e.ID, Since: r.Last.FinishedAt})
			continue
		}
		if e.Schedule.Kind == schedule.Manual {
			continue
		}
		scheduled = true
		base := r.LastOK
		if base.IsZero() {
			base = e.CreatedAt
		}
		if firstEntry.IsZero() || e.CreatedAt.Before(firstEntry) {
			firstEntry = e.CreatedAt
		}
		due, isDue := dueSince(e.Schedule, base, r.LastOK.IsZero(), now.In(loc))
		if isDue && now.Sub(due) > Grace && pc.LastSeen != nil && pc.LastSeen.After(due.Add(Grace)) {
			late = append(late, Problem{Kind: Late, PCID: pc.ID, Entry: e.ID, Since: due})
		}
	}
	// Quiet covers late: a PC that's been off for days is one problem.
	if scheduled {
		since := out.LastOK
		if since.IsZero() {
			since = firstEntry
		}
		if now.Sub(since) > QuietAfter {
			out.Problems = append(out.Problems, Problem{Kind: Quiet, PCID: pc.ID, Since: since})
			late = nil
		}
	}
	out.Problems = append(out.Problems, late...)
	for _, kind := range []string{Failed, Quiet, Late} {
		for _, p := range out.Problems {
			if p.Kind == kind && out.Problem == "" {
				out.Problem = kind
			}
		}
	}
	if out.Problems == nil {
		out.Problems = []Problem{}
	}
	return out
}

// dueSince says since when a run has been due, given the last good run
// (or, if never, when the entry was made: a new entry runs right away).
func dueSince(s schedule.Schedule, base time.Time, never bool, now time.Time) (time.Time, bool) {
	if never {
		return base, true
	}
	switch s.Kind {
	case schedule.Hourly:
		due := base.Add(time.Hour)
		return due, !due.After(now)
	case schedule.Daily, schedule.Weekly:
		last := s.Last(now)
		return last, base.Before(last)
	}
	return time.Time{}, false
}
