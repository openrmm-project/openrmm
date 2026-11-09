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
	"regexp"
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
	StartSession(ctx context.Context, agent *models.Agent, createdBy string, opts StartOptions) (*models.RemoteSession, error)
}

type StartOptions struct {
	AllowFiles bool
	// NoChat asks the sidecar not to open its chat window on the endpoint: a
	// guest's session, whose chat the server carries to the technician's.
	// Sidecars before 0.18 open it anyway.
	NoChat bool
	// MeshHostname lets a device with no linked MeshCentral node open by its
	// hostname, when that is turned on. Only a caller with access to every
	// device gets it: with a login token MeshCentral opens as
	// RMM_MESHCENTRAL_USER and looks the name up among every computer it
	// knows, so a restricted technician could reach one outside their scope by
	// giving a device of theirs that computer's name.
	MeshHostname bool
}

// Manager holds the configured providers and shared dependencies.
type Manager struct {
	mu           sync.RWMutex
	store        *store.Store
	hub          *hub.Hub
	meshURL      string
	meshTokenKey string
	meshUser     string
	meshEnabled  bool
	// meshMatchHostname opens a device with no linked MeshCentral node by its
	// hostname instead of refusing. Off unless an administrator turns it on:
	// MeshCentral picks one of several computers sharing a name, silently.
	meshMatchHostname bool
	rustdeskEnabled   bool
	providers         map[string]Provider

	// pending correlates on-demand remote_query requests with their replies.
	pendingMu sync.Mutex
	pending   map[string]chan protocol.RemoteQueryResultMsg
}

// UpdateMeshConfig swaps MeshCentral settings without rebuilding the server.
func (m *Manager) UpdateMeshConfig(meshURL, meshTokenKey, meshUser string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.meshURL = meshURL
	m.meshTokenKey = meshTokenKey
	m.meshUser = meshUser
	m.meshEnabled = enabled
}

// SetMeshMatchHostname toggles opening unlinked devices in MeshCentral by
// hostname at runtime.
func (m *Manager) SetMeshMatchHostname(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.meshMatchHostname = enabled
}

// SetRustDeskEnabled toggles the RustDesk provider at runtime.
func (m *Manager) SetRustDeskEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rustdeskEnabled = enabled
}

// NewManager wires up all providers.
func NewManager(s *store.Store, h *hub.Hub, meshURL, meshTokenKey, meshUser string, meshEnabled, rustdeskEnabled bool) *Manager {
	m := &Manager{store: s, hub: h, meshURL: meshURL,
		meshTokenKey: meshTokenKey, meshUser: meshUser, meshEnabled: meshEnabled, rustdeskEnabled: rustdeskEnabled,
		providers: map[string]Provider{}, pending: map[string]chan protocol.RemoteQueryResultMsg{}}
	register := func(p Provider) { m.providers[p.Name()] = p }
	register(&nativeProvider{m: m})
	register(&filesProvider{m: m})
	register(&terminalProvider{m: m})
	register(&meshProvider{m: m})
	register(&rustdeskProvider{m: m})
	return m
}

// queryAgent sends an on-demand remote_query to an online agent and waits for the
// reply (bounded). It is how the RustDesk provider reads the peer id at click time.
func (m *Manager) queryAgent(ctx context.Context, agentID, kind string) (string, error) {
	if !m.hub.IsOnline(agentID) {
		return "", fmt.Errorf("agent %s is offline", agentID)
	}
	reqID := auth.NewToken()
	ch := make(chan protocol.RemoteQueryResultMsg, 1)
	m.pendingMu.Lock()
	m.pending[reqID] = ch
	m.pendingMu.Unlock()
	defer func() {
		m.pendingMu.Lock()
		delete(m.pending, reqID)
		m.pendingMu.Unlock()
	}()

	q := protocol.RemoteQueryMsg{RequestID: reqID, Kind: kind}
	data, _ := json.Marshal(q)
	frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgRemoteQuery, Data: data})
	if !m.hub.Send(agentID, frame) {
		return "", fmt.Errorf("agent %s is offline", agentID)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("the agent did not respond in time")
	case res := <-ch:
		if res.Error != "" {
			return "", fmt.Errorf("%s", res.Error)
		}
		if res.Value == "" {
			return "", fmt.Errorf("the agent returned an empty value")
		}
		return res.Value, nil
	}
}

