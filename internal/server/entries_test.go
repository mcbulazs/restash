package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/store"
	"github.com/mcbulazs/restash/internal/vault"
)

func TestEntries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inv, _ := e.store.SetupInvite(ctx)
	anna := e.client()
	e.signup(anna, inv.Token, "anna@example.com", "pw")
	users, _ := e.store.Users(ctx)
	code, _, _ := e.store.CreateJoinCode(ctx, users[0].ID)
	pc, token, _ := e.store.Join(ctx, code, store.PC{Name: "laptop"})
	bearer := []string{"Authorization", "Bearer " + pc.ID + "." + token}

	inv2, _ := e.store.CreateInvite(ctx, "bob@example.com", false, users[0].ID, store.InviteLength)
	bob := e.client()
	e.signup(bob, inv2.Token, "bob@example.com", "pw")

	key := vault.EntryKey(vault.NewKey())
	id := "abcdefghijklmnop"
	cfg, _ := vault.Seal(key, []byte(`{"name":"Documents"}`), vault.LabelEntry(pc.ID, id))
	daily := schedule.Schedule{Kind: schedule.Daily, Minute: 180}

	base := "/api/pcs/" + pc.ID
	if s := e.call(bob, "POST", base+"/entries", entryRequest{ID: id, Schedule: daily, Config: cfg}, nil); s != 404 {
		t.Fatalf("bob added an entry to anna's PC: %d", s)
	}
	if s := e.call(anna, "POST", base+"/entries", entryRequest{ID: "BAD", Schedule: daily, Config: cfg}, nil); s != 400 {
		t.Fatalf("bad id: %d", s)
	}
	if s := e.call(anna, "POST", base+"/entries", entryRequest{ID: id, Schedule: schedule.Schedule{Kind: "often"}, Config: cfg}, nil); s != 400 {
		t.Fatalf("bad schedule: %d", s)
	}
	if s := e.call(anna, "POST", base+"/entries", entryRequest{ID: id, Schedule: daily, Config: cfg}, nil); s != 200 {
		t.Fatalf("create: %d", s)
	}

	var got []proto.Entry
	if s := e.call(&http.Client{}, "GET", "/api/agent/entries", nil, &got, bearer...); s != 200 || len(got) != 1 || got[0].Config != cfg || got[0].Schedule != daily {
		t.Fatalf("agent entries: %d %+v", s, got)
	}

	weekly := schedule.Schedule{Kind: schedule.Weekly, Minute: 720, Weekday: 0}
	if s := e.call(anna, "PUT", base+"/entries/"+id, entryRequest{Schedule: weekly, Config: cfg}, nil); s != 204 {
		t.Fatalf("update: %d", s)
	}
	if s := e.call(bob, "PUT", base+"/entries/"+id, entryRequest{Schedule: daily, Config: cfg}, nil); s != 404 {
		t.Fatalf("bob updated: %d", s)
	}

	detail, _ := vault.Seal(key, []byte("could not read file"), vault.LabelRunDetail(pc.ID))
	if s := e.call(&http.Client{}, "POST", "/api/agent/backups", proto.BackupReport{Entry: id, StartedAt: e.now(), FinishedAt: e.now(), OK: false, Detail: detail}, nil, bearer...); s != 204 {
		t.Fatalf("report: %d", s)
	}

	var page struct {
		Entries []entryJSON     `json:"entries"`
		Runs    []runDetailJSON `json:"runs"`
	}
	if s := e.call(anna, "GET", base, nil, &page); s != 200 || len(page.Entries) != 1 || page.Entries[0].Schedule != weekly || len(page.Runs) != 1 || page.Runs[0].Detail != detail {
		t.Fatalf("pc page: %d %+v", s, page)
	}
	if s := e.call(bob, "GET", base, nil, nil); s != 404 {
		t.Fatalf("bob saw anna's PC: %d", s)
	}

	// The PC isn't connected in this test.
	if s := e.call(anna, "POST", base+"/run", map[string]string{"entry": id}, nil); s != 409 {
		t.Fatalf("run offline: %d", s)
	}
	if s := e.call(anna, "POST", base+"/browse", map[string]string{"data": cfg}, nil); s != 409 {
		t.Fatalf("browse offline: %d", s)
	}

	if s := e.call(anna, "DELETE", base+"/entries/"+id, nil, nil); s != 204 {
		t.Fatalf("delete: %d", s)
	}
	e.call(&http.Client{}, "GET", "/api/agent/entries", nil, &got, bearer...)
	if len(got) != 0 {
		t.Fatalf("entry still there: %+v", got)
	}
}
