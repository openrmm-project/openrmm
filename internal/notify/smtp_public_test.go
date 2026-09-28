package notify

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8":          true,
		"54.205.195.44":    true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"10.1.2.3":         false,
		"172.16.0.1":       false,
		"192.168.1.10":     false,
		"169.254.169.254":  false, // cloud metadata
		"100.64.0.1":       false, // carrier-grade NAT
		"0.0.0.0":          false,
		"255.255.255.255":  false,
		"::1":              false,
		"fd00::1":          false, // IPv6 ULA
		"fe80::1":          false,
		"::ffff:127.0.0.1": false, // IPv4-mapped loopback
	} {
		if got := PublicIP(net.ParseIP(addr)); got != want {
			t.Errorf("PublicIP(%s) = %v, want %v", addr, got, want)
		}
	}
}

// PublicOnly is enforced on the address actually dialled, so nothing reaches a
// private listener — not even a TCP handshake that a port scan could time.
func TestSendPublicOnlyNeverReachesAPrivateAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var conns atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			_ = c.Close()
		}
	}()
	cfg := Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, From: "a@b.test", TLSMode: "none", Timeout: 2 * time.Second}

	cfg.PublicOnly = true
	err = Send(cfg, Message{To: "x@example.test", Subject: "s", Body: "b"})
	if !errors.Is(err, ErrNotPublic) {
		t.Fatalf("PublicOnly send to loopback: %v, want ErrNotPublic", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := conns.Load(); n != 0 {
		t.Fatalf("the refused send still connected (%d)", n)
	}

	// Without the flag — the operator's own relay — the same address is dialled.
	cfg.PublicOnly = false
	_ = Send(cfg, Message{To: "x@example.test", Subject: "s", Body: "b"})
	deadline := time.Now().Add(2 * time.Second)
	for conns.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if conns.Load() == 0 {
		t.Fatal("without PublicOnly the relay was not dialled")
	}
}
