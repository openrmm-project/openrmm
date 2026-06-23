// Package api wires the HTTP/REST surface, the agent WebSocket, and the remote
// relay into a single router.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/mcpserver"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/scheduler"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// Per-IP rate limits (requests per minute).
const (
	authRateLimit = 10 // login / bootstrap — strict (anti brute-force)
	mcpRateLimit  = 60 // external AI clients
	// The general API limit is per-IP and tunable via RMM_API_RATE_LIMIT
	// (config.APIRateLimit, default 600) — see s.apiRateLimit().
)

// maxAPIBodyBytes caps the size of an API request body (anti-DoS): a 1 MiB
// ceiling is ample for JSON payloads and human-written scripts, while keeping a
// single authenticated caller from forcing unbounded memory with a huge body.
const maxAPIBodyBytes = 1 << 20

type ctxKey string

const (
	actorKey     ctxKey = "actor"
	roleKey      ctxKey = "role"
	scopeUserKey ctxKey = "scopeUser"
)

// Server holds all HTTP dependencies.
type Server struct {
	cfg    *config.Config
	store  *store.Store
	hub    *hub.Hub
	jobs   *jobs.Service
	remote *remote.Manager
	relay  *remote.Relay
	chat   *ai.Engine
	sched  *scheduler.Scheduler
}

