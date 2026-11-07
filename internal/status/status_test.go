package status

import (
	"testing"
	"time"

	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/store"
)

var now = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func pc(seen time.Time) store.PC {
	return store.PC{ID: "pc", CreatedAt: now.Add(-30 * 24 * time.Hour), LastSeen: &seen, Timezone: "UTC"}
}

func entry(id string, s schedule.Schedule) store.Entry {
	return store.Entry{ID: id, Schedule: s, CreatedAt: now.Add(-30 * 24 * time.Hour)}
}

func ok(at time.Time) store.EntryRun {
	return store.EntryRun{Last: &store.Backup{OK: true, FinishedAt: at}, LastOK: at}
}

var daily3 = schedule.Schedule{Kind: schedule.Daily, Minute: 180}

func TestAllWell(t *testing.T) {
	got := Evaluate(pc(now), []store.Entry{entry("a", daily3)}, map[string]store.EntryRun{"a": ok(now.Add(-9 * time.Hour))}, now)
	if got.Problem != "" {
		t.Fatalf("%+v", got)
	}
}

func TestLateOnlyIfThePCWasOn(t *testing.T) {
	runs := map[string]store.EntryRun{"a": ok(now.Add(-33 * time.Hour))} // last ran yesterday 03:00
	if got := Evaluate(pc(now), []store.Entry{entry("a", daily3)}, runs, now); got.Problem != Late {
		t.Fatalf("on but didn't back up: %+v", got)
	}
	if got := Evaluate(pc(now.Add(-10*time.Hour)), []store.Entry{entry("a", daily3)}, runs, now); got.Problem != "" {
		t.Fatalf("off since before 03:00: %+v", got)
	}
}

func TestFailedAndQuiet(t *testing.T) {
	failed := store.EntryRun{Last: &store.Backup{OK: false, FinishedAt: now.Add(-time.Hour)}, LastOK: now.Add(-25 * time.Hour)}
	got := Evaluate(pc(now), []store.Entry{entry("a", daily3)}, map[string]store.EntryRun{"a": failed}, now)
	if got.Problem != Failed || got.Problems[0].Entry != "a" {
		t.Fatalf("%+v", got)
	}
	old := map[string]store.EntryRun{"a": ok(now.Add(-4 * 24 * time.Hour))}
	got = Evaluate(pc(now.Add(-4*24*time.Hour)), []store.Entry{entry("a", daily3)}, old, now)
	if got.Problem != Quiet || len(got.Problems) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestManualNeverLate(t *testing.T) {
	got := Evaluate(pc(now), []store.Entry{entry("m", schedule.Schedule{Kind: schedule.Manual})}, nil, now)
	if got.Problem != "" {
		t.Fatalf("%+v", got)
	}
}

func TestNewEntryGetsGrace(t *testing.T) {
	e := entry("h", schedule.Schedule{Kind: schedule.Hourly})
	e.CreatedAt = now.Add(-10 * time.Minute)
	if got := Evaluate(pc(now), []store.Entry{e}, nil, now); got.Problem != "" {
		t.Fatalf("%+v", got)
	}
	e.CreatedAt = now.Add(-3 * time.Hour)
	if got := Evaluate(pc(now), []store.Entry{e}, nil, now); got.Problem != Late {
		t.Fatalf("%+v", got)
	}
}
