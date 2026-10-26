package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/devicelimit"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/store"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Agents and the relay are not browser-origin constrained; tighten if you
	// serve the relay directly to browser viewers from another origin.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// relaySubprotocol is the constant the browser offers first; the ticket and auth
// token ride alongside it as extra subprotocol entries (relayTicket and
// relayViewerIdentity read those). RFC 6455 makes the client fail the connection
// when it offered subprotocols and the 101 response echoes none, so the relay
// upgrader has to select this one explicitly — a nil Subprotocols list would
// leave the header off and every viewer would close with 1006 right after
// upgrading.
const relaySubprotocol = "rmm-v1"

var relayUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(_ *http.Request) bool { return true },
	Subprotocols:    []string{relaySubprotocol},
}

// Per-message read limits cap memory a single peer can force the server to
// allocate (a malicious/compromised agent or relay peer could otherwise stream
// an unbounded frame). Agent frames are JSON envelopes (job results cap stdout/
// stderr at 1 MiB each); relay frames carry screen tiles / 64 KiB file chunks,
// so it gets generous headroom.
const (
	agentWSReadLimit = 8 << 20  // 8 MiB
	relayWSReadLimit = 32 << 20 // 32 MiB
)

// handleAgentWS upgrades, authenticates the agent via the register handshake,
// then runs the read loop while a write pump pushes queued frames.
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(agentWSReadLimit)
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

	var agentID, token, cancelCredentialRotationID string
	var ch chan []byte
	if reg.EnrollToken != "" {
		agentID, token, cancelCredentialRotationID, err = s.registerAgent(ctx, &reg)
		if err == nil {
			unlock := s.lockAgentLifecycle(agentID)
			agent, getErr := s.store.GetAgent(ctx, agentID)
			if getErr != nil || agent.Status == models.AgentRevoked {
				err = errors.New("agent was revoked during enrollment")
			} else {
				ch, err = s.attachAgentConnection(ctx, agentID, conn)
			}
			unlock()
		}
	} else {
		unlock := s.lockAgentLifecycle(reg.AgentID)
		agentID, token, cancelCredentialRotationID, err = s.registerAgent(ctx, &reg)
		if err == nil {
			ch, err = s.attachAgentConnection(ctx, agentID, conn)
		}
		unlock()
	}
	if err != nil {
		writeFrame(conn, protocol.MsgError, map[string]string{"error": err.Error()})
		_ = conn.Close()
		return
	}
	if err := writeFrame(conn, protocol.MsgRegistered, protocol.RegisterResp{
		AgentID:                    agentID,
		AgentToken:                 token,
		CancelCredentialRotationID: cancelCredentialRotationID,
	}); err != nil {
		s.detachAgentConnection(ctx, agentID, ch)
		_ = conn.Close()
		return
	}
	defer s.detachAgentConnection(ctx, agentID, ch)
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
		s.handleAgentFrame(ctx, agentID, ch, raw)
	}
}

func (s *Server) attachAgentConnection(ctx context.Context, agentID string, conn *websocket.Conn) (chan []byte, error) {
	ch := s.hub.Add(agentID, func() { _ = conn.Close() })
	if err := s.store.SetAgentStatus(ctx, agentID, models.AgentOnline); err != nil {
		s.hub.Remove(agentID, ch)
		return nil, err
	}
	return ch, nil
}

// registerAgent validates the handshake. On enrollment it returns a freshly
// minted agent token; on re-auth the returned token is empty (the agent keeps
// the one it already stored).
func (s *Server) registerAgent(ctx context.Context, reg *protocol.RegisterReq) (id, token, cancelCredentialRotationID string, err error) {
	if reg.EnrollToken != "" {
		// The plan's device limit, when one is enforced, is checked in the same
		// transaction that consumes the token and creates the agent.
		limit := s.enforcedDeviceLimit(time.Now())
		token = auth.NewToken()
		a, err := s.store.EnrollAgent(ctx, reg.EnrollToken, limit, reg.Hostname, reg.OS, reg.Arch, reg.Version, auth.Hash(token))
		if errors.Is(err, devicelimit.ErrLimitReached) {
			go s.observeDeviceLimitNow()
			return "", "", "", errors.New(devicelimit.RefusalMessage(limit))
		}
		if errors.Is(err, store.ErrNotFound) {
			return "", "", "", errors.New("invalid or exhausted enroll token")
		}
		if err != nil {
			return "", "", "", err
		}
		if limit > 0 {
			go s.observeDeviceLimitNow()
		}
		return a.ID, token, "", nil
	}
	if reg.AgentID == "" || reg.AgentToken == "" {
		return "", "", "", errors.New("missing credentials")
	}
	valid, err := s.store.AuthenticateAgentToken(ctx, reg.AgentID, auth.Hash(reg.AgentToken))
	if err != nil || !valid {
		return "", "", "", errors.New("invalid agent token")
	}
	_ = s.store.RefreshAgentInfo(ctx, reg.AgentID, reg.Hostname, reg.OS, reg.Arch, reg.Version)
	cancelCredentialRotationID, err = s.store.PendingAgentCredentialRotationCancellation(ctx, reg.AgentID)
	if errors.Is(err, store.ErrNotFound) {
		cancelCredentialRotationID = ""
		err = nil
	}
	if err != nil {
		return "", "", "", err
	}
	return reg.AgentID, "", cancelCredentialRotationID, nil
}

