// Package smtptest is an SMTP server for tests: it accepts what net/smtp sends
// over a plain connection without authentication, and keeps every message so a
// test can read what was mailed — a confirmation link, a footer, a recipient.
package smtptest

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// Message is one accepted mail.
type Message struct {
	From string
	To   []string
	Data string // headers and body, as sent
}

// Server is a running fake SMTP server on 127.0.0.1.
type Server struct {
	Port int

	mu       sync.Mutex
	messages []Message
}

// New starts a server and stops it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &Server{Port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

// Messages returns a copy of what has been accepted so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 smtptest ready")
	var cur Message
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 smtptest")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			cur = Message{From: strings.Trim(strings.TrimSpace(line)[len("MAIL FROM:"):], "<> \r\n")}
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			cur.To = append(cur.To, strings.Trim(strings.TrimSpace(line)[len("RCPT TO:"):], "<> \r\n"))
			reply("250 ok")
		case cmd == "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" || l == ".\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.Data = b.String()
			s.mu.Lock()
			s.messages = append(s.messages, cur)
			s.mu.Unlock()
			reply("250 accepted")
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