// New builds the API server.
func New(cfg *config.Config, s *store.Store, h *hub.Hub, j *jobs.Service, rm *remote.Manager, relay *remote.Relay, chat *ai.Engine, sched *scheduler.Scheduler) *Server {
	return &Server{cfg: cfg, store: s, hub: h, jobs: j, remote: rm, relay: relay, chat: chat, sched: sched}
}

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(s.cors)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	// WebSocket endpoints authenticate via their own protocol / tokens.
	r.Get("/ws/agent", s.handleAgentWS)
	r.Get("/ws/remote", s.handleRelayWS)

	// Unauthenticated auth endpoints — strictly rate-limited (anti brute-force).
	authLimiter := s.rateLimit(authRateLimit)
	r.With(authLimiter, maxBody).Post("/api/auth/login", s.login)
	r.With(authLimiter, maxBody).Post("/api/auth/bootstrap", s.bootstrapAdmin) // first-admin (only when no users exist)

	// MCP server (Streamable HTTP) — external AI clients (Claude, ChatGPT, …)
	// connect here. Same bearer auth as the REST API; tech role minimum since it
	// can propose jobs (which still require human approval).
	mcpHandler := mcpserver.Handler(s.store, s.jobs, func(r *http.Request) (string, string) {
		return actorOf(r), s.scopeUserID(r)
	})
	r.Group(func(r chi.Router) {
		r.Use(s.rateLimit(mcpRateLimit))
		r.Use(maxBody)
		r.Use(s.mcpIPAllowlist)
		r.Use(s.authMiddleware)
		r.Use(s.requireRole(models.RoleTech))
		r.Handle("/mcp", mcpHandler)
		r.Handle("/mcp/*", mcpHandler)
	})

	r.Route("/api", func(r chi.Router) {
		r.Use(s.rateLimit(s.apiRateLimit())) // per-IP safety net (anti token-guessing)
		r.Use(maxBody)                       // cap request body size (anti-DoS)
		r.Use(s.authMiddleware)

		// ---- read-only (any authenticated role) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(models.RoleReadOnly))
			r.Post("/auth/logout", s.logout)
			r.Get("/me", s.me)
			r.Post("/me/password", s.changePassword)
			r.Post("/me/2fa/setup", s.setup2FA)
			r.Post("/me/2fa/enable", s.enable2FA)
			r.Post("/me/2fa/disable", s.disable2FA)
			r.Post("/me/2fa/recovery-codes", s.regenRecoveryCodes)
			r.Get("/config", s.getConfig)

			r.Get("/clients", s.listClients)
			r.Get("/sites", s.listSites)
			r.Get("/devices", s.listDevices)
			r.Get("/devices/{id}", s.getDevice)
			r.Get("/devices/{id}/inventory", s.getInventory)
			// Remote access is allowed for readonly (remote assistance/observation);
			// it still cannot run scripts, approve jobs, or change config.
			r.Post("/devices/{id}/remote", s.startRemote)
			r.Get("/jobs", s.listJobs)
			r.Get("/jobs/{id}", s.getJob)
			r.Get("/checks", s.listChecks)
			r.Get("/checks/{id}/results", s.listCheckResults)
			r.Get("/tasks", s.listTasks)
			r.Get("/alerts", s.listAlerts)
			r.Get("/chat/sessions", s.listChatSessions)
			r.Get("/chat/sessions/{id}/messages", s.listChatMessages)
		})

		// ---- tech (run things) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(models.RoleTech))
			// Script library: tech+ (a script body can carry sensitive commands).
			r.Get("/scripts", s.listScripts)
			r.Get("/scripts/{id}", s.getScript)
			r.Post("/devices/{id}/run-script", s.runLibraryScript)
			r.Patch("/devices/{id}", s.updateDeviceMeta) // description + note (scope-checked)
			r.Post("/jobs", s.createJob)
			r.Post("/jobs/bulk", s.createBulkJob)
			r.Get("/jobs/batch/{batch_id}", s.getBatch)
			r.Post("/jobs/batch/{batch_id}/approve", s.approveBatch)
			r.Post("/jobs/batch/{batch_id}/reject", s.rejectBatch)
			r.Post("/jobs/{id}/approve", s.approveJob)
			r.Post("/jobs/{id}/reject", s.rejectJob)
			r.Post("/jobs/{id}/cancel", s.cancelJob)
			r.Post("/alerts/{id}/ack", s.ackAlert)
			r.Post("/alerts/{id}/resolve", s.resolveAlert)
			r.Post("/chat/sessions", s.createChatSession)
			r.Delete("/chat/sessions/{id}", s.deleteChatSession)
			r.Post("/chat/sessions/{id}/messages", s.postChatMessage)
		})

		// ---- admin (manage config) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(models.RoleAdmin))
			// Cross-tenant management data — admin-only to avoid leaking other
			// tenants' activity/structure to restricted users.
			r.Get("/audit", s.listAudit)
			r.Get("/groups", s.listGroups)
			r.Get("/policies", s.listPolicies)
			r.Get("/policies/{id}/assignments", s.listPolicyAssignments)
			r.Put("/config", s.updateConfig)
			r.Post("/enroll-tokens", s.createEnrollToken)
			r.Get("/api-tokens", s.listAPITokens)
			r.Post("/api-tokens", s.createAPIToken)
			r.Delete("/api-tokens/{id}", s.deleteAPIToken)
			r.Get("/users", s.listUsers)
			r.Post("/users", s.createUser)
			r.Patch("/users/{id}", s.updateUser)
			r.Get("/users/{id}/scopes", s.getUserScopes)
			r.Put("/users/{id}/scopes", s.setUserScopes)
			r.Post("/users/{id}/2fa/reset", s.reset2FA)
			r.Delete("/users/{id}", s.deleteUser)
			r.Delete("/devices/{id}", s.deleteDevice)
			r.Patch("/devices/{id}/group", s.setDeviceGroup)
			r.Patch("/devices/{id}/remote-ids", s.setDeviceRemoteIDs)
			r.Post("/groups", s.createGroup)
			r.Delete("/groups/{id}", s.deleteGroup)
			r.Post("/clients", s.createClient)
			r.Patch("/clients/{id}", s.updateClient)
			r.Delete("/clients/{id}", s.deleteClient)
			r.Post("/sites", s.createSite)
			r.Patch("/sites/{id}", s.updateSite)
			r.Delete("/sites/{id}", s.deleteSite)
			r.Patch("/devices/{id}/site", s.setDeviceSite)
			r.Post("/scripts", s.createScript)
			r.Put("/scripts/{id}", s.updateScript)
			r.Delete("/scripts/{id}", s.deleteScript)
			r.Post("/checks", s.createCheck)
			r.Put("/checks/{id}", s.updateCheck)
			r.Delete("/checks/{id}", s.deleteCheck)
			r.Post("/tasks", s.createTask)
			r.Put("/tasks/{id}", s.updateTask)
			r.Delete("/tasks/{id}", s.deleteTask)
			r.Post("/policies", s.createPolicy)
			r.Delete("/policies/{id}", s.deletePolicy)
			r.Post("/policies/{id}/assignments", s.createPolicyAssignment)
			r.Delete("/policies/assignments/{aid}", s.deletePolicyAssignment)
		})
	})

	return r
}

