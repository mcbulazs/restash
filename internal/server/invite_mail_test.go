package server

import (
	"context"
	"net/mail"
	"strings"
	"testing"

	restashmail "github.com/mcbulazs/restash/internal/mail"
	"github.com/mcbulazs/restash/internal/mail/mailtest"
)

func TestInviteEmails(t *testing.T) {
	e := newEnv(t)
	setup, _ := e.store.SetupInvite(context.Background())
	admin := e.client()
	e.signup(admin, setup.Token, "anna@example.com", "hunter2 hunter2")

	// Without email set up, invites are links only.
	var people peopleJSON
	if e.call(admin, "GET", "/api/people", nil, &people); people.Mail {
		t.Fatal("mail on without a server")
	}
	var inv inviteJSON
	if s := e.call(admin, "POST", "/api/invites", map[string]string{"email": "bob@example.com"}, &inv); s != 200 || inv.Emailed || inv.EmailError != "" {
		t.Fatalf("invite without mail: %d %+v", s, inv)
	}
	if s := e.call(admin, "POST", "/api/invites/"+inv.Token+"/send", nil, nil); s != 400 {
		t.Fatalf("send without mail: %d", s)
	}

	// With email set up.
	smtp := mailtest.New(t)
	sender, err := restashmail.New(restashmail.Config{Addr: smtp.Addr, From: "restash <no-reply@example.com>", TLS: restashmail.None})
	if err != nil {
		t.Fatal(err)
	}
	e.server.mail = sender
	if e.call(admin, "GET", "/api/people", nil, &people); !people.Mail {
		t.Fatal("mail off with a server")
	}
	if s := e.call(admin, "POST", "/api/invites", map[string]string{"email": "Carl@Example.com"}, &inv); s != 200 || !inv.Emailed {
		t.Fatalf("invite with mail: %d %+v", s, inv)
	}
	got := smtp.Wait(1)
	if len(got) != 1 || got[0].To[0] != "carl@example.com" {
		t.Fatalf("sent: %+v", got)
	}
	msg, err := mail.ReadMessage(strings.NewReader(got[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if rt := msg.Header.Get("Reply-To"); rt != "<anna@example.com>" {
		t.Errorf("reply-to %q", rt)
	}
	// Quoted-printable may wrap long lines, so look for the token, which is
	// short enough to stay on one line.
	if !strings.Contains(got[0].Data, inv.Token) || !strings.Contains(got[0].Data, "restash.test") {
		t.Errorf("link missing from the email:\n%s", got[0].Data)
	}

	// Sending again.
	if s := e.call(admin, "POST", "/api/invites/"+inv.Token+"/send", nil, nil); s != 204 {
		t.Fatalf("send again: %d", s)
	}
	if got := smtp.Wait(2); len(got) != 2 {
		t.Fatalf("send again didn't send: %d", len(got))
	}
	if s := e.call(admin, "POST", "/api/invites/nope/send", nil, nil); s != 404 {
		t.Fatalf("send unknown invite: %d", s)
	}

	// When sending fails, the invite is still made and the link returned.
	smtp.Reject("550 mailbox unavailable")
	inv = inviteJSON{}
	if s := e.call(admin, "POST", "/api/invites", map[string]string{"email": "dora@example.com"}, &inv); s != 200 || inv.Emailed ||
		!strings.Contains(inv.EmailError, "mailbox unavailable") || !strings.Contains(inv.Link, "/signup?invite=") {
		t.Fatalf("failed send: %d %+v", s, inv)
	}
	if s := e.call(admin, "POST", "/api/invites/"+inv.Token+"/send", nil, nil); s != 502 {
		t.Fatalf("failed send again: %d", s)
	}

	// Members can't send invites.
	smtp.Reject("")
	bob := e.client()
	e.call(admin, "GET", "/api/people", nil, &people)
	for _, i := range people.Invites {
		if i.Email == "bob@example.com" {
			e.signup(bob, i.Token, "bob@example.com", "bob's password")
		}
	}
	if s := e.call(bob, "POST", "/api/invites/"+inv.Token+"/send", nil, nil); s != 403 {
		t.Fatalf("member sent an invite: %d", s)
	}
}
