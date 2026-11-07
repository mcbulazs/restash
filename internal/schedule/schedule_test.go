package schedule

import (
	"testing"
	"time"
)

var loc = time.FixedZone("CET", 3600)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, loc) } // Nov 1 2026 is a Sunday

func TestDaily(t *testing.T) {
	s := Schedule{Kind: Daily, Minute: 3 * 60}
	if !s.Due(time.Time{}, at(7, 12, 0)) {
		t.Fatal("never ran: should be due")
	}
	if s.Due(at(7, 3, 5), at(7, 12, 0)) {
		t.Fatal("ran today after 03:00: not due")
	}
	if !s.Due(at(6, 3, 5), at(7, 12, 0)) {
		t.Fatal("missed today's 03:00: due")
	}
	if s.Due(at(6, 3, 5), at(7, 2, 0)) {
		t.Fatal("before today's 03:00: not due")
	}
	if next, _ := s.Next(at(7, 3, 5), at(7, 12, 0)); !next.Equal(at(8, 3, 0)) {
		t.Fatalf("next %v", next)
	}
}

func TestWeekly(t *testing.T) {
	s := Schedule{Kind: Weekly, Minute: 12 * 60, Weekday: 0} // Sundays at 12:00
	if got := s.Last(at(7, 9, 0)); !got.Equal(at(1, 12, 0)) {
		t.Fatalf("last %v", got)
	}
	if got := s.Last(at(8, 12, 0)); !got.Equal(at(8, 12, 0)) {
		t.Fatalf("last on the hour %v", got)
	}
	if s.Due(at(1, 12, 1), at(7, 9, 0)) {
		t.Fatal("ran last Sunday: not due")
	}
	if !s.Due(at(1, 11, 0), at(7, 9, 0)) {
		t.Fatal("missed last Sunday: due")
	}
}

func TestHourlyAndManual(t *testing.T) {
	h := Schedule{Kind: Hourly}
	if h.Due(at(7, 11, 30), at(7, 12, 0)) || !h.Due(at(7, 10, 59), at(7, 12, 0)) {
		t.Fatal("hourly")
	}
	m := Schedule{Kind: Manual}
	if m.Due(time.Time{}, at(7, 12, 0)) {
		t.Fatal("manual ran")
	}
}

func TestValidate(t *testing.T) {
	for _, bad := range []Schedule{{Kind: "x"}, {Kind: Daily, Minute: 1440}, {Kind: Weekly, Weekday: 7}} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