// ---------- middleware & helpers ----------

// roleLevel ranks roles for comparison.
func roleLevel(role string) int {
	switch role {
	case models.RoleAdmin:
		return 3
	case models.RoleTech:
		return 2
	case models.RoleReadOnly:
		return 1
	default:
		return 0
	}
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		var actor, role, scopeUser string
		switch {
		case auth.ConstantTimeEqual(token, s.cfg.AdminToken):
			actor, role = "admin", models.RoleAdmin
		default:
			hash := auth.Hash(token)
			if uid, rl, err := s.store.SessionPrincipal(r.Context(), hash); err == nil {
				actor, role, scopeUser = uid, rl, uid
			} else if rl, uid, ok, _ := s.store.APITokenRole(r.Context(), hash); ok {
				actor, role, scopeUser = "api", rl, uid // uid set only for tokens bound to a user
			} else {
				writeErr(w, http.StatusUnauthorized, "invalid token")
				return
			}
		}
		ctx := context.WithValue(r.Context(), actorKey, actor)
		ctx = context.WithValue(ctx, roleKey, role)
		ctx = context.WithValue(ctx, scopeUserKey, scopeUser)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// maxBody caps the request body so a single caller can't force unbounded memory
// with a huge payload. On exceed, the next read (e.g. in decode) returns an
// error and the handler responds 400; MaxBytesReader also flags the connection
// to close. Applied to all /api routes.
func maxBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// requireRole rejects principals below the required role level.
func (s *Server) requireRole(minRole string) func(http.Handler) http.Handler {
	need := roleLevel(minRole)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if roleLevel(roleOf(r)) < need {
				writeErr(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// apiRateLimit is the effective per-IP/minute budget for the general API,
// from config (RMM_API_RATE_LIMIT) with a safe default for the chatty SPA.
func (s *Server) apiRateLimit() int {
	if s.cfg.APIRateLimit > 0 {
		return s.cfg.APIRateLimit
	}
	return 600
}

// rateLimit returns a per-IP rate-limiting middleware. The key respects the
// trusted-proxy setting so it works both directly and behind a reverse proxy.
func (s *Server) rateLimit(perMinute int) func(http.Handler) http.Handler {
	return httprate.Limit(
		perMinute, time.Minute,
		httprate.WithKeyFuncs(func(r *http.Request) (string, error) {
			return clientIP(r, s.cfg.TrustProxyHeaders), nil
		}),
		httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded; slow down")
		}),
	)
}

// cors adds CORS headers so a browser frontend (served from another origin) can
// call the API. Bearer-token auth uses no cookies, so "*" is safe by default.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allow := ""
		for _, o := range s.cfg.CORSOrigins {
			if o == "*" {
				allow = "*"
				break
			}
			if o == origin && origin != "" {
				allow = origin
				break
			}
		}
		if allow != "" {
			w.Header().Set("Access-Control-Allow-Origin", allow)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version")
			w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// getConfig returns non-secret runtime config for the UI's settings screen.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	eff, err := settings.Effective(r.Context(), s.store, s.cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ai": map[string]any{
			"provider":    eff.AIProvider,
			"base_url":    eff.AIBaseURL,
			"model":       eff.AIModel,
			"configured":  eff.AIAPIKey != "",
			"api_key_set": eff.AIAPIKey != "",
		},
		"remote": map[string]any{
			"providers":             []string{"native", "terminal", "meshcentral"},
			"default":               eff.RemoteDefaultProvider,
			"meshcentral_url":       eff.MeshCentralURL,
			"meshcentral_user":      eff.MeshCentralUser,
			"meshcentral_token_set": eff.MeshCentralTokenKey != "",
			"meshcentral_enabled":   eff.MeshCentralEnabled,
			"rustdesk_enabled":      eff.RustDeskEnabled,
		},
		"server": map[string]any{
			"version":               "0.2.1",
			"base_url":              s.cfg.PublicURL,
			"allow_unapproved_jobs": s.cfg.AllowUnapprovedJobs,
		},
	})
}

