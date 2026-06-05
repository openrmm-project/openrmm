package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Agents and the relay are not browser-origin constrained; tighten if you
	// serve the relay directly to browser viewers from another origin.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// handleAgentWS upgrades, authenticates the agent via the register handshake,
// then runs the read loop while a write pump pushes queued frames.
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ctx := context.Background()

	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return
	}
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Type != protocol.MsgRegister {
		writeFrame(conn, protocol.MsgError, map[string]string{"error": "expected register"})
		_ = conn.Close()
		return
	}
	var reg protocol.RegisterReq
	_ = json.Unmarshal(env.Data, &reg)

	agentID, token, err := s.registerAgent(ctx, &reg)
	if err != nil {
		writeFrame(conn, protocol.MsgError, map[string]string{"error": err.Error()})
		_ = conn.Close()
		return
	}
	writeFrame(conn, protocol.MsgRegistered, protocol.RegisterResp{AgentID: agentID, AgentToken: token})

	ch := s.hub.Add(agentID)
	_ = s.store.SetAgentStatus(ctx, agentID, models.AgentOnline)
	go s.writePump(conn, ch)
	go s.jobs.DispatchPending(ctx, agentID)

	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	// Read loop.
	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		s.handleAgentFrame(ctx, agentID, raw)
	}

	s.hub.Remove(agentID, ch)
	_ = s.store.SetAgentStatus(ctx, agentID, models.AgentOffline)
	_ = conn.Close()
}

// registerAgent validates the handshake. On enrollment it returns a freshly
// minted agent token; on re-auth the returned token is empty (the agent keeps
// the one it already stored).
func (s *Server) registerAgent(ctx context.Context, reg *protocol.RegisterReq) (id, token string, err error) {
	if reg.EnrollToken != "" {
		siteID, err := s.store.ConsumeEnrollToken(ctx, reg.EnrollToken)
		if err != nil {
			return "", "", errors.New("invalid or exhausted enroll token")
		}
		token = auth.NewToken()
		a, err := s.store.CreateAgent(ctx, reg.Hostname, reg.OS, reg.Arch, reg.Version, auth.Hash(token), siteID)
		if err != nil {
			return "", "", err
		}
		return a.ID, token, nil
	}
	if reg.AgentID == "" || reg.AgentToken == "" {
		return "", "", errors.New("missing credentials")
	}
	hash, err := s.store.GetAgentTokenHash(ctx, reg.AgentID)
	if err != nil {
		return "", "", errors.New("unknown agent")
	}
	if !auth.ConstantTimeEqual(hash, auth.Hash(reg.AgentToken)) {
		return "", "", errors.New("invalid agent token")
	}
	_ = s.store.RefreshAgentInfo(ctx, reg.AgentID, reg.Hostname, reg.OS, reg.Arch, reg.Version)
	return reg.AgentID, "", nil
}

// handleAgentFrame processes one inbound agent message.
func (s *Server) handleAgentFrame(ctx context.Context, agentID string, raw []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	switch env.Type {
	case protocol.MsgHeartbeat:
		_ = s.store.TouchAgent(ctx, agentID)
	case protocol.MsgInventory:
		var inv protocol.InventoryMsg
		if json.Unmarshal(env.Data, &inv) == nil && len(inv.Data) > 0 {
			_ = s.store.UpsertInventory(ctx, agentID, inv.Data)
		}
	case protocol.MsgJobResult:
		var res protocol.JobResultMsg
		if json.Unmarshal(env.Data, &res) == nil {
			if err := s.jobs.OnResult(ctx, res); err != nil {
				log.Printf("job result error: %v", err)
			}
		}
	case protocol.MsgCheckResult:
		var res protocol.CheckResultMsg
		if json.Unmarshal(env.Data, &res) == nil {
			if err := s.sched.OnCheckResult(ctx, agentID, res); err != nil {
				log.Printf("check result error: %v", err)
			}
		}
	case protocol.MsgRemoteStatus:
		// Reserved for native remote-session state updates from the agent.
	}
}

// writePump drains the hub channel to the socket and keeps the link alive.
func (s *Server) writePump(conn *websocket.Conn, ch chan []byte) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func writeFrame(conn *websocket.Conn, typ string, payload any) {
	data, _ := json.Marshal(payload)
	frame, _ := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	_ = conn.WriteMessage(websocket.TextMessage, frame)
}

// handleRelayWS pairs an agent peer and a viewer peer that share a session
// token, then pipes traffic between them (native remote provider).
func (s *Server) handleRelayWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	role := r.URL.Query().Get("role")
	if role != "agent" && role != "viewer" {
		http.Error(w, "role must be agent or viewer", http.StatusBadRequest)
		return
	}
	sess, err := s.store.GetRemoteSessionByToken(r.Context(), token)
	if err != nil || time.Now().After(sess.ExpiresAt) {
		http.Error(w, "invalid or expired session", http.StatusForbidden)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	_ = s.store.SetRemoteSessionStatus(r.Context(), sess.ID, "active")
	_ = s.relay.Join(token, role, conn)
	_ = s.store.SetRemoteSessionStatus(context.Background(), sess.ID, "ended")
}
