package main

import (
	"testing"
	"time"
)

func TestPacerIdleAndResume(t *testing.T) {
	p := newPacer()
	if got := p.interval(true); got != p.active {
		t.Fatalf("sent frame → %v, want active %v", got, p.active)
	}
	// Quiet for longer than idleAfter → idle cadence.
	p.lastAct.Store(time.Now().Add(-idleAfter - time.Second).UnixNano())
	if got := p.interval(false); got != p.idle {
		t.Fatalf("quiet session → %v, want idle %v", got, p.idle)
	}
	// Input (markActivity from another goroutine) restores the active rate.
	p.markActivity()
	if got := p.interval(false); got != p.active {
		t.Fatalf("after input → %v, want active %v", got, p.active)
	}
}

func TestEnvIntClamp(t *testing.T) {
	t.Setenv("X_TUNABLE", "999")
	if got := envInt("X_TUNABLE", 20, 1, 60); got != 60 {
		t.Fatalf("clamp high = %d, want 60", got)
	}
	t.Setenv("X_TUNABLE", "abc")
	if got := envInt("X_TUNABLE", 20, 1, 60); got != 20 {
		t.Fatalf("bad value = %d, want default 20", got)
	}
}
