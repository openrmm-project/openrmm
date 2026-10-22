package api

import (
	"encoding/json"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// One conversation across a technician's session and their guests'.
//
// A guest's desktop session is a session of its own, and every session's
// sidecar opens its own chat window on the endpoint (sidecars before 0.18 do,
// whatever the session). Left there, the person at the machine would get a
// second window that reaches nobody, and the technician and the guest could
// not talk to each other. So the server carries chat lines between the
// technician's session on a device and the sessions of the guests they let
// onto it:
//
//   - a guest writes: the line goes to the technician's session — to its
//     window on the endpoint, labelled with the guest's name, and to the
//     technician's viewer — and to the technician's other guests there;
//   - the technician writes, or the person at the machine writes in the
//     technician's window: every guest of that technician there gets it;
//   - the person at the machine writes in a guest's window: the technician and
//     the other guests get it (that guest already has it from its own session).
//
// The relay reports each line once it has gone where it was going
// (remote.ChatHook), and one worker carries them in order, so a guest who is
// slow to read never holds up a session's own traffic.

const (
	// guestChatMax bounds one line from a guest, in characters.
	guestChatMax = 1000
	// guestChatInterval is the shortest time between two lines of one guest:
	// each one lands on the screen of the person at the machine.
	guestChatInterval = 300 * time.Millisecond
	// chatQueue lines wait for the worker at most; past that they are dropped.
	chatQueue = 256
)

type chatLine struct {
	agentID, sessionID, role, text string
}

// queueChat is the relay's ChatHook. It runs on a session's copy loop, so it
// only hands the line over.
func (s *Server) queueChat(agentID, sessionID, role, text string) {
	g := s.guests
	if g == nil || !g.anyLive() {
		return
	}
	g.chatOnce.Do(func() {
		g.chat = make(chan chatLine, chatQueue)
		go func() {
			for line := range g.chat {
				s.carryChat(line)
			}
		}()
	})
	select {
	case g.chat <- chatLine{agentID: agentID, sessionID: sessionID, role: role, text: text}:
	default:
	}
}

// carryChat delivers one line to the sessions that share its conversation.
func (s *Server) carryChat(line chatLine) {
	viewer, ok := s.presence.Session(line.agentID, line.sessionID)
	if !ok || viewer.Kind != "native" {
		return
	}
	if shareID, isGuest := models.GuestShareID(viewer.User); isGuest {
		s.carryGuestChat(line, shareID)
		return
	}
	guests := s.guests.sponsoredBy(line.agentID, viewer.User)
	if len(guests) == 0 {
		return
	}
	from := "user"
	if line.role == "viewer" {
		from = "tech"
	}
	frame := chatFrame(from, "", cleanChatLine(line.text))
	for _, id := range guests {
		s.sendToGuest(id, frame)
	}
}

// carryGuestChat carries a line from a guest's session: written by the guest,
// or by the person at the machine in that session's own window.
func (s *Server) carryGuestChat(line chatLine, shareID string) {
	gs := s.guests.session(shareID)
	if gs == nil || gs.sessionID != line.sessionID || gs.agentID != line.agentID {
		return
	}
	text := cleanChatLine(line.text)
	if text == "" {
		return
	}
	var toViewers []byte
	if line.role == "viewer" {
		if !s.guests.allowChat(shareID, time.Now()) {
			return
		}
		toViewers = chatFrame("guest", gs.name, text)
		// The endpoint's window labels every line from the console side as the
		// technician's, so the guest's name goes in the line itself.
		toEndpoint := chatFrame("tech", "", gs.name+" (guest): "+text)
		for _, tech := range s.presence.ForUser(gs.agentID, gs.sponsor, "native") {
			_ = s.relay.Send(tech.SessionID, "agent", toEndpoint)
		}
	} else {
		toViewers = chatFrame("user", "", text)
	}
	for _, tech := range s.presence.ForUser(gs.agentID, gs.sponsor, "native") {
		_ = s.relay.Send(tech.SessionID, "viewer", toViewers)
	}
	for _, id := range s.guests.sponsoredBy(gs.agentID, gs.sponsor) {
		if id != shareID {
			s.sendToGuest(id, toViewers)
		}
	}
}

func (s *Server) sendToGuest(shareID string, frame []byte) {
	if gs := s.guests.session(shareID); gs != nil {
		_ = s.relay.Send(gs.sessionID, "viewer", frame)
	}
}

// chatFrame is a chat line as the console and the sidecars read it. name is
// set on a guest's line to the consoles.
func chatFrame(from, name, text string) []byte {
	m := map[string]string{"t": "chat", "from": from, "text": text}
	if name != "" {
		m["name"] = name
	}
	b, _ := json.Marshal(m)
	return b
}

// cleanChatLine keeps a chat line on one line of printable text. One line
// matters on the endpoint, where the window prints each line after a sender
// label: a line break in a guest's message could start a line that seems to
// come from someone else.
func cleanChatLine(raw string) string {
	return cleanLine(raw, guestChatMax)
}
