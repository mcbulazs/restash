// Package mail sends email through an SMTP server. restash only sends a few
// kinds of email (invites), so this is a small client on top of net/smtp.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// TLS modes.
const (
	StartTLS = "starttls" // plain connection, upgraded with STARTTLS (port 587)
	TLS      = "tls"      // TLS from the start (port 465)
	None     = "none"     // no TLS; only for a relay on a private network
)

// Config says how to reach the SMTP server.
type Config struct {
	Addr     string // host or host:port; the port defaults to 587, or 465 for TLS
	From     string // e.g. "restash <no-reply@example.com>"
	Username string // optional
	Password string
	TLS      string // StartTLS (default), TLS or None
}

// Sender sends email with one Config.
type Sender struct {
	addr, host string
	from       *mail.Address
	auth       smtp.Auth
	tls        string
	timeout    time.Duration
}

// New checks cfg and returns a Sender for it.
func New(cfg Config) (*Sender, error) {
	mode := strings.ToLower(cfg.TLS)
	switch mode {
	case "":
		mode = StartTLS
	case StartTLS, TLS, None:
	default:
		return nil, fmt.Errorf("the TLS mode must be %s, %s or %s", StartTLS, TLS, None)
	}
	if cfg.Addr == "" {
		return nil, errors.New("no SMTP server set")
	}
	addr := cfg.Addr
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port := "587"
		if mode == TLS {
			port = "465"
		}
		addr = net.JoinHostPort(host, port)
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil || cfg.From == "" {
		return nil, fmt.Errorf("the sender must be an address like \"restash <no-reply@example.com>\"")
	}
	s := &Sender{addr: addr, host: host, from: from, tls: mode, timeout: 20 * time.Second}
	if cfg.Username != "" {
		// PlainAuth refuses to send the password without TLS, except to
		// localhost.
		s.auth = smtp.PlainAuth("", cfg.Username, cfg.Password, host)
	}
	return s, nil
}

// Addr is the server's host:port.
func (s *Sender) Addr() string { return s.addr }

// From is the sender address.
func (s *Sender) From() string { return s.from.String() }

// Message is one email.
type Message struct {
	To      string // one address
	ReplyTo string // optional
	Subject string
	Text    string // plain text body
	HTML    string // optional HTML body; sent alongside the text
}

// Send delivers m to the SMTP server.
func (s *Sender) Send(ctx context.Context, m Message) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("bad recipient address: %w", err)
	}
	data, err := s.build(m, to)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if s.tls == TLS {
		conn = tls.Client(conn, &tls.Config{ServerName: s.host})
	}

	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello(domainOf(s.from.Address)); err != nil {
		return err
	}
	if s.tls == StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s doesn't offer STARTTLS; if it's a relay on a private network, turn TLS off", s.addr)
		}
		if err := c.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
			return err
		}
	}
	if s.auth != nil {
		if err := c.Auth(s.auth); err != nil {
			return err
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// build writes the message: headers, then plain text, or text and HTML as
// multipart/alternative.
func (s *Sender) build(m Message, to *mail.Address) ([]byte, error) {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", s.from.String())
	h("To", to.String())
	if m.ReplyTo != "" {
		rt, err := mail.ParseAddress(m.ReplyTo)
		if err != nil {
			return nil, fmt.Errorf("bad reply-to address: %w", err)
		}
		h("Reply-To", rt.String())
	}
	h("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject)))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+randomID()+"@"+domainOf(s.from.Address)+">")
	h("MIME-Version", "1.0")

	if m.HTML == "" {
		h("Content-Type", "text/plain; charset=utf-8")
		h("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, m.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	mw := multipart.NewWriter(&b)
	h("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	b.WriteString("\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeQP(pw, part.body); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeQP(w interface{ Write([]byte) (int, error) }, s string) error {
	qp := quotedprintable.NewWriter(w)
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(s)); err != nil {
		return err
	}
	return qp.Close()
}

// oneLine keeps header values on one line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func domainOf(addr string) string {
	if at := strings.LastIndexByte(addr, '@'); at >= 0 {
		return addr[at+1:]
	}
	return "localhost"
}

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
