package remote

import (
	"encoding/json"

	"github.com/gorilla/websocket"
)

// GuestPolicy is what a guest's desktop session may carry, enforced here in
// the relay rather than trusted to the guest's browser or to the endpoint's
// sidecar: the relay is the one piece that sees every frame and that a guest
// cannot replace (docs/08-remote-access.md).
//
// It is an allowlist in both directions. Anything it does not name is dropped:
// files, clipboard, Ctrl+Alt+Delete, the input lock, UAC, and whatever a later
// sidecar adds before this list learns about it. Chat is the one exchange in
// between: the guest's lines never reach this session's endpoint — the server
// carries them to the technician's session instead (ChatHook) — while what the
// person at the machine writes here does reach the guest.
type GuestPolicy struct {
	// Control adds pointer and keyboard input to the view-only set.
	Control bool
}

// Close codes a guest's page reads (relay.go has the operator's). Each tells
// the page why the screen went away, so it can say so instead of reconnecting.
const (
	// CloseGuestEnded: the technician removed the guest, revoked the
	// invitation, or guest access was turned off.
	CloseGuestEnded = 4411
	// CloseGuestSponsorLeft: the technician who let the guest in closed their
	// own session. A guest is never on a device unsupervised.
	CloseGuestSponsorLeft = 4412
	// CloseGuestExpired: the invitation's time ran out.
	CloseGuestExpired = 4413
)

// guestFrameLimit bounds a text frame from a guest. Input events are a few
// dozen bytes; anything larger is not one of them.
const guestFrameLimit = 4096

// Binary channels (cmd/rmm-remote/encoder.go). Only the screen reaches a guest.
const chVideo = 0x01

// frameType reads a JSON frame's "t" exactly the way the sidecar does — into a
// struct field tagged "t" — so a frame with duplicate or differently cased keys
// is judged by the value the endpoint would act on, not by another reading of
// the same bytes.
func frameType(data []byte) (string, bool) {
	var e struct {
		T string `json:"t"`
	}
	if json.Unmarshal(data, &e) != nil {
		return "", false
	}
	return e.T, true
}

// FromViewer reports whether a frame from the guest may reach the endpoint.
func (p GuestPolicy) FromViewer(mt int, data []byte) bool {
	if mt != websocket.TextMessage || len(data) > guestFrameLimit {
		return false
	}
	t, ok := frameType(data)
	if !ok {
		return false
	}
	switch t {
	case "kf", "mon":
		return true
	case "m", "b", "w", "k":
		return p.Control
	}
	return false
}

// FromAgent reports whether a frame from the endpoint may reach the guest: the
// screen itself, and the few messages that describe it.
func (p GuestPolicy) FromAgent(mt int, data []byte) bool {
	if mt == websocket.BinaryMessage {
		return len(data) > 0 && data[0] == chVideo
	}
	if mt != websocket.TextMessage {
		return false
	}
	t, ok := frameType(data)
	if !ok {
		return false
	}
	switch t {
	case "hello", "monitors", "notice", "chat":
		return true
	}
	return false
}