// ResolveQuery delivers an agent's remote_query reply to the waiting caller. It is
// invoked from the agent WebSocket read loop. Unknown/expired request ids are
// dropped (the caller may have already timed out).
func (m *Manager) ResolveQuery(res protocol.RemoteQueryResultMsg) {
	m.pendingMu.Lock()
	ch, ok := m.pending[res.RequestID]
	m.pendingMu.Unlock()
	if ok {
		select {
		case ch <- res:
		default:
		}
	}
}

// Capabilities asks a connected agent for a conservative native-remote
// preflight. It is intentionally on-demand so stale desktop/session state is
// never silently represented as current.
func (m *Manager) Capabilities(ctx context.Context, agentID string) (*protocol.RemoteCapabilities, error) {
	value, err := m.queryAgent(ctx, agentID, protocol.RemoteQueryCapabilities)
	if err != nil {
		return nil, err
	}
	var caps protocol.RemoteCapabilities
	if err := json.Unmarshal([]byte(value), &caps); err != nil {
		return nil, fmt.Errorf("invalid remote capability response: %w", err)
	}
	return &caps, nil
}

// Start opens a session using the named provider (or the agent's best default).
func (m *Manager) Start(ctx context.Context, providerName string, agent *models.Agent, createdBy string, opts StartOptions) (*models.RemoteSession, error) {
	p, ok := m.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("unknown remote provider %q", providerName)
	}
	return p.StartSession(ctx, agent, createdBy, opts)
}

// persist saves a session and emits an audit record.
func (m *Manager) persist(ctx context.Context, rs *models.RemoteSession) error {
	if err := m.store.CreateRemoteSession(ctx, rs); err != nil {
		return err
	}
	host := ""
	if a, err := m.store.GetAgent(ctx, rs.AgentID); err == nil && a != nil {
		host = a.Hostname
	}
	fields := map[string]string{"provider": rs.Provider, "agent": rs.AgentID, "host": host}
	if rs.MatchedBy != "" {
		// How an external provider found the device: a linked id, or a name
		// that another computer could share.
		fields["matched_by"] = rs.MatchedBy
	}
	detail, _ := json.Marshal(fields)
	_ = m.store.AddAudit(ctx, rs.CreatedBy, "remote.start", rs.AgentID, detail)
	return nil
}

// ---------- native (built-in relay) ----------

// brokerRelay sets up a relayed session with distinct one-shot tickets for the
// agent and viewer. The relay pairs peers by non-secret session id. kind tells
// the agent which module to attach (desktop sidecar vs. elevated terminal); it is
// also recorded as the session provider so the audit/list reflect what was opened.
func (m *Manager) brokerRelay(ctx context.Context, agent *models.Agent, createdBy, provider, kind string, allowFiles *bool, noChat bool) (*models.RemoteSession, error) {
	if !m.hub.IsOnline(agent.ID) {
		return nil, fmt.Errorf("agent %s is offline", agent.ID)
	}
	viewerTicket := auth.NewToken()
	agentTicket := auth.NewToken()

	rs := &models.RemoteSession{
		AgentID:      agent.ID,
		Provider:     provider,
		Status:       "pending",
		ViewerTicket: viewerTicket,
		AgentTicket:  agentTicket,
		CreatedBy:    createdBy,
		ExpiresAt:    time.Now().Add(sessionTTL),
	}
	if err := m.persist(ctx, rs); err != nil {
		return nil, err
	}

	// Tell the agent to connect the requested remote module to the relay.
	start := protocol.RemoteStartMsg{SessionID: rs.ID, AgentTicket: agentTicket, Token: agentTicket, Kind: kind, AllowFiles: allowFiles, NoChat: noChat}
	data, _ := json.Marshal(start)
	frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgRemoteStart, Data: data})
	if !m.hub.Send(agent.ID, frame) {
		_ = m.store.SetRemoteSessionStatus(ctx, rs.ID, "ended")
		return nil, fmt.Errorf("agent %s went offline before the remote session could start", agent.ID)
	}
	return rs, nil
}

type nativeProvider struct{ m *Manager }

