package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/schedule"
	"github.com/mcbulazs/restash/internal/store"
	"github.com/mcbulazs/restash/internal/vault"
)

func TestRemovingThings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inv, _ := e.store.SetupInvite(ctx)
	anna := e.client() // admin
	e.signup(anna, inv.Token, "anna@example.com", "pw")
	users, _ := e.store.Users(ctx)
	annaID := users[0].ID
	inv2, _ := e.store.CreateInvite(ctx, "bob@example.com", false, annaID, store.InviteLength)
	bob := e.client()
	e.signup(bob, inv2.Token, "bob@example.com", "pw")
	bobUser, _ := e.store.UserByEmail(ctx, "bob@example.com")
	code, _, _ := e.store.CreateJoinCode(ctx, bobUser.ID)
	pc, token, _ := e.store.Join(ctx, code, store.PC{Name: "bobs-pc"})
	agent := &http.Client{}
	bearer := []string{"Authorization", "Bearer " + pc.ID + "." + token}
	base := "/api/pcs/" + pc.ID

	key := vault.EntryKey(vault.NewKey())
	daily := schedule.Schedule{Kind: schedule.Daily, Minute: 60}
	addEntry := func(id string) {
		cfg, _ := vault.Seal(key, []byte(`{"name":"x"}`), vault.LabelEntry(pc.ID, id))
		if s := e.call(bob, "POST", base+"/entries", entryRequest{ID: id, Schedule: daily, Config: cfg}, nil); s != 200 {
			t.Fatalf("add entry: %d", s)
		}
		now := time.Now()
		if s := e.call(agent, "POST", "/api/agent/backups", proto.BackupReport{Entry: id, StartedAt: now.Add(-time.Minute), FinishedAt: now, OK: true, Bytes: 500}, nil, bearer...); s != 204 {
			t.Fatalf("report: %d", s)
		}
	}
	keep, gone, drop := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"
	addEntry(keep)
	addEntry(gone)
	addEntry(drop)

	purges := func() []string {
		var out []string
		e.call(agent, "GET", "/api/agent/purges", nil, &out, bearer...)
		return out
	}

	// Bob deletes one entry keeping its backups: they show as leftovers.
	if s := e.call(bob, "DELETE", base+"/entries/"+gone, nil, nil); s != 204 {
		t.Fatalf("delete keeping backups: %d", s)
	}
	var page struct {
		Leftovers []store.Leftover `json:"leftovers"`
		Purges    []string         `json:"purges"`
	}
	e.call(bob, "GET", base, nil, &page)
	if len(page.Leftovers) != 1 || page.Leftovers[0].Entry != gone || page.Leftovers[0].Bytes != 500 || len(purges()) != 0 {
		t.Fatalf("leftovers %+v, purges %v", page.Leftovers, purges())
	}
	// ...and removes them later.
	if s := e.call(bob, "POST", base+"/purges", map[string]string{"entry": keep}, nil); s != 409 {
		t.Fatalf("purge of a live entry: %d", s)
	}
	if s := e.call(bob, "POST", base+"/purges", map[string]string{"entry": "../x"}, nil); s != 400 {
		t.Fatalf("bad entry: %d", s)
	}
	if s := e.call(bob, "POST", base+"/purges", map[string]string{"entry": gone}, nil); s != 204 {
		t.Fatalf("purge leftovers: %d", s)
	}

	// The admin deletes another entry with its backups; nobody else can.
	inv3, _ := e.store.CreateInvite(ctx, "carl@example.com", false, annaID, store.InviteLength)
	carl := e.client()
	e.signup(carl, inv3.Token, "carl@example.com", "pw")
	if s := e.call(carl, "DELETE", base+"/entries/"+drop+"?backups=delete", nil, nil); s != 404 {
		t.Fatalf("carl deleted bob's entry: %d", s)
	}
	if s := e.call(anna, "DELETE", base+"/entries/"+drop+"?backups=delete", nil, nil); s != 204 {
		t.Fatalf("admin delete: %d", s)
	}
	if got := purges(); len(got) != 2 || got[0] != gone || got[1] != drop {
		t.Fatalf("purges %v", got)
	}

	// The admin's view of Bob: the PC, its one entry (no names), the queue.
	var person struct {
		Person struct{ Email string } `json:"person"`
		PCs    []adminPCJSON          `json:"pcs"`
	}
	if s := e.call(anna, "GET", "/api/people/"+bobUser.ID, nil, &person); s != 200 || len(person.PCs) != 1 {
		t.Fatalf("person: %d %+v", s, person)
	}
	p := person.PCs[0]
	if len(p.Entries) != 1 || p.Entries[0].ID != keep || p.Entries[0].LastOK == nil || len(p.Purges) != 2 || len(p.Leftovers) != 0 {
		t.Fatalf("admin view %+v", p)
	}
	if s := e.call(bob, "GET", "/api/people/"+bobUser.ID, nil, nil); s != 403 {
		t.Fatalf("bob used the admin view: %d", s)
	}

	// The PC says it's done: the request and that entry's history go.
	if s := e.call(agent, "POST", "/api/agent/purges/"+drop+"/done", nil, nil, bearer...); s != 204 {
		t.Fatalf("done: %d", s)
	}
	if s := e.call(agent, "POST", "/api/agent/purges/"+drop+"/done", nil, nil, bearer...); s != 404 {
		t.Fatalf("done twice: %d", s)
	}
	if got := purges(); len(got) != 1 || got[0] != gone {
		t.Fatalf("after done: %v", got)
	}

	// Removing the PC: unlinked, repo in the trash.
	repo := filepath.Join(e.server.cfg.ReposDir, pc.ID)
	os.MkdirAll(repo, 0o700)
	os.WriteFile(filepath.Join(repo, "config"), []byte("x"), 0o600)
	if s := e.call(carl, "DELETE", base, nil, nil); s != 404 {
		t.Fatalf("carl removed bob's PC: %d", s)
	}
	if s := e.call(bob, "DELETE", base, nil, nil); s != 204 {
		t.Fatalf("remove PC: %d", s)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatal("repo still in place")
	}
	if moved, _ := filepath.Glob(filepath.Join(e.server.cfg.Storage, "trash", "*", "removed-"+pc.ID+"-*", "config")); len(moved) != 1 {
		t.Fatal("repo not in the trash")
	}
	if s := e.call(agent, "GET", "/api/agent/account", nil, nil, bearer...); s != 401 {
		t.Fatalf("removed PC still signs in: %d", s)
	}

	// Removing people: not yourself; others with everything they had.
	if s := e.call(anna, "DELETE", "/api/people/"+annaID, nil, nil); s != 409 {
		t.Fatalf("removed self: %d", s)
	}
	if s := e.call(bob, "DELETE", "/api/people/"+bobUser.ID, nil, nil); s != 403 {
		t.Fatalf("non-admin removed someone: %d", s)
	}
	if s := e.call(anna, "DELETE", "/api/people/"+bobUser.ID, nil, nil); s != 204 {
		t.Fatalf("remove bob: %d", s)
	}
	if _, err := e.store.UserByID(ctx, bobUser.ID); !isNotFound(err) {
		t.Fatalf("bob still there: %v", err)
	}
	if s := e.call(bob, "GET", "/api/me", nil, nil); s != 401 {
		t.Fatalf("bob's session survived: %d", s)
	}
	if s := e.call(anna, "DELETE", "/api/people/"+bobUser.ID, nil, nil); s != 404 {
		t.Fatalf("remove twice: %d", s)
	}
}
