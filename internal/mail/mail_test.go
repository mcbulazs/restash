package mail

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"

	"github.com/mcbulazs/restash/internal/mail/mailtest"
)

func TestNewChecksConfig(t *testing.T) {
	for _, c := range []Config{
		{Addr: "", From: "a@example.com"},
		{Addr: "smtp.example.com", From: ""},
		{Addr: "smtp.example.com", From: "not an address"},
		{Addr: "smtp.example.com", From: "a@example.com", TLS: "maybe"},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%+v: no error", c)
		}
	}
	for _, c := range []struct {
		cfg  Config
		addr string
	}{
		{Config{Addr: "smtp.example.com", From: "a@example.com"}, "smtp.example.com:587"},
		{Config{Addr: "smtp.example.com", From: "a@example.com", TLS: "TLS"}, "smtp.example.com:465"},
		{Config{Addr: "relay:25", From: "a@example.com", TLS: "none"}, "relay:25"},
	} {
		s, err := New(c.cfg)
		if err != nil || s.Addr() != c.addr {
			t.Errorf("%+v: %v %v", c.cfg, s, err)
		}
	}
}

func TestSend(t *testing.T) {
	srv := mailtest.New(t)
	s, err := New(Config{Addr: srv.Addr, From: "restash <no-reply@example.com>", TLS: None})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(context.Background(), Message{
		To:      "bob@example.com",
		ReplyTo: "anna@example.com",
		Subject: "Anna Ó\r\nBcc: evil@example.com invited you",
		Text:    "Hi Bob,\n.\nhttps://restash.test/signup?invite=abc",
		HTML:    `<p>Hi Bob, <a href="https://restash.test/signup?invite=abc">join</a></p>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || got[0].From != "no-reply@example.com" || len(got[0].To) != 1 || got[0].To[0] != "bob@example.com" {
		t.Fatalf("envelope: %+v", got)
	}
	msg, err := mail.ReadMessage(strings.NewReader(got[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Bcc") != "" {
		t.Fatal("a header got injected through the subject")
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subject != "Anna Ó Bcc: evil@example.com invited you" {
		t.Errorf("subject: %q", subject)
	}
	for k, want := range map[string]string{"From": `"restash" <no-reply@example.com>`, "To": "<bob@example.com>", "Reply-To": "<anna@example.com>"} {
		if got := msg.Header.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if !strings.HasSuffix(msg.Header.Get("Message-Id"), "@example.com>") || msg.Header.Get("Date") == "" {
		t.Errorf("missing Message-ID or Date: %v", msg.Header)
	}

	typ, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if typ != "multipart/alternative" {
		t.Fatalf("content type %q", typ)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart() // decodes quoted-printable
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(body))
	}
	if len(parts) != 2 ||
		parts[0] != "text/plain; charset=utf-8|Hi Bob,\r\n.\r\nhttps://restash.test/signup?invite=abc" ||
		!strings.HasPrefix(parts[1], "text/html; charset=utf-8|<p>Hi Bob") {
		t.Fatalf("parts: %q", parts)
	}
}

func TestSendPlainText(t *testing.T) {
	srv := mailtest.New(t)
	s, _ := New(Config{Addr: srv.Addr, From: "no-reply@example.com", TLS: None})
	if err := s.Send(context.Background(), Message{To: "bob@example.com", Subject: "hi", Text: "just text"}); err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(srv.Messages()[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(msg.Body)
	if !strings.HasPrefix(msg.Header.Get("Content-Type"), "text/plain") || strings.TrimRight(string(body), "\r\n") != "just text" {
		t.Fatalf("%v %q", msg.Header, body)
	}
}

func TestSendErrors(t *testing.T) {
	srv := mailtest.New(t)

	// STARTTLS is required unless turned off; the fake server doesn't offer it.
	s, _ := New(Config{Addr: srv.Addr, From: "no-reply@example.com"})
	if err := s.Send(context.Background(), Message{To: "bob@example.com", Text: "x"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("no STARTTLS: %v", err)
	}

	s, _ = New(Config{Addr: srv.Addr, From: "no-reply@example.com", TLS: None})
	srv.Reject("550 no such user")
	if err := s.Send(context.Background(), Message{To: "bob@example.com", Text: "x"}); err == nil || !strings.Contains(err.Error(), "no such user") {
		t.Fatalf("rejected: %v", err)
	}
	if err := s.Send(context.Background(), Message{To: "not an address", Text: "x"}); err == nil {
		t.Fatal("bad address sent")
	}
	if len(srv.Messages()) != 0 {
		t.Fatal("a message got through")
	}
}
