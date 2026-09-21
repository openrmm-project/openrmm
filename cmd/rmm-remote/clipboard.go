package main

import (
	"log"
	"sync"
	"time"
)

// The endpoint's clipboard, mirrored to the technician's while a session is
// open, so Ctrl+C on the remote screen produces something they can paste at
// home. The reverse direction (paste) is in the "clip" message handler of each
// media engine.
//
// This is a read of the endpoint user's clipboard. It is deliberate and it is
// what an operator expects of a remote-control session, but it is worth being
// plain about: while a session is open, what that person copies reaches the
// technician, the same way their screen already does. It stops when the session
// does — nothing is watched outside one.
const (
	clipboardPollInterval = 700 * time.Millisecond
	clipboardMaxBytes     = 256 * 1024
)

// clipboardMirror remembers what has already crossed in either direction, so a
// value is never bounced back to where it came from. Without it, pasting into
// the endpoint would immediately be read back and sent to the viewer, and a
// clipboard that changes on its own is worse than one that does nothing.
type clipboardMirror struct {
	mu   sync.Mutex
	seen string
}

func (m *clipboardMirror) sameAsLast(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen == text
}

func (m *clipboardMirror) remember(text string) {
	m.mu.Lock()
	m.seen = text
	m.mu.Unlock()
}

// watchClipboard polls the endpoint's clipboard and hands each new value to
// send. Polling rather than subscribing because two of the three platforms have
// no usable change notification for an unfocused process, and a 700ms poll of a
// string costs nothing next to the video encoder running beside it.
func watchClipboard(mirror *clipboardMirror, send func(string), done <-chan struct{}) {
	ticker := time.NewTicker(clipboardPollInterval)
	defer ticker.Stop()
	reportedFailure := false
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		text, err := readClipboard()
		if err != nil {
			// Logged once: an endpoint with no clipboard tool would otherwise
			// fill the log with the same line twice a second.
			if !reportedFailure {
				log.Printf("rmm-remote: clipboard mirror unavailable: %v", err)
				reportedFailure = true
			}
			continue
		}
		reportedFailure = false
		if text == "" || len(text) > clipboardMaxBytes || mirror.sameAsLast(text) {
			continue
		}
		mirror.remember(text)
		send(text)
	}
}
