// Package mailtest is a fake SMTP server for tests. It speaks just enough
// SMTP for net/smtp: no TLS, no auth.
package mailtest

import (
	"bufio"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message is one email the server got.
type Message struct {
	From string
	To   []string
	Data string // headers and body, with CRLF line endings
}

// Server is a running fake SMTP server.
type Server struct {
	Addr string // host:port to send to

	mu       sync.Mutex
	messages []Message
	reject   string // when set, RCPT gets this 5xx reply
}

// New starts a server that stops when the test ends.
func New(t *testing.T) *Server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: l.Addr().String()}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

// Reject makes the server refuse recipients with msg (e.g. "550 no such
// user"); an empty msg accepts them again.
func (s *Server) Reject(msg string) {
	s.mu.Lock()
	s.reject = msg
	s.mu.Unlock()
}

// Messages returns what the server got so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message{}, s.messages...)
}

// Wait waits briefly for n messages and returns what arrived.
func (s *Server) Wait(n int) []Message {
	for i := 0; i < 100; i++ {
		if got := s.Messages(); len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s.Messages()
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	tp := textproto.NewConn(conn)
	reply := func(line string) { tp.PrintfLine("%s", line) }
	reply("220 mailtest ready")
	var m Message
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 mailtest")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			m = Message{From: addr(line[len("MAIL FROM:"):])}
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			s.mu.Lock()
			reject := s.reject
			s.mu.Unlock()
			if reject != "" {
				reply(reject)
				continue
			}
			m.To = append(m.To, addr(line[len("RCPT TO:"):]))
			reply("250 ok")
		case cmd == "DATA":
			reply("354 go ahead")
			data, err := readData(tp.R)
			if err != nil {
				return
			}
			m.Data = data
			s.mu.Lock()
			s.messages = append(s.messages, m)
			s.mu.Unlock()
			reply("250 queued")
		case cmd == "RSET", cmd == "NOOP":
			reply("250 ok")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

// readData reads up to the lone "." line, undoing dot-stuffing.
func readData(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" {
			return b.String(), nil
		}
		b.WriteString(strings.TrimPrefix(line, "."))
	}
}

func addr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i] // drop parameters like BODY=8BITMIME
	}
	return strings.Trim(s, "<>")
}
