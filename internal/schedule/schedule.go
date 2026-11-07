// Package schedule decides when an entry's backup is due. Times are in the
// PC's local time zone.
package schedule

import (
	"errors"
	"time"
)

// Kinds of schedule.
const (
	Hourly = "hourly"
	Daily  = "daily"
	Weekly = "weekly"
	Manual = "manual"
)

// Schedule says when an entry runs. It is the only part of an entry the
// server can read, so it can tell when a backup is late.
type Schedule struct {
	Kind    string `json:"kind"`
	Minute  int    `json:"minute"`  // minutes after midnight (daily, weekly)
	Weekday int    `json:"weekday"` // 0 = Sunday (weekly)
}

// Validate checks the fields make sense for the kind.
func (s Schedule) Validate() error {
	switch s.Kind {
	case Hourly, Manual:
		return nil
	case Daily, Weekly:
		if s.Minute < 0 || s.Minute >= 24*60 {
			return errors.New("time of day out of range")
		}
		if s.Kind == Weekly && (s.Weekday < 0 || s.Weekday > 6) {
			return errors.New("day of week out of range")
		}
		return nil
	}
	return errors.New("unknown schedule")
}

// Last is the most recent time the schedule asked for a run, at or before
// now. Hourly has no fixed times, and manual never runs: both give zero.
func (s Schedule) Last(now time.Time) time.Time {
	switch s.Kind {
	case Daily:
		t := atMinute(now, s.Minute)
		if t.After(now) {
			t = atMinute(now.AddDate(0, 0, -1), s.Minute)
		}
		return t
	case Weekly:
		back := (int(now.Weekday()) - s.Weekday + 7) % 7
		t := atMinute(now.AddDate(0, 0, -back), s.Minute)
		if t.After(now) {
			t = t.AddDate(0, 0, -7)
		}
		return t
	}
	return time.Time{}
}

// Next is the next time a run is due after lastRun (zero if it never ran).
// A missed run is due right away. Manual entries are never due.
func (s Schedule) Next(lastRun, now time.Time) (time.Time, bool) {
	switch s.Kind {
	case Manual:
		return time.Time{}, false
	case Hourly:
		if lastRun.IsZero() {
			return now, true
		}
		return lastRun.Add(time.Hour), true
	}
	if lastRun.IsZero() || lastRun.Before(s.Last(now)) {
		return now, true
	}
	switch s.Kind {
	case Daily:
		return atMinute(now.AddDate(0, 0, 1), s.Minute), true
	default:
		return s.Last(now).AddDate(0, 0, 7), true
	}
}

// Due says whether a run should start now.
func (s Schedule) Due(lastRun, now time.Time) bool {
	next, ok := s.Next(lastRun, now)
	return ok && !next.After(now)
}

func atMinute(day time.Time, minute int) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, minute/60, minute%60, 0, 0, day.Location())
}
