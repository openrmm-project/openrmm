package remote

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// pairTimeout bounds how long the first peer waits for its partner.
const pairTimeout = 60 * time.Second

// sendTimeout bounds a frame the server adds to a session (Send): a peer that
// cannot take one short line in this long is not reading at all.
const sendTimeout = 5 * time.Second

// chatFrameLimit is the largest text frame read as a chat line for ChatHook.
// A longer one still reaches its peer; it is just not carried anywhere else.
const chatFrameLimit = 16 << 10

// ChatHook hears every chat line a session carries, once it has gone where it
// was going. role is the peer that wrote it: "agent" for the person at the
// machine, "viewer" for whoever is watching. The server uses it to make one
// conversation of a technician's session and their guests' (internal/api).
type ChatHook func(agentID, sessionID, role, text string)

// Relay pairs two WebSocket peers (agent + viewer) for a session id and pipes
// frames between them. Tickets are validated by the API before Join. This is the transport plane of the native
// remote provider — the media engine (screen capture / encoding / input) lives in
// the agent and viewer clients and rides on top of this relay.
type Relay struct {
	mu            sync.Mutex
	pairs         map[string]*pair
	blockedAgents map[string]struct{}
	// closed remembers sessions ended before either peer arrived, so a peer
	// that connects a moment later cannot open them again.
	closed map[string]time.Time
	chat   ChatHook
}

type pair struct {
	mu      sync.Mutex
	agentID string
	agent   *websocket.Conn
	viewer  *websocket.Conn
	// guest is set when the viewer is a guest: every frame between the two
	// peers then passes through its allowlist, in both directions.
	guest *GuestPolicy
	// One writer per connection at a time: the copy loop and a frame the
	// server adds (Send) may both write to the same peer.
	agentW, viewerW sync.Mutex

	ready     chan struct{}
	readyOnce sync.Once
	done      chan struct{}
	doneOnce  sync.Once
	closed    bool
}

// NewRelay creates an empty relay.
func NewRelay() *Relay {
	return &Relay{pairs: map[string]*pair{}, blockedAgents: map[string]struct{}{}, closed: map[string]time.Time{}}
}

// SetChatHook installs the hook for sessions joined from now on.
func (r *Relay) SetChatHook(h ChatHook) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.chat = h
	r.mu.Unlock()
}

func (r *Relay) get(sessionID, agentID string) (*pair, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, blocked := r.blockedAgents[agentID]; blocked {
		return nil, errors.New("relay agent is unavailable")
	}
	if _, ended := r.closed[sessionID]; ended {
		return nil, errors.New("relay session is closed")
	}
	p, ok := r.pairs[sessionID]
	if !ok {
		p = &pair{agentID: agentID, ready: make(chan struct{}), done: make(chan struct{})}
		r.pairs[sessionID] = p
	}
	return p, nil
}

func (p *pair) close() []*websocket.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.doneOnce.Do(func() { close(p.done) })
	conns := make([]*websocket.Conn, 0, 2)
	if p.agent != nil {
		conns = append(conns, p.agent)
	}
	if p.viewer != nil {
		conns = append(conns, p.viewer)
	}
	return conns
}

