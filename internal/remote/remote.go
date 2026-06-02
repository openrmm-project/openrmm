// Package remote abstracts remote-desktop access behind a pluggable provider so
// the rest of the system never depends on a single tool. Three providers ship:
//
//   - native      : a built-in session broker + relay (the future "own" remote
//                   tool / RustDesk fork connects its media engine to this relay).
//   - meshcentral : deep-link / token login into a MeshCentral server.
//   - rustdesk    : launches the native RustDesk client via the rustdesk:// URI.
//
// Adding or removing a provider never touches callers — if the native tool does
// not pan out, drop it and MeshCentral keeps working.
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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
	store        *store.Store
	hub          *hub.Hub
	publicURL    string
	meshURL      string
	meshTokenKey string
	meshUser     string
	providers    map[string]Provider
}

// NewManager wires up all providers.
func NewManager(s *store.Store, h *hub.Hub, publicURL, meshURL, meshTokenKey, meshUser string) *Manager {
	m := &Manager{store: s, hub: h, publicURL: publicURL, meshURL: meshURL,
		meshTokenKey: meshTokenKey, meshUser: meshUser, providers: map[string]Provider{}}
	register := func(p Provider) { m.providers[p.Name()] = p }
	register(&nativeProvider{m: m})
	register(&meshProvider{m: m})
	register(&rustdeskProvider{m: m})
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

type nativeProvider struct{ m *Manager }

func (p *nativeProvider) Name() string { return "native" }

// StartSession brokers a relayed session: both the agent's remote module and the
// technician's viewer connect to /ws/remote with the same token, and the server
// pipes bytes between them (NAT-friendly, like RustDesk's hbbr relay).
func (p *nativeProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	if !p.m.hub.IsOnline(agent.ID) {
		return nil, fmt.Errorf("agent %s is offline", agent.ID)
	}
	token := auth.NewToken()
	base := p.m.wsBase()
	viewerURL := fmt.Sprintf("%s/ws/remote?token=%s&role=viewer", base, token)
	agentURL := fmt.Sprintf("%s/ws/remote?token=%s&role=agent", base, token)

	rs := &models.RemoteSession{
		AgentID:    agent.ID,
		Provider:   "native",
		Status:     "pending",
		Token:      token,
		ConnectURL: viewerURL,
		CreatedBy:  createdBy,
		ExpiresAt:  time.Now().Add(sessionTTL),
	}
	if err := p.m.persist(ctx, rs); err != nil {
		return nil, err
	}

	// Tell the agent to connect its remote module to the relay.
	start := protocol.RemoteStartMsg{SessionID: rs.ID, Token: token, RelayURL: agentURL}
	data, _ := json.Marshal(start)
	frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgRemoteStart, Data: data})
	p.m.hub.Send(agent.ID, frame)
	return rs, nil
}

// ---------- meshcentral ----------

type meshProvider struct{ m *Manager }

func (p *meshProvider) Name() string { return "meshcentral" }

// StartSession builds a connect URL into MeshCentral for the agent's node. When
// a login token key is configured it mints a login token so the session embeds
// without a second login (like Tactical RMM); otherwise it returns a deep link
// that works once the technician is logged into MeshCentral.
func (p *meshProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	if p.m.meshURL == "" {
		return nil, fmt.Errorf("MeshCentral URL not configured (RMM_MESHCENTRAL_URL)")
	}
	if agent.MeshID == nil || *agent.MeshID == "" {
		return nil, fmt.Errorf("agent has no MeshCentral node id (set it via PATCH /api/devices/{id}/remote-ids)")
	}
	base := strings.TrimRight(p.m.meshURL, "/")
	// viewmode=11 = desktop; hide=31 hides the MeshCentral chrome for embedding.
	connect := fmt.Sprintf("%s/?viewmode=11&hide=31&node=%s", base, *agent.MeshID)
	if p.m.meshTokenKey != "" {
		tok, err := meshLoginToken(p.m.meshTokenKey, p.m.meshUser)
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

// ---------- rustdesk ----------

type rustdeskProvider struct{ m *Manager }

func (p *rustdeskProvider) Name() string { return "rustdesk" }

// StartSession returns a rustdesk:// URI that launches the technician's local
// RustDesk client and connects to the agent's RustDesk ID.
func (p *rustdeskProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string) (*models.RemoteSession, error) {
	if agent.RustDeskID == nil || *agent.RustDeskID == "" {
		return nil, fmt.Errorf("agent has no RustDesk id")
	}
	connect := fmt.Sprintf("rustdesk://connect/%s", *agent.RustDeskID)
	rs := &models.RemoteSession{
		AgentID:    agent.ID,
		Provider:   "rustdesk",
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