// updateConfig persists editable runtime config. Secret inputs are write-only:
// an empty secret field preserves the existing value, and responses only expose
// boolean "*_set" flags.
func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	var in settings.SaveRequest
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Remote.DefaultProvider != "" && !oneOf(in.Remote.DefaultProvider, "native", "meshcentral") {
		writeErr(w, http.StatusBadRequest, "invalid remote provider")
		return
	}
	before, _ := settings.Effective(r.Context(), s.store, s.cfg)
	eff, err := settings.Save(r.Context(), s.store, s.cfg, in)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Apply live to the components that need it (each guards its own state).
	// We intentionally do NOT mutate the shared *config.Config: settings.Effective
	// already overlays DB values on top of it, so reads stay race-free.
	s.chat.UpdateProvider(eff.AIBaseURL, eff.AIAPIKey, eff.AIModel)
	s.remote.UpdateMeshConfig(eff.MeshCentralURL, eff.MeshCentralTokenKey, eff.MeshCentralUser, eff.MeshCentralEnabled)
	s.remote.SetRustDeskEnabled(eff.RustDeskEnabled)

	changed := changedSettings(before, eff, in)
	_ = s.store.AddAudit(r.Context(), actorOf(r), "config.update", "app_settings", settings.AuditDetail(changed))
	s.getConfig(w, r)
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func changedSettings(before, after settings.Editable, in settings.SaveRequest) []string {
	var out []string
	add := func(name string, changed bool) {
		if changed {
			out = append(out, name)
		}
	}
	add("ai.provider", before.AIProvider != after.AIProvider)
	add("ai.base_url", before.AIBaseURL != after.AIBaseURL)
	add("ai.model", before.AIModel != after.AIModel)
	add("ai.api_key", strings.TrimSpace(in.AI.APIKey) != "")
	add("remote.default", before.RemoteDefaultProvider != after.RemoteDefaultProvider)
	add("remote.meshcentral_url", before.MeshCentralURL != after.MeshCentralURL)
	add("remote.meshcentral_user", before.MeshCentralUser != after.MeshCentralUser)
	add("remote.meshcentral_token_key", strings.TrimSpace(in.Remote.MeshTokenKey) != "")
	add("remote.meshcentral_enabled", before.MeshCentralEnabled != after.MeshCentralEnabled)
	add("remote.rustdesk_enabled", before.RustDeskEnabled != after.RustDeskEnabled)
	return out
}