func closeConnections(conns []*websocket.Conn) {
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (r *Relay) drop(sessionID string, p *pair) {
	r.mu.Lock()
	if cur, ok := r.pairs[sessionID]; ok && cur == p {
		delete(r.pairs, sessionID)
	}
	r.mu.Unlock()
	closeConnections(p.close())
}

// Join registers a peer for a session and, once both peers are present,
// pipes this peer's inbound frames to the other peer. It blocks until the
// session ends. role is "agent" or "viewer".
func (r *Relay) Join(sessionID, agentID, role string, conn *websocket.Conn) error {
	return r.join(sessionID, agentID, role, conn, nil)
}

// JoinGuest is Join for a guest's viewer. The policy is attached before the
// pair can be ready, so not one frame crosses unfiltered.
func (r *Relay) JoinGuest(sessionID, agentID string, conn *websocket.Conn, policy GuestPolicy) error {
	return r.join(sessionID, agentID, "viewer", conn, &policy)
}

func (r *Relay) join(sessionID, agentID, role string, conn *websocket.Conn, guest *GuestPolicy) error {
	p, err := r.get(sessionID, agentID)
	if err != nil {
		return err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("relay session is closed")
	}
	if role == "viewer" {
		if p.viewer != nil {
			p.mu.Unlock()
			return errors.New("relay viewer already connected")
		}
		p.viewer = conn
		p.guest = guest
	} else {
		if p.agent != nil {
			p.mu.Unlock()
			return errors.New("relay agent already connected")
		}
		p.agent = conn
	}
	both := p.agent != nil && p.viewer != nil
	p.mu.Unlock()
	if both {
		p.readyOnce.Do(func() { close(p.ready) })
	}

	select {
	case <-p.ready:
	case <-p.done:
		return errors.New("relay session is closed")
	case <-time.After(pairTimeout):
		r.drop(sessionID, p)
		return errors.New("relay pairing timed out")
	}

	// Pipe inbound frames from this conn to the partner conn.
	r.mu.Lock()
	hook := r.chat
	r.mu.Unlock()
	var onChat func(string)
	if hook != nil {
		onChat = func(text string) { hook(agentID, sessionID, role, text) }
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("relay session is closed")
	}
	var dst *websocket.Conn
	var dstW *sync.Mutex
	var allow func(int, []byte) bool
	if role == "viewer" {
		dst, dstW = p.agent, &p.agentW
		if p.guest != nil {
			allow = p.guest.FromViewer
		}
	} else {
		dst, dstW = p.viewer, &p.viewerW
		if p.guest != nil {
			allow = p.guest.FromAgent
		}
	}
	p.mu.Unlock()
	err = copyFrames(conn, dst, dstW, allow, onChat)
	if err != nil {
		log.Printf("remote relay session=%s role=%s ended: %v", sessionID, role, err)
	}
	r.drop(sessionID, p)
	return err
}

// Send writes one text frame to a peer of a live session ("agent" or
// "viewer"): a chat line the server carries over from another session.
func (r *Relay) Send(sessionID, role string, data []byte) error {
	r.mu.Lock()
	p := r.pairs[sessionID]
	r.mu.Unlock()
	if p == nil {
		return errors.New("relay session not found")
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("relay session is closed")
	}
	conn, w := p.viewer, &p.viewerW
	if role == "agent" {
		conn, w = p.agent, &p.agentW
	}
	p.mu.Unlock()
	if conn == nil {
		return errors.New("relay peer is not connected")
	}
	w.Lock()
	defer w.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(sendTimeout))
	err := conn.WriteMessage(websocket.TextMessage, data)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

// CloseAgent terminates every active native relay for a revoked endpoint.
func (r *Relay) CloseAgent(agentID string) {
	r.mu.Lock()
	r.blockedAgents[agentID] = struct{}{}
	pairs := make([]*pair, 0)
	for id, p := range r.pairs {
		if p.agentID == agentID {
			delete(r.pairs, id)
			pairs = append(pairs, p)
		}
	}
	r.mu.Unlock()
	for _, p := range pairs {
		closeConnections(p.close())
	}
}

// Close codes the console acts on. A viewer closed with one of these must not
// reconnect by itself: the operator has the session somewhere else.
const (
	// CloseSessionActive: the operator already has a desktop session on this
	// endpoint, and this one was refused.
	CloseSessionActive = 4409
	// CloseSessionReplaced: the operator moved the session to another tab.
	CloseSessionReplaced = 4410
)

// CloseViewer ends a viewer's socket with a code the console reads, rather
// than a bare drop that its reconnect loop would retry.
func CloseViewer(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = conn.Close()
}

// CloseSession ends one session. Its viewer is told why, so the tab it was in
// says so instead of reconnecting and taking the session back.
func (r *Relay) CloseSession(sessionID string, code int, reason string) {
	r.mu.Lock()
	p, ok := r.pairs[sessionID]
	if ok {
		delete(r.pairs, sessionID)
	} else {
		// Neither peer is here yet. Remember the session for as long as a
		// ticket could still bring one, then forget it.
		now := time.Now()
		for id, at := range r.closed {
			if now.Sub(at) > 2*pairTimeout {
				delete(r.closed, id)
			}
		}
		r.closed[sessionID] = now
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	p.mu.Lock()
	viewer := p.viewer
	p.mu.Unlock()
	if viewer != nil {
		_ = viewer.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	}
	closeConnections(p.close())
}

// copyFrames forwards every message from src to dst until either side errors.
// When allow is set, a frame it refuses is dropped and the session goes on.
// onChat, when set, hears every chat line from src, refused or not: a guest's
// line never reaches its own session's endpoint, and is carried elsewhere.
func copyFrames(src, dst *websocket.Conn, dstW *sync.Mutex, allow func(int, []byte) bool, onChat func(string)) error {
	for {
		mt, data, err := src.ReadMessage()
		if err != nil {
			return err
		}
		if allow == nil || allow(mt, data) {
			dstW.Lock()
			err := dst.WriteMessage(mt, data)
			dstW.Unlock()
			if err != nil {
				return err
			}
		}
		if onChat != nil && mt == websocket.TextMessage {
			if text, ok := chatText(data); ok {
				onChat(text)
			}
		}
	}
}

// chatText reads a chat line the way the sidecars do: "t" and "text" into a
// struct, so duplicate or differently cased keys mean what they mean there.
func chatText(data []byte) (string, bool) {
	if len(data) > chatFrameLimit {
		return "", false
	}
	var e struct {
		T    string `json:"t"`
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &e) != nil || e.T != "chat" || e.Text == "" {
		return "", false
	}
	return e.Text, true
}
