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
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/mcpserver"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/scheduler"
	"github.com/p1ngu/rmm/internal/store"
)

type ctxKey string

const (
	actorKey ctxKey = "actor"
	roleKey  ctxKey = "role"
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

	// Unauthenticated auth endpoints.
	r.Post("/api/auth/login", s.login)
	r.Post("/api/auth/bootstrap", s.bootstrapAdmin) // first-admin creation (only when no users exist)

	// MCP server (Streamable HTTP) — external AI clients (Claude, ChatGPT, …)
	// connect here. Same bearer auth as the REST API; tech role minimum since it
	// can propose jobs (which still require human approval).
	mcpHandler := mcpserver.Handler(s.store, s.jobs, actorOf)
	r.Group(func(r chi.Router) {
		r.Use(s.mcpIPAllowlist)
		r.Use(s.authMiddleware)
		r.Use(s.requireRole(models.RoleTech))
		r.Handle("/mcp", mcpHandler)
		r.Handle("/mcp/*", mcpHandler)
	})

	r.Route("/api", func(r chi.Router) {
		r.Use(s.authMiddleware)

		// ---- read-only (any authenticated role) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(models.RoleReadOnly))
			r.Post("/auth/logout", s.logout)
			r.Get("/me", s.me)
			r.Get("/config", s.getConfig)

			r.Get("/clients", s.listClients)
			r.Get("/sites", s.listSites)
			r.Get("/groups", s.listGroups)
			r.Get("/devices", s.listDevices)
			r.Get("/devices/{id}", s.getDevice)
			r.Get("/devices/{id}/inventory", s.getInventory)
			r.Get("/scripts", s.listScripts)
			r.Get("/scripts/{id}", s.getScript)
			r.Get("/jobs", s.listJobs)
			r.Get("/jobs/{id}", s.getJob)
			r.Get("/checks", s.listChecks)
			r.Get("/checks/{id}/results", s.listCheckResults)
			r.Get("/tasks", s.listTasks)
			r.Get("/policies", s.listPolicies)
			r.Get("/policies/{id}/assignments", s.listPolicyAssignments)
			r.Get("/alerts", s.listAlerts)
			r.Get("/audit", s.listAudit)
			r.Get("/chat/sessions", s.listChatSessions)
			r.Get("/chat/sessions/{id}/messages", s.listChatMessages)
		})

		// ---- tech (run things) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(models.RoleTech))
			r.Post("/devices/{id}/remote", s.startRemote)
			r.Post("/devices/{id}/run-script", s.runLibraryScript)
			r.Post("/jobs", s.createJob)
			r.Post("/jobs/{id}/approve", s.approveJob)
			r.Post("/jobs/{id}/reject", s.rejectJob)
			r.Post("/alerts/{id}/ack", s.ackAlert)
			r.Post("/alerts/{id}/resolve", s.resolveAlert)
			r.Post("/chat/sessions", s.createChatSession)
			r.Post("/chat/sessions/{id}/messages", s.postChatMessage)
		})

		// ---- admin (manage config) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(models.RoleAdmin))
			r.Post("/enroll-tokens", s.createEnrollToken)
			r.Get("/api-tokens", s.listAPITokens)
			r.Post("/api-tokens", s.createAPIToken)
			r.Delete("/api-tokens/{id}", s.deleteAPIToken)
			r.Get("/users", s.listUsers)
			r.Post("/users", s.createUser)
			r.Delete("/users/{id}", s.deleteUser)
			r.Delete("/devices/{id}", s.deleteDevice)
			r.Patch("/devices/{id}/group", s.setDeviceGroup)
			r.Post("/groups", s.createGroup)
			r.Delete("/groups/{id}", s.deleteGroup)
			r.Post("/clients", s.createClient)
			r.Delete("/clients/{id}", s.deleteClient)
			r.Post("/sites", s.createSite)
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
		var actor, role string
		switch {
		case auth.ConstantTimeEqual(token, s.cfg.AdminToken):
			actor, role = "admin", models.RoleAdmin
		default:
			hash := auth.Hash(token)
			if uid, rl, err := s.store.SessionPrincipal(r.Context(), hash); err == nil {
				actor, role = uid, rl
			} else if rl, ok, _ := s.store.APITokenRole(r.Context(), hash); ok {
				actor, role = "api", rl
			} else {
				writeErr(w, http.StatusUnauthorized, "invalid token")
				return
			}
		}
		ctx := context.WithValue(r.Context(), actorKey, actor)
		ctx = context.WithValue(ctx, roleKey, role)
		next.ServeHTTP(w, r.WithContext(ctx))
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
func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ai": map[string]any{
			"base_url":   s.cfg.AIBaseURL,
			"model":      s.cfg.AIModel,
			"configured": s.cfg.AIAPIKey != "",
		},
		"remote": map[string]any{
			"providers": []string{"native", "meshcentral", "rustdesk"},
			"default":   s.cfg.DefaultRemoteProvider,
		},
		"server": map[string]any{"version": "0.2.0", "base_url": s.cfg.PublicURL},
	})
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
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
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": agents})
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) getInventory(w http.ResponseWriter, r *http.Request) {
	inv, err := s.store.GetInventory(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) startRemote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
	}
	_ = decode(r, &in)
	if in.Provider == "" {
		in.Provider = s.cfg.DefaultRemoteProvider
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

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) approveJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.jobs.Approve(r.Context(), chi.URLParam(r, "id"), actorOf(r))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) rejectJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.jobs.Reject(r.Context(), chi.URLParam(r, "id"), actorOf(r))
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
	sessions, err := s.store.ListChatSessions(r.Context())
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
	sess, err := s.store.CreateChatSession(r.Context(), in.Title)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) listChatMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.store.ListChatMessages(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) postChatMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	ok, err := s.store.ChatSessionExists(r.Context(), sessionID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "chat session not found")
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := decode(r, &in); err != nil || in.Content == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	reply, err := s.chat.Respond(r.Context(), sessionID, in.Content, actorOf(r))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reply)
}
