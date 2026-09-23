package main

import "time"

// The order in which a Mac is asked for its privacy permissions, kept out of the
// darwin files on purpose: the rules are about sequencing, not about macOS, and
// this way every CI runner exercises them instead of only the one billed ten
// times over.

// permissionProbe is how the macOS engine talks to TCC. The request functions
// check first and only then ask, so calling one for a permission that is
// already allowed puts nothing on the screen.
type permissionProbe struct {
	screenGranted func() bool
	inputGranted  func() bool
	requestScreen func() bool
	requestInput  func() bool
}

// permissionStart is what is known when the session opens.
type permissionStart struct {
	screen bool
	input  bool
	// inputDeferred: Accessibility is missing and has not been asked for yet,
	// because the Screen Recording prompt was on screen. followInput asks later.
	inputDeferred bool
}

// startPermissions asks for Screen Recording and, only when that needed no
// answer from the person at the Mac, for Accessibility as well.
//
// macOS shows one privacy prompt at a time, and a second request made while the
// first is on screen is dropped without a trace: no prompt, no row in the
// privacy database, and the app never appears in the Accessibility list. That
// is what an operator ran into on macOS 15.8 — Screen Recording allowed, nothing
// to allow for Accessibility, until the session was reopened and the sidecar
// asked again on its own.
func startPermissions(p permissionProbe) permissionStart {
	s := permissionStart{screen: p.requestScreen()}
	if s.screen {
		s.input = p.requestInput()
		return s
	}
	// Allowed in an earlier session: there is nothing to ask and nothing to wait for.
	s.input = p.inputGranted()
	s.inputDeferred = !s.input
	return s
}

// permissionEvent is what followInput reports, for the log and the viewer.
type permissionEvent struct {
	kind  string // "screen-allowed", "asked", "granted"
	after time.Duration
	// screenAnswered, on "asked": Screen Recording was allowed before the wait
	// ran out. When false the prompt may simply have been dismissed or denied.
	screenAnswered bool
}

// followInput makes the request startPermissions deferred, once Screen
// Recording has been allowed — or once screenWait has passed, since a dismissed
// or denied prompt leaves nothing to observe.
//
// It does not wait for the answer, because this process cannot see it: on
// macOS 15.8 CGPreflightPostEventAccess keeps answering "denied" in the process
// that asked, long after the grant. The grant itself does apply — mouse and
// keyboard work in that same session — so nothing is gated on the check and
// the viewer is not told to reopen.
func followInput(p permissionProbe, s permissionStart, done <-chan struct{}, poll, screenWait time.Duration, report func(permissionEvent)) {
	if s.input || !s.inputDeferred {
		return
	}
	stopped := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	// waitFor polls cond until it holds, the limit passes, or the session ends.
	waitFor := func(cond func() bool, limit time.Duration) bool {
		start := time.Now()
		tick := time.NewTicker(poll)
		defer tick.Stop()
		for {
			if cond() {
				return true
			}
			if time.Since(start) >= limit {
				return false
			}
			select {
			case <-done:
				return false
			case <-tick.C:
			}
		}
	}

	start := time.Now()
	answered := waitFor(p.screenGranted, screenWait)
	// A prompt raised after the session has ended would ask the person at the
	// Mac about a technician who is no longer there.
	if stopped() {
		return
	}
	// Unlike Accessibility, Screen Recording does reach the running process:
	// capture resumed seconds after it was allowed. The viewer's "capture
	// blocked" banner has to go as soon as the picture is back.
	if answered {
		report(permissionEvent{kind: "screen-allowed", after: time.Since(start)})
	}
	if p.requestInput() {
		report(permissionEvent{kind: "granted", after: time.Since(start)})
		return
	}
	report(permissionEvent{kind: "asked", after: time.Since(start), screenAnswered: answered})
}

const (
	macCaptureBlocked = "Allow Screen & System Audio Recording for OpenRMM in System Settings → Privacy & Security, then reopen the session. If it already appears enabled, turn it off and on again to replace the stale grant."
	macInputBlocked   = "Allow Accessibility for OpenRMM in System Settings → Privacy & Security; keyboard, mouse, paste and shortcuts work as soon as it is allowed. If it already appears enabled, turn it off and on again to replace the stale grant."
	macInputPending   = "macOS asks for Accessibility once Screen Recording has been answered at the Mac. Keyboard, mouse, paste and shortcuts work as soon as it is allowed."
)

// macHello is the session greeting. It is sent again whenever what it says
// changes, and the viewer simply adopts the newest one.
func macHello(version string, allowFiles, screen, input, inputPending bool) map[string]any {
	hello := map[string]any{"t": "hello", "v": version, "allow_files": allowFiles, "cad": input, "chat": true}
	if !screen {
		hello["capture_error"] = macCaptureBlocked
	}
	if !input {
		if inputPending {
			hello["cad_error"] = macInputPending
		} else {
			hello["cad_error"] = macInputBlocked
		}
	}
	return hello
}