// detachAgentConnection only marks an endpoint offline when the closing socket
// still owns its hub lease. A replaced connection must remain online.
func (s *Server) detachAgentConnection(ctx context.Context, agentID string, ch chan []byte) {
	unlock := s.lockAgentLifecycle(agentID)
	defer unlock()
	if s.hub.Remove(agentID, ch) {
		_ = s.store.SetAgentStatus(ctx, agentID, models.AgentOffline)
	}
}

// handleAgentFrame processes one inbound agent message.
func (s *Server) handleAgentFrame(ctx context.Context, agentID string, ch chan []byte, raw []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	// Linearize frame processing with registration, replacement, and revocation.
	// Once a transport loses its lease, no already-read frame may mutate state.
	unlock := s.lockAgentLifecycle(agentID)
	defer unlock()
	if !s.hub.IsCurrent(agentID, ch) {
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
			if err := s.jobs.OnResult(ctx, agentID, res); err != nil {
				log.Printf("job result error: %v", err)
			} else {
				// Durably recorded — ack so the agent drops it from its retry
				// outbox. If this ack is lost, the agent re-sends and OnResult is
				// a no-op (idempotent), so we simply ack again.
				data, _ := json.Marshal(protocol.JobResultAckMsg{JobID: res.JobID})
				frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgJobResultAck, Data: data})
				s.hub.Send(agentID, frame)
			}
		}
	case protocol.MsgCheckResult:
		var res protocol.CheckResultMsg
		if json.Unmarshal(env.Data, &res) == nil {
			ack, err := s.sched.OnCheckResult(ctx, agentID, res)
			if err != nil {
				log.Printf("check result error: %v", err)
			}
			if ack && res.ExecutionID != "" {
				data, _ := json.Marshal(protocol.CheckResultAckMsg{ExecutionID: res.ExecutionID})
				frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgCheckResultAck, Data: data})
				s.hub.Send(agentID, frame)
			}
		}
	case protocol.MsgPatchReport:
		var rep protocol.PatchReportMsg
		if json.Unmarshal(env.Data, &rep) == nil {
			if err := s.patches.OnReport(ctx, agentID, &rep); err != nil {
				log.Printf("patch report error: %v", err)
			}
		}
	case protocol.MsgPatchResult:
		var res protocol.PatchResultMsg
		if json.Unmarshal(env.Data, &res) == nil {
			if err := s.patches.OnResult(ctx, agentID, &res); err != nil {
				log.Printf("patch result error: %v", err)
			}
		}
	case protocol.MsgRemoteQueryResult:
		var res protocol.RemoteQueryResultMsg
		if json.Unmarshal(env.Data, &res) == nil {
			s.remote.ResolveQuery(res)
		}
	case protocol.MsgRemoteStatus:
		// Reserved for native remote-session state updates from the agent.
	case protocol.MsgCredentialRotated:
		// The replacement reconnect is the commit point. This acknowledgement only
		// confirms local durability and must not race a requested cancellation.
	case protocol.MsgCredentialRotationCanceled:
		var msg protocol.CredentialRotationCancelMsg
		if json.Unmarshal(env.Data, &msg) == nil && msg.RotationID != "" {
			changed, err := s.store.ConfirmAgentCredentialRotationCancellation(ctx, agentID, msg.RotationID)
			if err != nil {
				log.Printf("agent credential rotation cancellation error: %v", err)
				return
			}
			if changed {
				_ = s.store.AddAudit(ctx, "agent", "device.credential_rotation_canceled", agentID, nil)
			}
			data, _ := json.Marshal(protocol.CredentialRotationCancelMsg{RotationID: msg.RotationID})
			frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgCredentialRotationCanceledAck, Data: data})
			s.hub.Send(agentID, frame)
		}
	}
}

