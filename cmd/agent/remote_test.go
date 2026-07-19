package main

import "testing"

func TestRelayURL(t *testing.T) {
	cases := []struct{ ws, want string }{
		{"ws://192.168.0.40:3000/ws/agent", "ws://192.168.0.40:3000/ws/remote?session=session-id&role=agent"},
		{"wss://rmm.example.com/ws/agent", "wss://rmm.example.com/ws/remote?session=session-id&role=agent"},
	}
	for _, c := range cases {
		a := &agent{wsURL: c.ws}
		if got := a.relayURL("session-id"); got != c.want {
			t.Errorf("relayURL(%q) = %q, want %q", c.ws, got, c.want)
		}
	}
}

func TestLegacyRelayURL(t *testing.T) {
	a := &agent{wsURL: "wss://rmm.example.com/ws/agent"}
	if got, want := a.legacyRelayURL("ticket"), "wss://rmm.example.com/ws/remote?role=agent&token=ticket"; got != want {
		t.Fatalf("legacyRelayURL() = %q, want %q", got, want)
	}
}

func TestSidecarArgsNeverAddsEmptyTicket(t *testing.T) {
	args := sidecarArgs("wss://rmm.example.com/ws/remote", "", []string{"-files"})
	if len(args) != 3 || args[2] != "-files" {
		t.Fatalf("sidecarArgs = %#v, want relay args without -ticket", args)
	}
}

func TestHTTPBase(t *testing.T) {
	cases := []struct{ ws, want string }{
		{"ws://192.168.0.40:3000/ws/agent", "http://192.168.0.40:3000"},
		{"wss://rmm.example.com/ws/agent", "https://rmm.example.com"},
	}
	for _, c := range cases {
		a := &agent{wsURL: c.ws}
		if got := a.httpBase(); got != c.want {
			t.Errorf("httpBase(%q) = %q, want %q", c.ws, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate under cap changed value: %q", got)
	}
	got := truncate("hello world", 5)
	if got[:5] != "hello" || got == "hello world" {
		t.Errorf("truncate over cap = %q, want prefix kept + marker", got)
	}
}
