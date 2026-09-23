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
// It does not wait for the answer, because this process could not use it: on
// macOS 15.8 an Accessibility grant reaches only processes started after it.
// The sidecar that asked kept reporting it as missing half an hour later, and
// Force Quit failed there, while the next session's sidecar used it at once.
// So the viewer is told to reopen the session, which is the truth.
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
	macInputBlocked   = "Allow Accessibility for OpenRMM in System Settings → Privacy & Security, then reopen the session. If it already appears enabled, turn it off and on again to replace the stale grant. Without it no keyboard, mouse, paste, or Force Quit shortcut reaches this Mac."
	macInputPending   = "macOS asks for Accessibility once Screen Recording has been answered at the Mac. After it is allowed, reopen the session: macOS applies it to the next session, not to this one."
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