// writePump drains the hub channel to the socket and keeps the link alive.
func (s *Server) writePump(conn *websocket.Conn, ch chan []byte) {
	defer conn.Close()
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

func writeFrame(conn *websocket.Conn, typ string, payload any) error {
	data, _ := json.Marshal(payload)
	frame, _ := json.Marshal(protocol.Envelope{Type: typ, Data: data})
	return conn.WriteMessage(websocket.TextMessage, frame)
}

// handleRelayWS consumes a role-specific one-time ticket and pairs the peers by
// non-secret session id. Browser tickets travel in Sec-WebSocket-Protocol; the
// sidecar sends its ticket in Authorization, so credentials never enter URLs.
func (s *Server) handleRelayWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	role := r.URL.Query().Get("role")
	if role != "agent" && role != "viewer" {
		http.Error(w, "session and valid role are required", http.StatusBadRequest)
		return
	}
	legacyToken := ""
	if role == "agent" {
		legacyToken = r.URL.Query().Get("token")
		if legacyToken != "" && !s.cfg.AllowLegacyAgentRelay {
			http.Error(w, "legacy agent relay is disabled; update the remote sidecar", http.StatusUpgradeRequired)
			return
		}
	} else if r.URL.Query().Get("token") != "" {
		http.Error(w, "viewer relay tickets must use the WebSocket handshake", http.StatusBadRequest)
		return
	}
	if sessionID == "" && !(role == "agent" && legacyToken != "") {
		http.Error(w, "session and valid role are required", http.StatusBadRequest)
		return
	}
	if role == "viewer" && !s.sameOrigin(r) {
		http.Error(w, "invalid viewer origin", http.StatusForbidden)
		return
	}
	if role == "viewer" && s.cfg.EnforceConsoleRelayIngress && r.Header.Get("X-RMM-Relay-Ingress") != "console" {
		http.Error(w, "viewer relay is only available through the console ingress", http.StatusForbidden)
		return
	}
	ticket := relayTicket(r, role)
	if role == "agent" && sessionID == "" {
		ticket = legacyToken
	}
	if ticket == "" {
		http.Error(w, "missing relay ticket", http.StatusUnauthorized)
		return
	}
	viewerSubject := ""
	// guestShare is set when the viewer is a guest a technician let in: it
	// authenticates with its guest credential instead of an operator token,
	// and every frame of its session is filtered (remote.GuestPolicy).
	var guestShare *models.RemoteShare
	if role == "viewer" && guestRelayCredential(r) != "" {
		share, guestErr := s.guestRelayShare(r.Context(), sessionID, guestRelayCredential(r))
		if guestErr != nil {
			http.Error(w, guestErr.Error(), http.StatusForbidden)
			return
		}
		guestShare = share
		viewerSubject = models.GuestPrincipal(share.ID)
	} else if role == "viewer" {
		identity, identityErr := s.relayViewerIdentity(r)
		if identityErr != nil || identity.relayPrincipal == "" {
			http.Error(w, "viewer authentication required", http.StatusUnauthorized)
			return
		}
		metadata, metadataErr := s.store.GetRemoteSessionForAuthorization(r.Context(), sessionID)
		if metadataErr != nil || metadata.CreatedBy != identity.relayPrincipal {
			http.Error(w, "invalid or expired session", http.StatusForbidden)
			return
		}
		permission := models.PermDevicesRemote
		switch metadata.Provider {
		case "files":
			permission = models.PermDevicesFiles
		case "terminal":
			permission = models.PermDevicesTerminal
		}
		permissions, permissionErr := s.permissionsForRole(r.Context(), identity.role)
		if permissionErr != nil || !permissionSet(permissions)[permission] {
			http.Error(w, "remote permission was revoked", http.StatusForbidden)
			return
		}
		if identity.scopeUser != "" {
			allowed, scopeErr := s.store.AgentAllowedForUser(r.Context(), identity.scopeUser, metadata.AgentID)
			if scopeErr != nil || !allowed {
				http.Error(w, "device is outside your current access scope", http.StatusForbidden)
				return
			}
		}
		viewerSubject = identity.relayPrincipal
	}
	var sess *models.RemoteSession
	var err error
	if role == "agent" && sessionID == "" {
		sess, err = s.store.ConsumeLegacyAgentTicket(r.Context(), ticket)
	} else {
		sess, err = s.store.ConsumeRemoteSessionTicket(r.Context(), sessionID, role, ticket, viewerSubject)
	}
	if err != nil {
		http.Error(w, "invalid, expired, or already-used session ticket", http.StatusForbidden)
		return
	}
	conn, err := relayUpgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = s.store.SetRemoteSessionStatus(context.Background(), sess.ID, "ended")
		return
	}
	defer conn.Close()
	conn.SetReadLimit(relayWSReadLimit)
	// Only the viewer side is presence: the agent end is the endpoint itself,
	// and announcing that a machine is attached to itself says nothing. Removed
	// by defer, so a dropped socket cannot leave a ghost claiming somebody is
	// still connected.
	if role == "viewer" {
		viewer := remote.Viewer{
			SessionID: sess.ID,
			AgentID:   sess.AgentID,
			User:      viewerSubject,
			Kind:      sess.Provider,
			Since:     time.Now().UTC(),
		}
		leave := func() {}
		if guestShare != nil && !s.guests.owns(guestShare.ID, sess.ID) {
			// Removed between brokering and connecting: never admit it.
			remote.CloseViewer(conn, remote.CloseGuestEnded, "guest_removed")
			_ = s.store.SetRemoteSessionStatus(context.Background(), sess.ID, "ended")
			return
		}
		if sess.Provider == "native" {
			// One desktop session per operator per endpoint. The start handler
			// already asked; this is the check two tabs cannot both pass.
			var existing *remote.Viewer
			if leave, existing = s.presence.JoinExclusive(viewer); existing != nil {
				remote.CloseViewer(conn, remote.CloseSessionActive, "remote_session_active")
				// The endpoint may already be waiting on the other side.
				s.relay.CloseSession(sess.ID, remote.CloseSessionActive, "remote_session_active")
				_ = s.store.SetRemoteSessionStatus(context.Background(), sess.ID, "ended")
				return
			}
		} else {
			leave = s.presence.Join(viewer)
		}
		defer leave()
		if guestShare != nil {
			policy := remote.GuestPolicy{Control: guestShare.Mode == models.RemoteShareControl}
			if err := s.relay.JoinGuest(sess.ID, sess.AgentID, conn, policy); err != nil {
				log.Printf("remote relay session=%s guest=%s rejected: %v", sess.ID, guestShare.ID, err)
			}
			s.guests.finish(guestShare.ID, sess.ID)
			_ = s.store.SetRemoteSessionStatus(context.Background(), sess.ID, "ended")
			s.auditShare(context.Background(), "guest", "remote.share.guest_end", guestShare, map[string]string{"session": sess.ID})
			return
		}
		if err := s.relay.Join(sess.ID, sess.AgentID, role, conn); err != nil {
			log.Printf("remote relay session=%s role=%s rejected: %v", sess.ID, role, err)
		}
		_ = s.store.SetRemoteSessionStatus(context.Background(), sess.ID, "ended")
		if sess.Provider == "native" {
			// The technician's desktop session ended: once they have none left
			// on this device, the guests they let in leave with them.
			leave()
			s.sponsorLeft(sess.AgentID, viewerSubject)
		}
		return
	}
	if err := s.relay.Join(sess.ID, sess.AgentID, role, conn); err != nil {
		log.Printf("remote relay session=%s role=%s rejected: %v", sess.ID, role, err)
	}
	_ = s.store.SetRemoteSessionStatus(context.Background(), sess.ID, "ended")
}

func relayTicket(r *http.Request, role string) string {
	if role == "agent" {
		const prefix = "Remote-Agent "
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, prefix) {
			return strings.TrimPrefix(h, prefix)
		}
		return ""
	}
	for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "rmm-ticket.") {
			return strings.TrimPrefix(p, "rmm-ticket.")
		}
	}
	return ""
}

// relayViewerIdentity authenticates the browser token and reloads its current
// role and effective scope immediately before a viewer ticket is consumed.
func (s *Server) relayViewerIdentity(r *http.Request) (authenticatedIdentity, error) {
	var token string
	for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "rmm-auth.") {
			token = strings.TrimPrefix(p, "rmm-auth.")
			break
		}
	}
	if token == "" {
		return authenticatedIdentity{}, store.ErrNotFound
	}
	return s.authenticateToken(r.Context(), "/ws/remote", token)
}

func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := r.Host
	// Nginx's $host intentionally strips the port. Only use the original host
	// supplied by a configured trusted proxy, never a direct client header.
	if s.cfg.TrustProxyHeaders && r.Header.Get("X-Forwarded-Host") != "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	return u.Host == host
}