// mcpIPAllowlist restricts the MCP endpoint to configured IPs/CIDRs (defense in
// depth). If no allowlist is configured, it is a no-op (the deployer may rely on
// a private network / proxy instead).
func (s *Server) mcpIPAllowlist(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.MCPAllowedIPs) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if !ipAllowed(clientIP(r, s.cfg.TrustProxyHeaders), s.cfg.MCPAllowedIPs) {
			writeErr(w, http.StatusForbidden, "source IP not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func ipAllowed(ip string, allowed []string) bool {
	parsed := net.ParseIP(ip)
	for _, entry := range allowed {
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			if parsed != nil && cidr.Contains(parsed) {
				return true
			}
			continue
		}
		if entry == ip {
			return true
		}
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

func actorOf(r *http.Request) string {
	if v, ok := r.Context().Value(actorKey).(string); ok {
		return v
	}
	return "unknown"
}

func roleOf(r *http.Request) string {
	if v, ok := r.Context().Value(roleKey).(string); ok {
		return v
	}
	return ""
}

// scopeUserID returns the user id whose access scope restricts this request, or
// "" when the principal has full access (admin role, bootstrap/admin token, or
// an unscoped API token).
func (s *Server) scopeUserID(r *http.Request) string {
	if roleOf(r) == models.RoleAdmin {
		return ""
	}
	if v, ok := r.Context().Value(scopeUserKey).(string); ok {
		return v
	}
	return ""
}

// agentAllowed reports whether the request's principal may access the agent.
func (s *Server) agentAllowed(r *http.Request, agentID string) bool {
	uid := s.scopeUserID(r)
	if uid == "" {
		return true
	}
	ok, err := s.store.AgentAllowedForUser(r.Context(), uid, agentID)
	return err == nil && ok
}

// requireAgent 403s when the principal may not access the agent. Returns false
// when it has already written the response.
func (s *Server) requireAgent(w http.ResponseWriter, r *http.Request, agentID string) bool {
	if s.agentAllowed(r, agentID) {
		return true
	}
	writeErr(w, http.StatusForbidden, "device is outside your access scope")
	return false
}

// scopedAgentSet returns (set, true) for a restricted principal — the agent ids
// it may see — used to filter list endpoints. Returns (nil, false) for full access.
func (s *Server) scopedAgentSet(r *http.Request) (map[string]bool, bool) {
	uid := s.scopeUserID(r)
	if uid == "" {
		return nil, false
	}
	agents, _ := s.store.ListAgentsForUser(r.Context(), uid)
	set := make(map[string]bool, len(agents))
	for _, a := range agents {
		set[a.ID] = true
	}
	return set, true
}

func inScope(set map[string]bool, id *string) bool { return id != nil && set[*id] }

func writeJSON(w http.ResponseWriter, code int, v any) {
	denilSlices(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// denilSlices replaces nil slice values in a top-level response map with empty
// slices, so list endpoints always emit `[]` instead of `null` (Go marshals a
// nil slice as null, which crashes naive clients doing `.map`/`.length`).
func denilSlices(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	for k, val := range m {
		rv := reflect.ValueOf(val)
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			m[k] = []any{}
		}
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	// Don't leak internal detail (DB/driver/provider errors) to clients on 5xx;
	// log it server-side and return a generic message. 4xx keep their message
	// since they're caller-actionable validation errors.
	if code >= 500 {
		log.Printf("server error %d: %s", code, msg)
		msg = "internal server error"
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func httpStatusFor(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// ---------- enroll tokens ----------

func (s *Server) createEnrollToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Description string  `json:"description"`
		Uses        int     `json:"uses"`
		TTLHours    int     `json:"ttl_hours"`
		SiteID      *string `json:"site_id"`
	}
	_ = decode(r, &in)
	if in.Uses <= 0 {
		in.Uses = 1
	}
	var expires *time.Time
	if in.TTLHours > 0 {
		t := time.Now().Add(time.Duration(in.TTLHours) * time.Hour)
		expires = &t
	}
	tok, err := s.store.CreateEnrollToken(r.Context(), auth.NewToken(), in.Description, in.Uses, expires, in.SiteID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, tok)
}

// ---------- devices ----------

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	var agents []models.Agent
	var err error
	switch {
	case r.URL.Query().Get("site_id") != "": // lazy load for the scope tree
		agents, err = s.store.ListAgentsBySite(r.Context(), r.URL.Query().Get("site_id"))
	case r.URL.Query().Get("q") != "": // scope-tree search by hostname
		agents, err = s.store.SearchAgents(r.Context(), r.URL.Query().Get("q"), 200)
	case s.scopeUserID(r) != "":
		agents, err = s.store.ListAgentsForUser(r.Context(), s.scopeUserID(r))
	default:
		agents, err = s.store.ListAgents(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": agents})
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	a, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) getInventory(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	inv, err := s.store.GetInventory(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) startRemote(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	var in struct {
		Provider string `json:"provider"`
	}
	_ = decode(r, &in)
	if in.Provider == "" {
		// Resolve the live default (DB-overlaid) without reading the shared cfg.
		if eff, err := settings.Effective(r.Context(), s.store, s.cfg); err == nil && eff.RemoteDefaultProvider != "" {
			in.Provider = eff.RemoteDefaultProvider
		} else {
			in.Provider = s.cfg.DefaultRemoteProvider
		}
	}
	// The terminal provider opens an elevated (root/SYSTEM) shell — equivalent to
	// running arbitrary scripts — so it needs tech+, unlike read-only remote
	// desktop assistance which any authenticated role may start. File transfer
	// reads and writes arbitrary endpoint files, so it is tech+ as well.
	if (in.Provider == "terminal" || in.Provider == "files") && roleLevel(roleOf(r)) < roleLevel(models.RoleTech) {
		writeErr(w, http.StatusForbidden, "this remote session requires the tech role")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	sess, err := s.remote.Start(r.Context(), in.Provider, agent, actorOf(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

// ---------- jobs ----------

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	js, err := s.store.ListJobs(r.Context(), r.URL.Query().Get("agent_id"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped := s.scopedAgentSet(r); scoped {
		f := js[:0]
		for _, j := range js {
			if set[j.AgentID] {
				f = append(f, j)
			}
		}
		js = f
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": js})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentID         string `json:"agent_id"`
		Shell           string `json:"shell"`
		Script          string `json:"script"`
		Description     string `json:"description"`
		RequireApproval *bool  `json:"require_approval"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.requireAgent(w, r, in.AgentID) {
		return
	}
	// Approval is mandatory unless the deployer explicitly opted into unapproved
	// jobs (RMM_ALLOW_UNAPPROVED_JOBS=true). Every ad-hoc script run is gated.
	require := true
	if in.RequireApproval != nil && s.cfg.AllowUnapprovedJobs {
		require = *in.RequireApproval
	}
	if in.Shell == "" {
		in.Shell = models.ShellBash
	}
	job, err := s.jobs.Propose(r.Context(), in.AgentID, in.Shell, in.Script, in.Description, actorOf(r), require)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

// createBulkJob fans a script out to many devices as ONE approval-gated batch.
// Targets are the union of explicit agent_ids + a device group + a site,
// intersected with the caller's access scope. Every job lands in
// awaiting_approval sharing a batch_id; nothing runs until the batch is approved.
func (s *Server) createBulkJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentIDs    []string `json:"agent_ids"`
		GroupID     string   `json:"group_id"`
		SiteID      string   `json:"site_id"`
		Shell       string   `json:"shell"`
		Script      string   `json:"script"`
		Description string   `json:"description"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Script == "" {
		writeErr(w, http.StatusBadRequest, "script is required")
		return
	}
	if in.Shell == "" {
		in.Shell = models.ShellBash
	}
	targets := map[string]bool{}
	for _, id := range in.AgentIDs {
		if id != "" {
			targets[id] = true
		}
	}
	if in.GroupID != "" {
		ags, err := s.store.ListAgentsByGroup(r.Context(), in.GroupID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, a := range ags {
			targets[a.ID] = true
		}
	}
	if in.SiteID != "" {
		ags, err := s.store.ListAgentsBySite(r.Context(), in.SiteID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, a := range ags {
			targets[a.ID] = true
		}
	}
	if set, scoped := s.scopedAgentSet(r); scoped {
		for id := range targets {
			if !set[id] {
				delete(targets, id)
			}
		}
	}
	if len(targets) == 0 {
		writeErr(w, http.StatusBadRequest, "no in-scope target devices resolved")
		return
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}

	// A deployment never spans companies: ProposeBulk splits the targets by
	// company and creates ONE batch per company (so a "deploy to all companies"
	// request fans out into one batch per company). Same engine as the AI tool.
	batches, err := s.jobs.ProposeBulk(r.Context(), ids, in.Shell, in.Script, in.Description, actorOf(r))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	type batchOut struct {
		ClientID string `json:"client_id,omitempty"`
		BatchID  string `json:"batch_id"`
		Count    int    `json:"count"`
	}
	out := make([]batchOut, len(batches))
	total := 0
	for i, b := range batches {
		out[i] = batchOut{ClientID: b.ClientID, BatchID: b.BatchID, Count: len(b.JobIDs)}
		total += len(b.JobIDs)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"batches": out, "count": total, "companies": len(out)})
}

// batchScopeOK 403s a scoped caller if the batch targets any out-of-scope device.
func (s *Server) batchScopeOK(w http.ResponseWriter, r *http.Request, batchID string) bool {
	set, scoped := s.scopedAgentSet(r)
	if !scoped {
		return true
	}
	jobs, err := s.store.ListJobsByBatch(r.Context(), batchID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return false
	}
	for _, j := range jobs {
		if !set[j.AgentID] {
			writeErr(w, http.StatusForbidden, "batch contains devices outside your access scope")
			return false
		}
	}
	return true
}

// getBatch returns the jobs in a batch (scope-filtered) for the review/approval UI.
func (s *Server) getBatch(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobsByBatch(r.Context(), chi.URLParam(r, "batch_id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if set, scoped := s.scopedAgentSet(r); scoped {
		out := make([]models.Job, 0, len(jobs))
		for _, j := range jobs {
			if set[j.AgentID] {
				out = append(out, j)
			}
		}
		jobs = out
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) approveBatch(w http.ResponseWriter, r *http.Request) {
	batchID := chi.URLParam(r, "batch_id")
	if !s.batchScopeOK(w, r, batchID) {
		return
	}
	n, err := s.jobs.ApproveBatch(r.Context(), batchID, actorOf(r))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approved": n})
}

func (s *Server) rejectBatch(w http.ResponseWriter, r *http.Request) {
	batchID := chi.URLParam(r, "batch_id")
	if !s.batchScopeOK(w, r, batchID) {
		return
	}
	n, err := s.jobs.RejectBatch(r.Context(), batchID, actorOf(r))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rejected": n})
}

// requireJobAgent 403s when the job's target agent is outside the principal's
// scope. Returns false when it has already written the response.
func (s *Server) requireJobAgent(w http.ResponseWriter, r *http.Request, jobID string) bool {
	if s.scopeUserID(r) == "" {
		return true
	}
	job, err := s.store.GetJob(r.Context(), jobID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return false
	}
	return s.requireAgent(w, r, job.AgentID)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	job, err := s.store.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) approveJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	job, err := s.jobs.Approve(r.Context(), chi.URLParam(r, "id"), actorOf(r))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) rejectJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	job, err := s.jobs.Reject(r.Context(), chi.URLParam(r, "id"), actorOf(r))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	job, err := s.jobs.Cancel(r.Context(), chi.URLParam(r, "id"), actorOf(r))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// ---------- audit ----------

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// ---------- chat ----------

func (s *Server) listChatSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListChatSessions(r.Context(), actorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) createChatSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	_ = decode(r, &in)
	sess, err := s.store.CreateChatSession(r.Context(), in.Title, actorOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

// requireChatOwner 403s unless the session belongs to the caller (chats are
// private per user). Returns false when it has written the response.
func (s *Server) requireChatOwner(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	owner, err := s.store.ChatSessionOwner(r.Context(), sessionID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "chat session not found")
		return false
	}
	if owner != actorOf(r) {
		writeErr(w, http.StatusForbidden, "not your chat session")
		return false
	}
	return true
}

func (s *Server) listChatMessages(w http.ResponseWriter, r *http.Request) {
	if !s.requireChatOwner(w, r, chi.URLParam(r, "id")) {
		return
	}
	msgs, err := s.store.ListChatMessages(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) deleteChatSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireChatOwner(w, r, id) {
		return
	}
	if err := s.store.DeleteChatSession(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) postChatMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if !s.requireChatOwner(w, r, sessionID) {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := decode(r, &in); err != nil || in.Content == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	// Name the conversation from its first message (only while still a placeholder).
	title := strings.TrimSpace(in.Content)
	if rs := []rune(title); len(rs) > 60 {
		title = string(rs[:60]) + "…"
	}
	_ = s.store.MaybeTitleChatSession(r.Context(), sessionID, title)
	reply, err := s.chat.Respond(r.Context(), sessionID, in.Content, actorOf(r), s.scopeUserID(r))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reply)
}
