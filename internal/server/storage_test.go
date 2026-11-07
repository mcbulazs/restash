package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/store"
)

func TestStorageLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inv, _ := e.store.SetupInvite(ctx)
	anna := e.client() // the admin
	e.signup(anna, inv.Token, "anna@example.com", "pw")
	users, _ := e.store.Users(ctx)
	inv2, _ := e.store.CreateInvite(ctx, "bob@example.com", false, users[0].ID, store.InviteLength)
	bob := e.client()
	e.signup(bob, inv2.Token, "bob@example.com", "pw")
	bobUser, _ := e.store.UserByEmail(ctx, "bob@example.com")
	code, _, _ := e.store.CreateJoinCode(ctx, bobUser.ID)
	pc, token, _ := e.store.Join(ctx, code, store.PC{Name: "bobs-pc"})
	bearer := []string{"Authorization", "Bearer " + pc.ID + "." + token}

	// Bob's PC stores 1000 bytes.
	repo := filepath.Join(e.server.cfg.ReposDir, pc.ID, "data", "ab")
	os.MkdirAll(repo, 0o700)
	os.WriteFile(filepath.Join(repo, "pack"), make([]byte, 1000), 0o600)

	var st proto.Storage
	if e.call(bob, "GET", "/api/storage", nil, &st); st.Used != 1000 || st.Limit != 0 {
		t.Fatalf("no limit yet: %+v", st)
	}

	// Only the admin sets limits, and only sensible ones.
	limit := func(c *http.Client, n int64) int {
		return e.call(c, "PUT", "/api/people/"+bobUser.ID+"/limit", map[string]int64{"limit": n}, nil)
	}
	if s := limit(bob, 1<<40); s != 403 {
		t.Fatalf("bob raised his own limit: %d", s)
	}
	if s := limit(anna, -1); s != 400 {
		t.Fatalf("negative limit: %d", s)
	}
	if s := e.call(anna, "PUT", "/api/people/nobody/limit", map[string]int64{"limit": 5}, nil); s != 404 {
		t.Fatalf("unknown person: %d", s)
	}
	if s := limit(anna, 800); s != 204 {
		t.Fatalf("set limit: %d", s)
	}
	var people peopleJSON
	e.call(anna, "GET", "/api/people", nil, &people)
	for _, p := range people.People {
		if p.Email == "bob@example.com" && (p.Limit != 800 || p.Stored != 1000) {
			t.Fatalf("people: %+v", p)
		}
	}
	if e.call(bob, "GET", "/api/storage", nil, &st); st != (proto.Storage{Used: 1000, Limit: 800}) {
		t.Fatalf("bob's storage: %+v", st)
	}
	if e.call(&http.Client{}, "GET", "/api/agent/storage", nil, &st, bearer...); st != (proto.Storage{Used: 1000, Limit: 800}) {
		t.Fatalf("agent's storage: %+v", st)
	}

	// Past the limit and the slack, the repo takes no new data, but
	// locks, reads and deletes still work so space can be freed.
	repoReq := func(method, path string) int {
		req, _ := http.NewRequest(method, e.srv.URL+"/repo/"+pc.ID+path, nil)
		req.SetBasicAuth(pc.ID, token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	id := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/data/" + id, http.StatusInsufficientStorage},
		{"POST", "/snapshots/" + id, http.StatusInsufficientStorage},
		{"POST", "/locks/" + id, 200},
		{"GET", "/data/" + id, 200},
		{"GET", "/config", 200},
	} {
		if got := repoReq(c.method, c.path); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, got, c.want)
		}
	}

	// Within the slack (limit 950: 1000 is over, but under 1045), writes go on.
	limit(anna, 950)
	if got := repoReq("POST", "/data/"+id); got != 200 {
		t.Fatalf("within the slack: %d", got)
	}
	// No limit at all.
	limit(anna, 0)
	if got := repoReq("POST", "/data/"+id); got != 200 {
		t.Fatalf("no limit: %d", got)
	}
}
