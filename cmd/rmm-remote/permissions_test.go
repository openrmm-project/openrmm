package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTCC stands in for the macOS privacy database. A request records that it
// was made; whether it grants anything is up to the test, as it is up to the
// person at the Mac.
type fakeTCC struct {
	screen, input                   atomic.Bool
	screenRequests, inputRequests   atomic.Int32
	grantScreenOnAsk, grantInputAsk bool
}

func (f *fakeTCC) probe() permissionProbe {
	return permissionProbe{
		screenGranted: func() bool { return f.screen.Load() },
		inputGranted:  func() bool { return f.input.Load() },
		requestScreen: func() bool {
			f.screenRequests.Add(1)
			if f.grantScreenOnAsk {
				f.screen.Store(true)
			}
			return f.screen.Load()
		},
		requestInput: func() bool {
			f.inputRequests.Add(1)
			if f.grantInputAsk {
				f.input.Store(true)
			}
			return f.input.Load()
		},
	}
}

func TestStartPermissionsBothAllowed(t *testing.T) {
	f := &fakeTCC{}
	f.screen.Store(true)
	f.input.Store(true)
	s := startPermissions(f.probe())
	if !s.screen || !s.input || s.inputDeferred {
		t.Fatalf("got %+v, want both allowed and nothing deferred", s)
	}
}

// The defect this sequencing exists for: both requests back to back, the
// second one silently dropped while the first prompt is on screen.
func TestStartPermissionsDefersAccessibilityWhileScreenPromptIsUp(t *testing.T) {
	f := &fakeTCC{}
	s := startPermissions(f.probe())
	if s.screen || s.input || !s.inputDeferred {
		t.Fatalf("got %+v, want Accessibility deferred", s)
	}
	if n := f.inputRequests.Load(); n != 0 {
		t.Fatalf("Accessibility was requested %d times while the Screen Recording prompt was showing", n)
	}
}

func TestStartPermissionsAsksAccessibilityWhenScreenNeedsNoAnswer(t *testing.T) {
	f := &fakeTCC{}
	f.screen.Store(true)
	s := startPermissions(f.probe())
	if !s.screen || s.input || s.inputDeferred {
		t.Fatalf("got %+v, want Accessibility asked now, not deferred", s)
	}
	if n := f.inputRequests.Load(); n != 1 {
		t.Fatalf("Accessibility requested %d times, want 1", n)
	}
}

// Screen Recording denied but Accessibility granted in an earlier session:
// nothing to ask, nothing to wait for.
func TestStartPermissionsKeepsAnEarlierAccessibilityGrant(t *testing.T) {
	f := &fakeTCC{}
	f.input.Store(true)
	s := startPermissions(f.probe())
	if s.screen || !s.input || s.inputDeferred {
		t.Fatalf("got %+v", s)
	}
	if n := f.inputRequests.Load(); n != 0 {
		t.Fatalf("an allowed permission was requested again (%d)", n)
	}
}

type events struct {
	mu   sync.Mutex
	list []permissionEvent
}

func (e *events) add(ev permissionEvent) { e.mu.Lock(); e.list = append(e.list, ev); e.mu.Unlock() }
func (e *events) kinds() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var k []string
	for _, ev := range e.list {
		k = append(k, ev.kind)
	}
	return strings.Join(k, ",")
}

const (
	poll = time.Millisecond
	wait = 200 * time.Millisecond
)

func TestFollowInputAsksOnceScreenIsAllowed(t *testing.T) {
	f := &fakeTCC{}
	s := startPermissions(f.probe())
	var got events
	go func() { time.Sleep(20 * time.Millisecond); f.screen.Store(true) }()
	followInput(f.probe(), s, make(chan struct{}), poll, wait, got.add)
	if n := f.inputRequests.Load(); n != 1 {
		t.Fatalf("Accessibility requested %d times, want exactly 1 after Screen Recording", n)
	}
	// The screen event first, so the viewer drops its capture banner before the
	// Accessibility prompt goes up.
	if got.kinds() != "screen-allowed,asked" {
		t.Fatalf("events %q, want screen-allowed,asked", got.kinds())
	}
	if !got.list[1].screenAnswered {
		t.Fatal("the request should record that Screen Recording was allowed first")
	}
}

