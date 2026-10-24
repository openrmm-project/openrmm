package remote

import (
	"sort"
	"sync"
	"time"
)

// Presence answers "who is on this endpoint right now".
//
// Two technicians connecting to the same machine at once is allowed — one may
// be watching while the other works, and blocking it would break legitimate
// hand-offs. What is not acceptable is each of them believing they are alone:
// that is how two people fight over the same mouse, or how one closes a window
// the other was reading. So the connection is never refused, only announced.
//
// The one exception is the same operator twice on the same screen. That is
// never a hand-off: it is a forgotten tab, and its mouse and keyboard fight the
// tab the operator is actually using while the endpoint captures the screen
// twice. JoinExclusive refuses it; the console offers to move the session.
//
// The registry is in memory on purpose. It describes live attachments, and a
// row that outlives the process it describes would be a lie — a server restart
// drops every relay connection with it.
type Presence struct {
	mu      sync.Mutex
	viewers map[string]map[string]Viewer // agent id -> session id -> viewer
}

// Viewer is one attached operator. Kind mirrors the session's provider, so the
// console can say "file transfer" rather than implying somebody is watching the
// screen.
type Viewer struct {
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id"`
	User      string    `json:"user"`
	Kind      string    `json:"kind"`
	Since     time.Time `json:"since"`
}

func NewPresence() *Presence {
	return &Presence{viewers: map[string]map[string]Viewer{}}
}

// Join records an attachment and returns the function that removes it. The
// caller defers that function, so a panic or a dropped socket cannot leave a
// ghost behind claiming somebody is connected.
func (p *Presence) Join(v Viewer) func() {
	leave, _ := p.join(v, false)
	return leave
}

// JoinExclusive is Join for a session one operator may hold only once per
// endpoint: when the same user already has a session of the same kind on it,
// nothing is recorded and that session is returned instead. Checking and
// recording under one lock is the point — two tabs connecting at the same
// moment cannot both get in.
func (p *Presence) JoinExclusive(v Viewer) (func(), *Viewer) {
	return p.join(v, true)
}

func (p *Presence) join(v Viewer, exclusive bool) (func(), *Viewer) {
	if p == nil || v.AgentID == "" || v.SessionID == "" {
		return func() {}, nil
	}
	if v.Since.IsZero() {
		v.Since = time.Now().UTC()
	}
	p.mu.Lock()
	if exclusive {
		for _, other := range p.viewers[v.AgentID] {
			if other.SessionID != v.SessionID && other.User == v.User && other.Kind == v.Kind {
				p.mu.Unlock()
				return func() {}, &other
			}
		}
	}
	if p.viewers[v.AgentID] == nil {
		p.viewers[v.AgentID] = map[string]Viewer{}
	}
	p.viewers[v.AgentID][v.SessionID] = v
	p.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			sessions := p.viewers[v.AgentID]
			if sessions == nil {
				return
			}
			delete(sessions, v.SessionID)
			if len(sessions) == 0 {
				delete(p.viewers, v.AgentID)
			}
		})
	}, nil
}

// ForUser lists one operator's attachments of one kind to one endpoint.
func (p *Presence) ForUser(agentID, user, kind string) []Viewer {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Viewer{}
	for _, v := range sorted(p.viewers[agentID]) {
		if v.User == user && v.Kind == kind {
			out = append(out, v)
		}
	}
	return out
}

// Session returns the viewer attached to one session of an endpoint.
func (p *Presence) Session(agentID, sessionID string) (Viewer, bool) {
	if p == nil {
		return Viewer{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.viewers[agentID][sessionID]
	return v, ok
}

// Remove forgets a session now, before its socket's own cleanup runs. A
// replaced session is closed asynchronously; without this the session that
// replaced it would find it still attached and be refused.
func (p *Presence) Remove(agentID, sessionID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if sessions := p.viewers[agentID]; sessions != nil {
		delete(sessions, sessionID)
		if len(sessions) == 0 {
			delete(p.viewers, agentID)
		}
	}
}

// ForAgent lists who is attached to one endpoint, oldest first — the order a
// hand-off reads in: whoever was there first is at the top.
func (p *Presence) ForAgent(agentID string) []Viewer {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return sorted(p.viewers[agentID])
}

// All lists every attachment, for the fleet view.
func (p *Presence) All() []Viewer {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Viewer{}
	for _, sessions := range p.viewers {
		out = append(out, sorted(sessions)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentID != out[j].AgentID {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}

func sorted(sessions map[string]Viewer) []Viewer {
	out := make([]Viewer, 0, len(sessions))
	for _, v := range sessions {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}
