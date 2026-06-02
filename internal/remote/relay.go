package remote

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// pairTimeout bounds how long the first peer waits for its partner.
const pairTimeout = 60 * time.Second

// Relay pairs two WebSocket peers (agent + viewer) that present the same session
// token and pipes frames between them. This is the transport plane of the native
// remote provider — the media engine (screen capture / encoding / input) lives in
// the agent and viewer clients and rides on top of this relay.
type Relay struct {
	mu    sync.Mutex
	pairs map[string]*pair
}

type pair struct {
	mu        sync.Mutex
	agent     *websocket.Conn
	viewer    *websocket.Conn
	ready     chan struct{}
	readyOnce sync.Once
	closeOnce sync.Once
}

// NewRelay creates an empty relay.
func NewRelay() *Relay { return &Relay{pairs: map[string]*pair{}} }

func (r *Relay) get(token string) *pair {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pairs[token]
	if !ok {
		p = &pair{ready: make(chan struct{})}
		r.pairs[token] = p
	}
	return p
}

func (r *Relay) drop(token string, p *pair) {
	r.mu.Lock()
	if cur, ok := r.pairs[token]; ok && cur == p {
		delete(r.pairs, token)
	}
	r.mu.Unlock()
	p.closeOnce.Do(func() {
		if p.agent != nil {
			_ = p.agent.Close()
		}
		if p.viewer != nil {
			_ = p.viewer.Close()
		}
	})
}

// Join registers a peer for a session token and, once both peers are present,
// pipes this peer's inbound frames to the other peer. It blocks until the
// session ends. role is "agent" or "viewer".
func (r *Relay) Join(token, role string, conn *websocket.Conn) error {
	p := r.get(token)

	p.mu.Lock()
	if role == "viewer" {
		p.viewer = conn
	} else {
		p.agent = conn
	}
	both := p.agent != nil && p.viewer != nil
	p.mu.Unlock()
	if both {
		p.readyOnce.Do(func() { close(p.ready) })
	}

	select {
	case <-p.ready:
	case <-time.After(pairTimeout):
		r.drop(token, p)
		return errors.New("relay pairing timed out")
	}

	// Pipe inbound frames from this conn to the partner conn.
	var dst *websocket.Conn
	if role == "viewer" {
		dst = p.agent
	} else {
		dst = p.viewer
	}
	err := copyFrames(conn, dst)
	r.drop(token, p)
	return err
}

// copyFrames forwards every message from src to dst until either side errors.
func copyFrames(src, dst *websocket.Conn) error {
	for {
		mt, data, err := src.ReadMessage()
		if err != nil {
			return err
		}
		if err := dst.WriteMessage(mt, data); err != nil {
			return err
		}
	}
}