// It asks and stops there. On macOS 15.8 the check stays "denied" in the
// process that asked, so watching it would wait for something this process
// can never see, even though the grant itself applies.
func TestFollowInputDoesNotWaitForTheGrant(t *testing.T) {
	f := &fakeTCC{}
	f.screen.Store(false)
	s := startPermissions(f.probe())
	var got events
	begin := time.Now()
	followInput(f.probe(), s, make(chan struct{}), poll, 20*time.Millisecond, got.add)
	if time.Since(begin) > time.Second {
		t.Fatalf("followInput kept waiting after it had asked: %s", time.Since(begin))
	}
	if got.kinds() != "asked" {
		t.Fatalf("events %q, want asked", got.kinds())
	}
}

func TestFollowInputAsksAnywayWhenScreenIsNeverAnswered(t *testing.T) {
	// Denied or dismissed: nothing changes to observe, so the wait runs out and
	// Accessibility is still asked for rather than never.
	f := &fakeTCC{grantInputAsk: true}
	s := startPermissions(f.probe())
	var got events
	followInput(f.probe(), s, make(chan struct{}), poll, 20*time.Millisecond, got.add)
	if got.kinds() != "granted" || f.inputRequests.Load() != 1 {
		t.Fatalf("events %q, requests %d", got.kinds(), f.inputRequests.Load())
	}
	if got.list[0].screenAnswered {
		t.Fatal("Screen Recording was never allowed")
	}
}

func TestFollowInputStaysQuietAfterTheSessionEnds(t *testing.T) {
	f := &fakeTCC{}
	s := startPermissions(f.probe())
	done := make(chan struct{})
	close(done)
	var got events
	followInput(f.probe(), s, done, poll, wait, got.add)
	if n := f.inputRequests.Load(); n != 0 {
		t.Fatalf("prompted the Mac %d times for a session that had already ended", n)
	}
	if got.kinds() != "" {
		t.Fatalf("events %q after the session ended", got.kinds())
	}
}

// Asked at session start because Screen Recording needed no answer: nothing is
// deferred, so nothing is asked twice.
func TestFollowInputLeavesAnImmediateRequestAlone(t *testing.T) {
	f := &fakeTCC{}
	f.screen.Store(true)
	s := startPermissions(f.probe())
	var got events
	followInput(f.probe(), s, make(chan struct{}), poll, wait, got.add)
	if f.inputRequests.Load() != 1 || got.kinds() != "" {
		t.Fatalf("requests %d, events %q", f.inputRequests.Load(), got.kinds())
	}
}

func TestFollowInputDoesNothingWhenAlreadyAllowed(t *testing.T) {
	f := &fakeTCC{}
	var got events
	followInput(f.probe(), permissionStart{screen: true, input: true}, make(chan struct{}), poll, wait, got.add)
	if got.kinds() != "" || f.inputRequests.Load() != 0 {
		t.Fatalf("events %q, requests %d", got.kinds(), f.inputRequests.Load())
	}
}

func TestMacHello(t *testing.T) {
	h := macHello("9.9.9", true, true, true, false)
	if h["cad"] != true || h["capture_error"] != nil || h["cad_error"] != nil || h["v"] != "9.9.9" || h["allow_files"] != true {
		t.Fatalf("all allowed: %v", h)
	}
	h = macHello("9.9.9", false, false, false, true)
	if h["capture_error"] != macCaptureBlocked {
		t.Fatalf("capture_error = %v", h["capture_error"])
	}
	// Pending is not blocked: the grant applies to the running session, so
	// telling the technician to reopen would send them round for nothing.
	if h["cad_error"] != macInputPending || strings.Contains(macInputPending, "reopen") {
		t.Fatalf("pending cad_error = %v", h["cad_error"])
	}
	if h = macHello("9.9.9", false, true, false, false); h["cad_error"] != macInputBlocked || h["cad"] != false {
		t.Fatalf("blocked: %v", h)
	}
}
