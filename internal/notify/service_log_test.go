package notify

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(nil); log.SetFlags(flags) })
	return &buf
}

// An unusable transport used to be entirely silent: drain returned early and
// alert mail piled up pending with nothing in the log to explain it. That is the
// failure mode where a misconfiguration stops every alert and nobody finds out.
func TestQuietTransitionIsLoggedOnceEachWay(t *testing.T) {
	buf := captureLog(t)
	s := &Service{}

	s.setQuiet(true, "no mail transport is configured")
	first := buf.String()
	if !strings.Contains(first, "not being delivered") {
		t.Fatalf("entering the quiet state should say so, got %q", first)
	}

	// Every subsequent tick (30s apart) must stay silent, or the one line that
	// matters is buried.
	buf.Reset()
	for i := 0; i < 5; i++ {
		s.setQuiet(true, "no mail transport is configured")
	}
	if buf.Len() != 0 {
		t.Errorf("repeat ticks should not log, got %q", buf.String())
	}

	buf.Reset()
	s.setQuiet(false, "")
	if !strings.Contains(buf.String(), "usable again") {
		t.Errorf("recovery should be logged, got %q", buf.String())
	}

	buf.Reset()
	s.setQuiet(false, "")
	if buf.Len() != 0 {
		t.Errorf("staying healthy should not log, got %q", buf.String())
	}
}

// The reason has to be actionable: "not configured" and "configured wrong" call
// for different fixes, and a half-filled alerts profile is the case that used to
// disappear silently.
func TestQuietReasonDistinguishesTheFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		err  error
		want string
	}{
		{"nothing set", Config{}, nil, "no mail transport is configured"},
		{"sender without host", Config{From: "alerts@example.test"}, nil, "no host"},
		{"host without sender", Config{Host: "relay.example.test"}, nil, "no sender address"},
		{"unreadable settings", Config{}, errors.New("db down"), "cannot read the mail settings"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := quietReason(tt.cfg, tt.err); !strings.Contains(got, tt.want) {
				t.Errorf("quietReason = %q, want it to mention %q", got, tt.want)
			}
		})
	}
	// The host is named so an operator knows which relay to look at.
	if got := quietReason(Config{Host: "relay.example.test"}, nil); !strings.Contains(got, "relay.example.test") {
		t.Errorf("the reason should name the host, got %q", got)
	}
}

// A relay that is down for hours must produce one line, not one per attempt per
// message — but a *different* error is new information and speaks again.
func TestNewErrorDeduplicatesRepeats(t *testing.T) {
	s := &Service{}
	if !s.newError("connection refused") {
		t.Fatal("the first error is new")
	}
	if s.newError("connection refused") {
		t.Error("a repeat of the same error should not log again")
	}
	if !s.newError("535 authentication failed") {
		t.Error("a different error is new information")
	}
}
