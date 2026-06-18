// Package remote abstracts remote-desktop access behind a pluggable provider so
// the rest of the system never depends on a single tool. Two providers ship:
//
//   - native      : a built-in session broker + relay with our own media sidecar
//     (the engine that streams the screen and injects input rides on this relay).
//   - meshcentral : deep-link / token login into a MeshCentral server.
//
// Adding or removing a provider never touches callers.
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

// sessionTTL bounds how long a brokered session token stays valid.
const sessionTTL = 2 * time.Minute

// Provider opens a remote-access session to an agent.
type Provider interface {
	Name() string
	StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error)
}

// Manager holds the configured providers and shared dependencies.
type Manager struct {
	mu           sync.RWMutex
	store        *store.Store
	hub          *hub.Hub
	publicURL    string
	meshURL      string
	meshTokenKey string
	meshUser     string
	providers    map[string]Provider
}

// UpdateMeshConfig swaps MeshCentral settings without rebuilding the server.
func (m *Manager) UpdateMeshConfig(meshURL, meshTokenKey, meshUser string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.meshURL = meshURL
	m.meshTokenKey = meshTokenKey
	m.meshUser = meshUser
}

// NewManager wires up all providers.
func NewManager(s *store.Store, h *hub.Hub, publicURL, meshURL, meshTokenKey, meshUser string) *Manager {
	m := &Manager{store: s, hub: h, publicURL: publicURL, meshURL: meshURL,
		meshTokenKey: meshTokenKey, meshUser: meshUser, providers: map[string]Provider{}}
	register := func(p Provider) { m.providers[p.Name()] = p }
	register(&nativeProvider{m: m})
	register(&filesProvider{m: m})
	register(&terminalProvider{m: m})
	register(&meshProvider{m: m})
	return m
}

// Start opens a session using the named provider (or the agent's best default).
func (m *Manager) Start(ctx context.Context, providerName string, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	p, ok := m.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("unknown remote provider %q", providerName)
	}
	return p.StartSession(ctx, agent, createdBy)
}

// wsBase converts the configured public HTTP URL to a ws:// or wss:// base.
func (m *Manager) wsBase() string {
	u := m.publicURL
	if strings.HasPrefix(u, "https://") {
		return "wss://" + strings.TrimPrefix(u, "https://")
	}
	return "ws://" + strings.TrimPrefix(u, "http://")
}

// persist saves a session and emits an audit record.
func (m *Manager) persist(ctx context.Context, rs *models.RemoteSession) error {
	if err := m.store.CreateRemoteSession(ctx, rs); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]string{"provider": rs.Provider, "agent": rs.AgentID})
	_ = m.store.AddAudit(ctx, rs.CreatedBy, "remote.start", rs.AgentID, detail)
	return nil
}

// ---------- native (built-in relay) ----------

// brokerRelay sets up a relayed session: both the agent's remote module and the
// technician's viewer connect to /ws/remote with the same token, and the server
// pipes bytes between them (NAT-friendly relay, hbbr-equivalent). kind tells
// the agent which module to attach (desktop sidecar vs. elevated terminal); it is
// also recorded as the session provider so the audit/list reflect what was opened.
func (m *Manager) brokerRelay(ctx context.Context, agent *models.Agent, createdBy, provider, kind string) (*models.RemoteSession, error) {
	if !m.hub.IsOnline(agent.ID) {
		return nil, fmt.Errorf("agent %s is offline", agent.ID)
	}
	token := auth.NewToken()
	base := m.wsBase()
	viewerURL := fmt.Sprintf("%s/ws/remote?token=%s&role=viewer", base, token)
	agentURL := fmt.Sprintf("%s/ws/remote?token=%s&role=agent", base, token)

	rs := &models.RemoteSession{
		AgentID:    agent.ID,
		Provider:   provider,
		Status:     "pending",
		Token:      token,
		ConnectURL: viewerURL,
		CreatedBy:  createdBy,
		ExpiresAt:  time.Now().Add(sessionTTL),
	}
	if err := m.persist(ctx, rs); err != nil {
		return nil, err
	}

	// Tell the agent to connect the requested remote module to the relay.
	start := protocol.RemoteStartMsg{SessionID: rs.ID, Token: token, RelayURL: agentURL, Kind: kind}
	data, _ := json.Marshal(start)
	frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgRemoteStart, Data: data})
	m.hub.Send(agent.ID, frame)
	return rs, nil
}

