// Package hub tracks live agent WebSocket connections so the server can push
// jobs and remote-session requests to online agents.
package hub

import "sync"

// Hub is a concurrency-safe registry of per-agent outbound channels.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]client
}

type client struct {
	ch      chan []byte
	closeFn func()
}

// New creates an empty hub.
func New() *Hub {
	return &Hub{clients: make(map[string]client)}
}

// Add registers an agent connection and returns its outbound channel. If the
// agent already had a connection, the old connection is closed after its channel
// has been removed from the registry (last writer wins).
func (h *Hub) Add(agentID string, closeFn func()) chan []byte {
	ch := make(chan []byte, 32)
	var old client
	var replaced bool
	h.mu.Lock()
	if existing, ok := h.clients[agentID]; ok {
		old = existing
		close(old.ch)
		replaced = true
	}
	h.clients[agentID] = client{ch: ch, closeFn: closeFn}
	h.mu.Unlock()
	if replaced && old.closeFn != nil {
		old.closeFn()
	}
	return ch
}

// Remove deregisters an agent connection, but only if ch is still the current
// one (avoids a reconnect race removing the new connection).
func (h *Hub) Remove(agentID string, ch chan []byte) bool {
	h.mu.Lock()
	removed := false
	if cur, ok := h.clients[agentID]; ok && cur.ch == ch {
		delete(h.clients, agentID)
		close(cur.ch)
		removed = true
	}
	h.mu.Unlock()
	return removed
}

// OnlineCount returns the number of agents with a live connection. Used by the
// metrics endpoint.
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Send delivers a raw frame to an agent. Returns false if the agent is offline
// or its buffer is full.
func (h *Hub) Send(agentID string, msg []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.clients[agentID]
	if !ok {
		return false
	}
	select {
	case c.ch <- msg:
		return true
	default:
		return false
	}
}

// Disconnect closes a live device transport. It is used by credential revocation
// so a compromised endpoint cannot keep using an already-authenticated socket.
func (h *Hub) Disconnect(agentID string) bool {
	h.mu.Lock()
	c, ok := h.clients[agentID]
	if ok {
		delete(h.clients, agentID)
		close(c.ch)
	}
	h.mu.Unlock()
	if ok && c.closeFn != nil {
		c.closeFn()
	}
	return ok
}

// DisconnectCurrent closes an agent transport only when ch still owns the live
// lease. It prevents a delayed frame from a displaced socket from disconnecting
// a newer reconnect.
func (h *Hub) DisconnectCurrent(agentID string, ch chan []byte) bool {
	h.mu.Lock()
	c, ok := h.clients[agentID]
	if ok && c.ch == ch {
		delete(h.clients, agentID)
		close(c.ch)
	} else {
		ok = false
	}
	h.mu.Unlock()
	if ok && c.closeFn != nil {
		c.closeFn()
	}
	return ok
}

// IsCurrent reports whether ch still owns agentID's live connection lease.
// Callers use it to prevent a displaced connection from changing device state.
func (h *Hub) IsCurrent(agentID string, ch chan []byte) bool {
	h.mu.RLock()
	cur, ok := h.clients[agentID]
	h.mu.RUnlock()
	return ok && cur.ch == ch
}

// IsOnline reports whether an agent currently has a live connection.
func (h *Hub) IsOnline(agentID string) bool {
	h.mu.RLock()
	_, ok := h.clients[agentID]
	h.mu.RUnlock()
	return ok
}