func (p *nativeProvider) Name() string { return "native" }

// StartSession brokers a relayed remote-desktop session (media sidecar).
func (p *nativeProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string, opts StartOptions) (*models.RemoteSession, error) {
	allowFiles := opts.AllowFiles
	return p.m.brokerRelay(ctx, agent, createdBy, "native", protocol.RemoteKindDesktop, &allowFiles, opts.NoChat)
}

// ---------- files (file transfer only — no screen capture) ----------

type filesProvider struct{ m *Manager }

func (p *filesProvider) Name() string { return "files" }

// StartSession brokers a relayed session whose agent module is the media sidecar
// in file-transfer-only mode: it serves the file browser without capturing or
// streaming the screen. Opening "Remote ▸ File transfer" must NOT start a remote
// desktop session — only this. (A desktop session can still open the file
// browser in-band via the Files button, which stays the native provider.)
func (p *filesProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string, _ StartOptions) (*models.RemoteSession, error) {
	allowFiles := true
	return p.m.brokerRelay(ctx, agent, createdBy, "files", protocol.RemoteKindFiles, &allowFiles, false)
}

// ---------- terminal (elevated shell over the relay) ----------

type terminalProvider struct{ m *Manager }

func (p *terminalProvider) Name() string { return "terminal" }

// StartSession brokers a relayed session whose agent module is an interactive
// shell on a PTY. The shell runs inside the agent process, so it inherits the
// agent's privileges — and the agent runs as a system service (root on
// Linux/macOS, SYSTEM on Windows), so the terminal session is elevated.
func (p *terminalProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string, _ StartOptions) (*models.RemoteSession, error) {
	return p.m.brokerRelay(ctx, agent, createdBy, "terminal", protocol.RemoteKindTerminal, nil, false)
}

// ---------- meshcentral ----------

type meshProvider struct{ m *Manager }

func (p *meshProvider) Name() string { return "meshcentral" }

// StartSession builds a connect URL into MeshCentral for the agent's node. When
// a login token key is configured it mints a login token so the session embeds
// without a second login; otherwise it returns a deep link
// that works once the technician is logged into MeshCentral.
//
// A device with no linked node is refused, unless opening unlinked devices by
// hostname is turned on: the link then asks MeshCentral for the computer whose
// own reported name is the device's hostname (gotodevicername).
func (p *meshProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string, opts StartOptions) (*models.RemoteSession, error) {
	p.m.mu.RLock()
	meshURL, meshTokenKey, meshUser, enabled, byHostname := p.m.meshURL, p.m.meshTokenKey, p.m.meshUser, p.m.meshEnabled, p.m.meshMatchHostname
	p.m.mu.RUnlock()
	if !enabled {
		return nil, fmt.Errorf("the MeshCentral provider is disabled")
	}
	if meshURL == "" {
		return nil, fmt.Errorf("MeshCentral URL not configured (RMM_MESHCENTRAL_URL)")
	}
	var nodeID, hostname, matchedBy string
	switch {
	case agent.MeshID != nil && *agent.MeshID != "":
		nodeID, matchedBy = *agent.MeshID, "node"
	case byHostname && !opts.MeshHostname:
		return nil, fmt.Errorf("this device has no linked MeshCentral node, and opening by hostname is only for users with access to every device: ask an administrator to link its node")
	case byHostname:
		name, err := meshDeviceName(agent.Hostname)
		if err != nil {
			return nil, err
		}
		hostname, matchedBy = name, "hostname"
	default:
		return nil, fmt.Errorf("agent has no MeshCentral node id: link its node (PATCH /api/devices/{id}/remote-ids), or turn on opening unlinked devices by hostname")
	}
	loginToken := ""
	if meshTokenKey != "" {
		tok, err := meshLoginToken(meshTokenKey, meshUser)
		if err != nil {
			return nil, fmt.Errorf("mesh login token: %w", err)
		}
		loginToken = tok
	}
	rs := &models.RemoteSession{
		AgentID:    agent.ID,
		Provider:   "meshcentral",
		Status:     "ready",
		Token:      auth.NewToken(),
		ConnectURL: meshConnectURL(strings.TrimRight(meshURL, "/"), loginToken, nodeID, hostname),
		MatchedBy:  matchedBy,
		CreatedBy:  createdBy,
		ExpiresAt:  time.Now().Add(sessionTTL),
	}
	if err := p.m.persist(ctx, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// meshConnectURL is the link into MeshCentral's remote desktop for one device:
// by its linked node id when there is one, otherwise by its hostname.
// viewmode=11 opens the desktop; hide=31 hides MeshCentral's own chrome.
func meshConnectURL(base, loginToken, nodeID, hostname string) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("/?")
	if loginToken != "" {
		b.WriteString("login=" + url.QueryEscape(loginToken) + "&")
	}
	b.WriteString("viewmode=11&hide=31&")
	if nodeID != "" {
		b.WriteString("node=" + nodeID)
	} else {
		// Written as it is, not escaped: MeshCentral reads its page arguments
		// without decoding them and drops any value holding '%' or '+', so an
		// escaped name could never match. meshDeviceName admits only
		// characters that need no escaping.
		b.WriteString("gotodevicername=" + hostname)
	}
	return b.String()
}

// meshDeviceNamePattern is what MeshCentral can match from a link: its page
// splits arguments on '?', '&' and '|', and drops a value containing any of
// < > & " ' + ( ) # %. A hostname of letters, digits, dots, hyphens and
// underscores passes through untouched; anything else could not be found, and
// would let a device's reported hostname add arguments to the link.
var meshDeviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)

