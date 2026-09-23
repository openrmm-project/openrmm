package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What the sidecar writes about itself has to answer, from the log alone, the
// questions a remote-desktop failure raises: which build and which code
// identity was running, what the OS was asked for, and what came back. Before
// this, a Mac's log was 3.8 MB of one capture error repeated every half second,
// with nothing saying whether the bundle was signed or what had been requested.

// maxLogBytes bounds rmm-remote.log. One previous file is kept beside it.
const maxLogBytes = 5 << 20

// rotateLog moves an oversized log aside before it is opened for appending.
func rotateLog(path string, max int64) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= max {
		return
	}
	old := path + ".1"
	_ = os.Remove(old) // Windows will not rename over an existing file
	_ = os.Rename(path, old)
}

// repeatLog keeps a failure that repeats on every frame from burying everything
// else. The first occurrence is written at once; repeats of the same message
// are counted and summarised at most once per interval; and the moment it
// stops is written too, because the recovery dates the fix as precisely as the
// first line dates the fault.
type repeatLog struct {
	every time.Duration
	now   func() time.Time
	logf  func(format string, args ...any)

	msg       string
	pending   int // repeats not yet reported
	total     int
	first     time.Time
	lastWrite time.Time
}

func newRepeatLog(every time.Duration, logf func(string, ...any)) *repeatLog {
	return &repeatLog{every: every, now: time.Now, logf: logf}
}

func (r *repeatLog) fail(msg string) {
	now := r.now()
	if msg != r.msg {
		r.flush()
		r.msg, r.pending, r.total, r.first, r.lastWrite = msg, 0, 1, now, now
		r.logf("%s", msg)
		return
	}
	r.total++
	r.pending++
	if now.Sub(r.lastWrite) >= r.every {
		r.logf("%s (repeated %d more times in the last %s)", msg, r.pending, now.Sub(r.lastWrite).Round(time.Second))
		r.pending = 0
		r.lastWrite = now
	}
}

// ok marks the operation as working again. Cheap enough to call on every frame.
func (r *repeatLog) ok() {
	if r.msg == "" {
		return
	}
	r.logf("recovered after %d failures over %s: %s", r.total, r.now().Sub(r.first).Round(time.Millisecond), r.msg)
	r.msg, r.pending, r.total = "", 0, 0
}

func (r *repeatLog) flush() {
	if r.msg != "" && r.pending > 0 {
		r.logf("%s (repeated %d more times)", r.msg, r.pending)
	}
}

// bundleIdentity says which macOS code identity the running build carries, read
// from the marker the bundle builder writes when a certificate signed it.
// Privacy grants follow that identity, so it is the first thing to know when a
// Mac asks for them again.
func bundleIdentity(exe string) string {
	slashed := filepath.ToSlash(exe)
	i := strings.Index(slashed, ".app/Contents/MacOS/")
	if i < 0 {
		return "not in an app bundle"
	}
	bundle := filepath.FromSlash(slashed[:i+len(".app")])
	b, err := os.ReadFile(filepath.Join(bundle, "Contents", "Resources", "StableCodeIdentity"))
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "ad-hoc identity (privacy grants do not survive an update)"
	}
	return "certificate identity: " + strings.TrimSpace(string(b))
}

// dialHint explains the one relay failure whose wording points the wrong way.
// macOS reports a connection refused by its Local Network permission as a
// routing error, and a new code identity — the first run after an update that
// changed it — has no Local Network grant yet.
func dialHint(goos string, err error) string {
	if err == nil {
		return ""
	}
	if goos == "darwin" && strings.Contains(err.Error(), "no route to host") {
		return " (on macOS this is usually the Local Network permission, not the network: allow OpenRMM Remote under Privacy & Security ▸ Local Network, or reopen the session once the prompt has been answered)"
	}
	return ""
}

// describePermissions is the one-line summary written when a session starts.
func describePermissions(s permissionStart) string {
	state := func(ok bool) string {
		if ok {
			return "allowed"
		}
		return "missing"
	}
	input := state(s.input)
	if s.inputDeferred {
		input = "missing, asked after Screen Recording is answered"
	}
	return fmt.Sprintf("screen recording %s, accessibility %s", state(s.screen), input)
}

// dialRetryable reports a relay dial failure worth another attempt. There is
// one: on macOS, Local Network access is enforced per executable UUID, and the
// first run of a new build — every update — dials before the system has put
// the new UUID in its cache. The permission is intact ("Local network allowed
// by preference … Clearing cached UUIDs and restarting session", nehelper,
// macOS 15.8), the refusal is a race, and the cache is rebuilt within about
// half a second. Giving up on the first attempt turned every update's first
// session into a black screen. The connection is refused before the relay is
// reached, so the single-use ticket has not been spent.
func dialRetryable(goos string, err error) bool {
	return err != nil && goos == "darwin" && strings.Contains(err.Error(), "no route to host")
}

// retryDial calls dial until it succeeds, fails in a way retryable does not
// accept, or runs out of attempts, doubling the pause each time.
func retryDial[T any](dial func() (T, error), retryable func(error) bool, attempts int, wait time.Duration, sleep func(time.Duration), logf func(string, ...any)) (T, error) {
	var conn T
	var err error
	for attempt := 1; ; attempt++ {
		conn, err = dial()
		if err == nil {
			if attempt > 1 {
				logf("rmm-remote: relay reached on attempt %d", attempt)
			}
			return conn, nil
		}
		if attempt >= attempts || !retryable(err) {
			return conn, err
		}
		logf("rmm-remote: dial relay attempt %d: %v; retrying in %s", attempt, err, wait)
		sleep(wait)
		wait *= 2
	}
}
