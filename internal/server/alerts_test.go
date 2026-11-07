package server

import (
	"context"
	"testing"
	"time"

	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/store"
)

func TestAlertsOnceAndRecovery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inv, _ := e.store.SetupInvite(ctx)
	anna := e.client()
	e.signup(anna, inv.Token, "anna@example.com", "pw")
	users, _ := e.store.Users(ctx)
	if s := e.call(anna, "PUT", "/api/notify", store.Notify{Topic: "anna-alerts", Fail: true, Quiet: true, Login: true}, nil); s != 204 {
		t.Fatalf("settings: %d", s)
	}
	if s := e.call(anna, "PUT", "/api/notify", store.Notify{Topic: "../evil"}, nil); s != 400 {
		t.Fatalf("bad topic accepted: %d", s)
	}

	code, _, _ := e.store.CreateJoinCode(ctx, users[0].ID)
	pc, _, _ := e.store.Join(ctx, code, store.PC{Name: "laptop"})
	e.store.CreateEntry(ctx, &store.Entry{ID: "aaaaaaaaaaaaaaaa", PCID: pc.ID, Schedule: schedule.Schedule{Kind: schedule.Hourly}, Config: "x"})
	now := time.Now()
	e.store.AddBackup(ctx, store.Backup{PCID: pc.ID, Entry: "aaaaaaaaaaaaaaaa", StartedAt: now, FinishedAt: now, OK: false})

	e.server.check(ctx)
	e.server.check(ctx)
	if got := e.sent(1); len(got) != 1 || got[0] != "anna-alerts|A backup failed on laptop" {
		t.Fatalf("after failure: %v", got)
	}
	var pcs []pcJSON
	e.call(anna, "GET", "/api/pcs", nil, &pcs)
	if pcs[0].Problem != "failed" {
		t.Fatalf("problem %q", pcs[0].Problem)
	}

	e.store.AddBackup(ctx, store.Backup{PCID: pc.ID, Entry: "aaaaaaaaaaaaaaaa", StartedAt: now, FinishedAt: now.Add(time.Second), OK: true})
	e.server.check(ctx)
	if got := e.sent(2); len(got) != 2 || got[1] != "anna-alerts|laptop is backing up again" {
		t.Fatalf("after recovery: %v", got)
	}

	// A login from a new browser is announced; the same browser again isn't.
	other := e.client()
	e.login(other, "anna@example.com", "pw")
	e.login(other, "anna@example.com", "pw")
	time.Sleep(100 * time.Millisecond) // a second login would show up by now
	if got := e.sent(3); len(got) != 3 || got[2] != "anna-alerts|New login for Anna" {
		t.Fatalf("after logins: %v", got)
	}

	var feed struct {
		Items []activityItem `json:"items"`
	}
	if s := e.call(anna, "GET", "/api/activity", nil, &feed); s != 200 {
		t.Fatal(s)
	}
	kinds := map[string]int{}
	for _, it := range feed.Items {
		kinds[it.Kind]++
	}
	if kinds["failed"] != 1 || kinds["backups"] != 1 || kinds["recovered"] != 1 || kinds["login"] != 1 {
		t.Fatalf("feed kinds %v", kinds)
	}
}