// meshNumericArg is any value MeshCentral's page could turn into a number
// (parseInt(v) == v) before comparing it loosely with each computer's name:
// "01234" or "1234." would find a computer named "1234", "1e0" one named "1",
// and "0x10" one named "16". Every name that reads as a JavaScript number
// literal is refused, rather than risk opening another computer.
var meshNumericArg = regexp.MustCompile(`^(?:0[xXbBoO][0-9A-Fa-f]+|[0-9]+\.?[0-9]*(?:[eE][+-]?[0-9]+)?)$`)

// meshDeviceName is the hostname to look up in MeshCentral, or an error saying
// why this device has to be linked to its node instead. The hostname is the
// one the agent reports, so it is checked and never echoed back.
func meshDeviceName(hostname string) (string, error) {
	if hostname == "" {
		return "", fmt.Errorf("this device reports no hostname to look up in MeshCentral: link its node instead")
	}
	if !meshDeviceNamePattern.MatchString(hostname) {
		return "", fmt.Errorf("MeshCentral cannot look up this device's hostname from a link (only letters, digits, dots, hyphens and underscores): link its node instead")
	}
	if meshNumericArg.MatchString(hostname) {
		return "", fmt.Errorf("MeshCentral reads a hostname that is only a number as a number and could find another computer: link its node instead")
	}
	return hostname, nil
}

// ---------- rustdesk (on-demand id read; client installed out-of-band) ----------

type rustdeskProvider struct{ m *Manager }

func (p *rustdeskProvider) Name() string { return "rustdesk" }

// StartSession reads the device's RustDesk peer id on demand from the agent (the
// RustDesk client is installed separately, not by this RMM) and returns a
// rustdesk://<id> deep link the technician's RustDesk viewer opens. The media path
// goes through the operator's RustDesk server (hbbs/hbbr), never through OpenRMM.
func (p *rustdeskProvider) StartSession(ctx context.Context, agent *models.Agent, createdBy string, _ StartOptions) (*models.RemoteSession, error) {
	p.m.mu.RLock()
	enabled := p.m.rustdeskEnabled
	p.m.mu.RUnlock()
	if !enabled {
		return nil, fmt.Errorf("the RustDesk provider is disabled")
	}
	id, err := p.m.queryAgent(ctx, agent.ID, protocol.RemoteQueryRustDeskID)
	if err != nil {
		return nil, fmt.Errorf("read RustDesk id: %w", err)
	}
	rs := &models.RemoteSession{
		AgentID:    agent.ID,
		Provider:   "rustdesk",
		Status:     "ready",
		Token:      auth.NewToken(),
		ConnectURL: "rustdesk://" + id,
		CreatedBy:  createdBy,
		ExpiresAt:  time.Now().Add(sessionTTL),
	}
	if err := p.m.persist(ctx, rs); err != nil {
		return nil, err
	}
	return rs, nil
}