type nativeProvider struct{ m *Manager }

func (p *nativeProvider) Name() string { return "native" }

// StartSession brokers a relayed remote-desktop session (media sidecar).
func (p *nativeProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	return p.m.brokerRelay(ctx, agent, createdBy, "native", protocol.RemoteKindDesktop)
}

// ---------- files (file transfer only — no screen capture) ----------

type filesProvider struct{ m *Manager }

func (p *filesProvider) Name() string { return "files" }

// StartSession brokers a relayed session whose agent module is the media sidecar
// in file-transfer-only mode: it serves the file browser without capturing or
// streaming the screen. Opening "Remote ▸ File transfer" must NOT start a remote
// desktop session — only this. (A desktop session can still open the file
// browser in-band via the Files button, which stays the native provider.)
func (p *filesProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	return p.m.brokerRelay(ctx, agent, createdBy, "files", protocol.RemoteKindFiles)
}

// ---------- terminal (elevated shell over the relay) ----------

type terminalProvider struct{ m *Manager }

func (p *terminalProvider) Name() string { return "terminal" }

// StartSession brokers a relayed session whose agent module is an interactive
// shell on a PTY. The shell runs inside the agent process, so it inherits the
// agent's privileges — and the agent runs as a system service (root on
// Linux/macOS, SYSTEM on Windows), so the terminal session is elevated.
func (p *terminalProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	return p.m.brokerRelay(ctx, agent, createdBy, "terminal", protocol.RemoteKindTerminal)
}

// ---------- meshcentral ----------

type meshProvider struct{ m *Manager }

func (p *meshProvider) Name() string { return "meshcentral" }

// StartSession builds a connect URL into MeshCentral for the agent's node. When
// a login token key is configured it mints a login token so the session embeds
// without a second login (like Tactical RMM); otherwise it returns a deep link
// that works once the technician is logged into MeshCentral.
func (p *meshProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	p.m.mu.RLock()
	meshURL, meshTokenKey, meshUser := p.m.meshURL, p.m.meshTokenKey, p.m.meshUser
	p.m.mu.RUnlock()
	if meshURL == "" {
		return nil, fmt.Errorf("MeshCentral URL not configured (RMM_MESHCENTRAL_URL)")
	}
	if agent.MeshID == nil || *agent.MeshID == "" {
		return nil, fmt.Errorf("agent has no MeshCentral node id (set it via PATCH /api/devices/{id}/remote-ids)")
	}
	base := strings.TrimRight(meshURL, "/")
	// viewmode=11 = desktop; hide=31 hides the MeshCentral chrome for embedding.
	connect := fmt.Sprintf("%s/?viewmode=11&hide=31&node=%s", base, *agent.MeshID)
	if meshTokenKey != "" {
		tok, err := meshLoginToken(meshTokenKey, meshUser)
		if err != nil {
			return nil, fmt.Errorf("mesh login token: %w", err)
		}
		connect = fmt.Sprintf("%s/?login=%s&viewmode=11&hide=31&node=%s", base, url.QueryEscape(tok), *agent.MeshID)
	}
	rs := &models.RemoteSession{
		AgentID:    agent.ID,
		Provider:   "meshcentral",
		Status:     "ready",
		Token:      auth.NewToken(),
		ConnectURL: connect,
		CreatedBy:  createdBy,
		ExpiresAt:  time.Now().Add(sessionTTL),
	}
	if err := p.m.persist(ctx, rs); err != nil {
		return nil, err
	}
	return rs, nil
}
