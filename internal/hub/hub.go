// Package hub tracks live agent WebSocket connections so the server can push
// jobs and remote-session requests to online agents.
package hub

import "sync"

// Hub is a concurrency-safe registry of per-agent outbound channels.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]chan []byte
}

// New creates an empty hub.
func New() *Hub {
	return &Hub{clients: make(map[string]chan []byte)}
}

// Add registers an agent connection and returns its outbound channel. If the
// agent already had a connection, the old channel is closed (last writer wins).
func (h *Hub) Add(agentID string) chan []byte {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	if old, ok := h.clients[agentID]; ok {
		close(old)
	}
	h.clients[agentID] = ch
	h.mu.Unlock()
	return ch
}

// Remove deregisters an agent connection, but only if ch is still the current
// one (avoids a reconnect race removing the new connection).
func (h *Hub) Remove(agentID string, ch chan []byte) {
	h.mu.Lock()
	if cur, ok := h.clients[agentID]; ok && cur == ch {
		delete(h.clients, agentID)
		close(ch)
	}
	h.mu.Unlock()
}

// Send delivers a raw frame to an agent. Returns false if the agent is offline
// or its buffer is full.
func (h *Hub) Send(agentID string, msg []byte) bool {
	h.mu.RLock()
	ch, ok := h.clients[agentID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case ch <- msg:
		return true
	default:
		return false
	}
}

// IsOnline reports whether an agent currently has a live connection.
func (h *Hub) IsOnline(agentID string) bool {
	h.mu.RLock()
	_, ok := h.clients[agentID]
	h.mu.RUnlock()
	return ok
}
