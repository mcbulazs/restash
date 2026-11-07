package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mcbulazs/restash/internal/proto"
	"github.com/mcbulazs/restash/internal/store"
)

func TestBackupProgress(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inv, _ := e.store.SetupInvite(ctx)
	anna := e.client()
	e.signup(anna, inv.Token, "anna@example.com", "pw")
	users, _ := e.store.Users(ctx)
	code, _, _ := e.store.CreateJoinCode(ctx, users[0].ID)
	pc, token, _ := e.store.Join(ctx, code, store.PC{Name: "laptop"})
	inv2, _ := e.store.CreateInvite(ctx, "bob@example.com", false, users[0].ID, store.InviteLength)
	bob := e.client()
	e.signup(bob, inv2.Token, "bob@example.com", "pw")

	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/agent/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + pc.ID + "." + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	send := func(m proto.Message) {
		if err := wsjson.Write(ctx, c, m); err != nil {
			t.Fatal(err)
		}
	}
	type progress struct {
		Online  bool           `json:"online"`
		Running []progressJSON `json:"running"`
	}
	get := func() progress {
		var p progress
		for i := 0; i < 50; i++ { // messages arrive asynchronously
			p = progress{}
			e.call(anna, "GET", "/api/pcs/"+pc.ID+"/backups/running", nil, &p)
			if p.Online {
				return p
			}
			time.Sleep(20 * time.Millisecond)
		}
		return p
	}
	waitFor := func(n int, pct float64) progress {
		t.Helper()
		var p progress
		for i := 0; i < 50; i++ {
			p = get()
			if len(p.Running) == n && (n == 0 || p.Running[0].Percent == pct) {
				return p
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("wanted %d running at %v, got %+v", n, pct, p)
		return p
	}

	send(proto.Message{Type: proto.TypeHello, Version: "test"})
	send(proto.Message{Type: proto.TypeBackupState, Entry: "docs", State: "running"})
	send(proto.Message{Type: proto.TypeBackupState, Entry: "docs", State: "running", Percent: 0.6, Files: 60, TotalFiles: 100, Bytes: 600, TotalBytes: 1000, ETA: 30})
	p := waitFor(1, 0.6)
	if r := p.Running[0]; r.Entry != "docs" || r.Files != 60 || r.TotalBytes != 1000 || r.ETA != 30 {
		t.Fatalf("progress %+v", r)
	}

	// Nonsense is clamped, not trusted.
	send(proto.Message{Type: proto.TypeBackupState, Entry: "docs", State: "running", Percent: 7, Bytes: -5})
	if r := waitFor(1, 1).Running[0]; r.Bytes != 0 {
		t.Fatalf("clamped %+v", r)
	}

	// Only the owner sees it; the PC page and list carry it too.
	if s := e.call(bob, "GET", "/api/pcs/"+pc.ID+"/backups/running", nil, nil); s != 404 {
		t.Fatalf("bob sees anna's progress: %d", s)
	}
	var list []pcJSON
	e.call(anna, "GET", "/api/pcs", nil, &list)
	if len(list) != 1 || len(list[0].Running) != 1 {
		t.Fatalf("list %+v", list)
	}

	// Done ends it.
	send(proto.Message{Type: proto.TypeBackupState, Entry: "docs", State: "done", Percent: 1})
	waitFor(0, 0)

	// Going offline mid-backup ends it too.
	send(proto.Message{Type: proto.TypeBackupState, Entry: "pics", State: "running", Percent: 0.1})
	waitFor(1, 0.1)
	c.Close(websocket.StatusNormalClosure, "")
	for i := 0; i < 50 && len(e.server.progress.list(pc.ID, time.Now())) > 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(e.server.progress.list(pc.ID, time.Now())); n != 0 {
		t.Fatalf("%d still running after going offline", n)
	}
}

func TestProgressGoesStale(t *testing.T) {
	var ps progressStore
	start := time.Now()
	ps.update("pc", proto.Message{Entry: "docs", State: "running", Percent: 0.5}, start)
	if len(ps.list("pc", start.Add(time.Minute))) != 1 {
		t.Fatal("dropped too early")
	}
	if len(ps.list("pc", start.Add(progressStale+time.Second))) != 0 {
		t.Fatal("stale progress kept")
	}
}
