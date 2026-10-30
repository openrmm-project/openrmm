// Package api wires the HTTP/REST surface, the agent WebSocket, and the remote
// relay into a single router.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
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
	"github.com/p1ngu/rmm/internal/oidc"
	"github.com/p1ngu/rmm/internal/patches"
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

var errTokenMCPOnly = errors.New("OAuth token is restricted to MCP")

type ctxKey string

const (
	actorKey          ctxKey = "actor"
	roleKey           ctxKey = "role"
	scopeUserKey      ctxKey = "scopeUser"
	permsKey          ctxKey = "permissions"
	relayPrincipalKey ctxKey = "relayPrincipal"
)

// Server holds all HTTP dependencies.
type Server struct {
	cfg    *config.Config
	store  *store.Store
	hub    *hub.Hub
	jobs   *jobs.Service
	remote *remote.Manager
	relay  *remote.Relay
	// presence is who is attached to which endpoint right now, so a second
	// technician is told rather than surprised. See internal/remote/presence.go.
	presence *remote.Presence
	// guests are the live sessions of guests a technician let in, so they can
	// be ended the moment their invitation or their sponsor goes away.
	guests  *guestSessions
	chat    *ai.Engine
	sched   *scheduler.Scheduler
	patches *patches.Service

	// oidc is the discovered identity provider, or nil when single sign-on is
	// off or discovery failed. Guarded because InitOIDC runs after New and a
	// request can arrive in between.
	oidc   *oidc.Provider
	oidcMu sync.RWMutex

	// deviceLimit is the plan's device limit as the portal last stated it
	// (RunDeviceLimit); the zero value is no limit.
	deviceLimit deviceLimitCache

	// supportHTTP reaches the portal for the self-hosted support link; nil is a
	// plain client (tests point it at a TLS test server).
	supportHTTP *http.Client

	// agentLifecycle stores one stable lock per agent. It linearizes that agent's
	// authentication, lease attachment, frames, revocation, and credential
	// transitions without allowing slow DB I/O from one endpoint to stall others.
	agentLifecycle sync.Map // agent id -> *sync.Mutex
}

// New builds the API server.
func New(cfg *config.Config, s *store.Store, h *hub.Hub, j *jobs.Service, rm *remote.Manager, relay *remote.Relay, chat *ai.Engine, sched *scheduler.Scheduler, patchSvc *patches.Service) *Server {
	srv := &Server{cfg: cfg, store: s, hub: h, jobs: j, remote: rm, relay: relay, presence: remote.NewPresence(), guests: newGuestSessions(), chat: chat, sched: sched, patches: patchSvc}
	relay.SetChatHook(srv.queueChat)
	return srv
}

func (s *Server) lockAgentLifecycle(agentID string) func() {
	value, _ := s.agentLifecycle.LoadOrStore(agentID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(s.cors)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Get("/readyz", s.readyz)
	r.Get("/metrics", s.metrics)

	// MCP OAuth 2.1 discovery metadata (unauthenticated, RFC 8414 / RFC 9728).
	// Gated by RMM_MCP_OAUTH_ENABLED so the AS is opt-in. See docs/17-mcp-oauth.md.
	if s.cfg.MCPOAuthEnabled {
		r.Get("/.well-known/oauth-authorization-server", s.oauthAuthServerMeta)
		r.Get("/.well-known/oauth-protected-resource", s.oauthProtectedResourceMeta)
	}

	// WebSocket endpoints authenticate via their own protocol / tokens.
	r.Get("/ws/agent", s.handleAgentWS)
	r.Get("/ws/remote", s.handleRelayWS)

	// Unauthenticated auth endpoints — strictly rate-limited (anti brute-force).
	authLimiter := s.rateLimit(authRateLimit)
	r.With(authLimiter, maxBody).Post("/api/auth/method", s.authMethod)
	r.With(authLimiter, maxBody).Post("/api/auth/login", s.login)
	r.With(authLimiter, maxBody).Post("/api/auth/bootstrap", s.bootstrapAdmin) // first admin: admin token + no users yet
	// Unauthenticated by nature — someone who cannot sign in is the only caller.
	// Rate limited like login: this endpoint sends mail on demand, so it is both
	// an enumeration target and a way to use the instance as a mail relay.
	r.With(authLimiter, maxBody).Post("/api/auth/password-reset", s.requestPasswordReset)
	r.With(authLimiter, maxBody).Post("/api/auth/password-reset/confirm", s.confirmPasswordReset)
	r.With(authLimiter, maxBody).Post("/api/auth/verify-email", s.confirmEmailVerification)
	// Single sign-on. Unauthenticated by definition — nobody has a session yet.
	// The status endpoint is what the login screen reads to decide whether to
	// draw the button at all.
	r.With(authLimiter).Get("/api/auth/oidc", s.oidcStatus)
	r.With(authLimiter).Get("/api/auth/oidc/start", s.oidcStart)
	r.With(authLimiter).Get("/api/auth/oidc/begin", s.oidcBegin)
	r.With(authLimiter).Get("/api/auth/oidc/callback", s.oidcCallback)
	r.With(authLimiter, maxBody).Post("/api/auth/oidc/exchange", s.oidcExchange)

	// Guests: someone a technician invited to a device's screen, with no
	// account (api_remote_shares.go). The link and the code are checked under
	// the login limiter; the page's polling and reconnects have their own.
	guestLimiter := s.rateLimit(guestPollRateLimit)
	r.With(authLimiter, maxBody).Post("/api/guest/lookup", s.guestLookup)
	r.With(authLimiter, maxBody).Post("/api/guest/join", s.guestJoin)
	r.With(guestLimiter).Get("/api/guest/request", s.guestRequest)
	r.With(guestLimiter, maxBody).Post("/api/guest/session", s.guestSession)

	// MCP OAuth 2.1 flow (register / authorize+consent / token). Public clients +
	// PKCE; the authorize POST verifies user credentials, so it's rate-limited like
	// login. Gated by RMM_MCP_OAUTH_ENABLED. See docs/17-mcp-oauth.md.
	if s.cfg.MCPOAuthEnabled {
		r.With(authLimiter, maxBody).Post("/oauth/register", s.oauthRegister)
		r.With(authLimiter).Get("/oauth/authorize", s.oauthAuthorize)
		r.With(authLimiter).Get("/oauth/authorize/sso", s.oauthAuthorizeSSO)
		r.With(authLimiter, maxBody).Post("/oauth/authorize", s.oauthAuthorizeSubmit)
		r.With(authLimiter, maxBody).Post("/oauth/token", s.oauthToken)
	}

	// MCP server (Streamable HTTP) — external AI clients (Claude, ChatGPT, …)
	// connect here. Same bearer auth as the REST API; requires the MCP permission
	// since it can propose jobs (which still require human approval).
	mcpHandler := mcpserver.Handler(s.store, s.jobs, s.patches, s.cfg, func(r *http.Request) mcpserver.Identity {
		principal := ""
		if token := bearer(r); token != "" && roleOf(r) != "" {
			principal = "token:" + auth.Hash(token)
		}
		return mcpserver.Identity{
			Actor: actorOf(r), ScopeUser: s.scopeUserID(r),
			Principal: principal, Permissions: permissionsOf(r),
		}
	}, func(authorization string) mcpserver.AuthorizeFunc {
		token := strings.TrimPrefix(authorization, "Bearer ")
		return func(ctx context.Context) (mcpserver.Identity, error) {
			identity, err := s.authenticateToken(ctx, "/mcp", token)
			if err != nil {
				return mcpserver.Identity{}, err
			}
			perms, err := s.permissionsForRole(ctx, identity.role)
			if err != nil {
				return mcpserver.Identity{}, err
			}
			set := permissionSet(perms)
			if !set[models.PermMCPUse] {
				return mcpserver.Identity{}, errors.New("MCP permission revoked")
			}
			return mcpserver.Identity{Actor: identity.actor, ScopeUser: identity.scopeUser, Principal: "token:" + auth.Hash(token), Permissions: set}, nil
		}
	})
	r.Group(func(r chi.Router) {
		r.Use(s.rateLimit(mcpRateLimit))
		r.Use(maxBody)
		r.Use(s.mcpIPAllowlist)
		r.Use(s.authMiddleware)
		r.Use(s.requirePermission(models.PermMCPUse))
		r.Handle("/mcp", mcpHandler)
		r.Handle("/mcp/*", mcpHandler)
	})

	r.Route("/api", func(r chi.Router) {
		r.Use(s.rateLimit(s.apiRateLimit())) // per-IP safety net (anti token-guessing)
		r.Use(maxBody)                       // cap request body size (anti-DoS)
		r.Use(s.authMiddleware)

		// ---- account self-service (all authenticated principals) ----
		r.Post("/auth/logout", s.logout)
		r.Get("/me", s.me)
		r.Post("/me/password", s.changePassword)
		r.Post("/me/2fa/setup", s.setup2FA)
		r.Post("/me/2fa/enable", s.enable2FA)
		r.Post("/me/2fa/disable", s.disable2FA)
		r.Post("/me/2fa/recovery-codes", s.regenRecoveryCodes)
		// Notification preferences are self-service: the address is where the
		// caller's own alerts go, and it grants no visibility their scope lacks.
		r.Get("/me/notifications", s.getMyNotifications)
		r.Put("/me/notifications", s.updateMyNotifications)
		r.Post("/me/email/verify", s.requestMyEmailVerification)
		r.Get("/config", s.getConfig)
		// The dashboard's counts. Open to every principal; each part is
		// filled only with the permission that guards its full list.
		r.Get("/dashboard", s.getDashboard)

		r.With(s.requirePermission(models.PermClientsView)).Get("/clients", s.listClients)
		r.With(s.requirePermission(models.PermClientsView)).Get("/sites", s.listSites)
		r.With(s.requirePermission(models.PermClientsView)).Get("/groups", s.listGroups)
		r.With(s.requirePermission(models.PermClientsManage)).Post("/groups", s.createGroup)
		r.With(s.requirePermission(models.PermClientsManage)).Patch("/groups/{id}", s.renameGroup)
		r.With(s.requirePermission(models.PermClientsManage)).Delete("/groups/{id}", s.deleteGroup)
		r.With(s.requirePermission(models.PermClientsManage)).Post("/clients", s.createClient)
		r.With(s.requirePermission(models.PermClientsManage)).Patch("/clients/{id}", s.updateClient)
		r.With(s.requirePermission(models.PermClientsManage)).Delete("/clients/{id}", s.deleteClient)
		r.With(s.requirePermission(models.PermClientsManage)).Post("/sites", s.createSite)
		r.With(s.requirePermission(models.PermClientsManage)).Patch("/sites/{id}", s.updateSite)
		r.With(s.requirePermission(models.PermClientsManage)).Delete("/sites/{id}", s.deleteSite)

		r.With(s.requirePermission(models.PermDevicesView)).Get("/devices", s.listDevices)
		r.With(s.requirePermission(models.PermDevicesView)).Get("/devices/limit", s.deviceLimitInfo)
		r.With(s.requirePermission(models.PermSettingsManage)).Get("/support", s.supportInfo)
		r.With(s.requirePermission(models.PermSettingsManage)).Put("/support", s.setSupportKey)
		r.With(s.requirePermission(models.PermSettingsManage)).Delete("/support", s.removeSupportKey)
		r.With(s.requirePermission(models.PermDevicesView)).Get("/devices/{id}", s.getDevice)
		r.With(s.requirePermission(models.PermDevicesView)).Get("/devices/{id}/inventory", s.getInventory)
		r.With(s.requirePermission(models.PermDevicesView)).Get("/devices/{id}/remote-capabilities", s.getRemoteCapabilities)
		// What runs on this device: direct and policy-inherited (api_device_monitoring.go).
		r.With(s.requirePermission(models.PermChecksView)).Get("/devices/{id}/checks", s.listDeviceChecks)
		r.With(s.requirePermission(models.PermTasksView)).Get("/devices/{id}/tasks", s.listDeviceTasks)
		// Who is attached right now. Readable by anyone who can see devices:
		// knowing that a colleague is already on a machine is what stops two
		// people from working on it blind, and it reveals nothing about the
		// session's contents.
		r.With(s.requirePermission(models.PermDevicesView)).Get("/remote/presence", s.listRemotePresence)
		r.With(s.requirePermissionAny(models.PermDevicesRemote, models.PermDevicesFiles, models.PermDevicesTerminal)).Post("/devices/{id}/remote", s.startRemote)
		// Guest invitations (api_remote_shares.go). Sharing takes remote access
		// and its own permission; the technician's session decides who gets in.
		shareRemote := []func(http.Handler) http.Handler{s.requirePermission(models.PermDevicesRemote), s.requirePermission(models.PermDevicesRemoteShare)}
		r.With(shareRemote...).Post("/devices/{id}/remote-shares", s.createRemoteShare)
		r.With(shareRemote...).Get("/devices/{id}/remote-shares", s.listRemoteShares)
		r.With(shareRemote...).Get("/remote-shares/pending", s.pendingRemoteShares)
		r.With(shareRemote...).Post("/remote-shares/{id}/accept", s.decideRemoteShare(true))
		r.With(shareRemote...).Post("/remote-shares/{id}/reject", s.decideRemoteShare(false))
		r.With(s.requirePermissionAny(models.PermDevicesRemoteShare, models.PermUsersManage)).Delete("/remote-shares/{id}", s.closeRemoteShare)
		r.With(s.requirePermission(models.PermDevicesManage)).Patch("/devices/{id}", s.updateDeviceMeta)
		r.With(s.requirePermission(models.PermDevicesManage)).Patch("/devices/{id}/remote-ids", s.setDeviceRemoteIDs)
		r.With(s.requirePermission(models.PermDevicesDelete)).Post("/devices/{id}/revoke", s.revokeDevice)
		r.With(s.requirePermission(models.PermDevicesDelete)).Post("/devices/{id}/rotate-credential", s.rotateDeviceCredential)
		r.With(s.requirePermission(models.PermDevicesDelete)).Post("/devices/{id}/cancel-credential-rotation", s.cancelDeviceCredentialRotation)
		r.With(s.requirePermission(models.PermDevicesDelete)).Delete("/devices/{id}", s.deleteDevice)
		r.With(s.requirePermission(models.PermClientsManage)).Patch("/devices/{id}/group", s.setDeviceGroup)
		r.With(s.requirePermission(models.PermClientsManage)).Patch("/devices/{id}/site", s.setDeviceSite)
		r.With(s.requirePermission(models.PermClientsManage)).Post("/devices/bulk/move", s.bulkMoveDevices)
		r.With(s.requirePermission(models.PermDevicesDelete)).Post("/devices/bulk/delete", s.bulkDeleteDevices)
		// Proposals from the AI and MCP clients. Listing needs only visibility;
		// deciding needs the permission the operation itself would, checked
		// per proposal in the handler.
		r.With(s.requirePermission(models.PermDevicesView)).Get("/device-operations", s.listDeviceOperations)
		r.With(s.requirePermissionAny(models.PermClientsManage, models.PermDevicesDelete)).Post("/device-operations/{id}/approve", s.approveDeviceOperation)
		r.With(s.requirePermissionAny(models.PermClientsManage, models.PermDevicesDelete)).Post("/device-operations/{id}/reject", s.rejectDeviceOperation)

		r.With(s.requirePermission(models.PermJobsView)).Get("/jobs", s.listJobs)
		r.With(s.requirePermission(models.PermJobsView)).Get("/jobs/{id}", s.getJob)
		r.With(s.requirePermission(models.PermJobsView)).Get("/jobs/batch/{batch_id}", s.getBatch)
		r.With(s.requirePermission(models.PermJobsCreate)).Post("/jobs", s.createJob)
		r.With(s.requirePermission(models.PermJobsCreate)).Post("/jobs/bulk", s.createBulkJob)
		r.With(s.requirePermission(models.PermJobsManage)).Post("/jobs/batch/{batch_id}/approve", s.approveBatch)
		r.With(s.requirePermission(models.PermJobsManage)).Post("/jobs/batch/{batch_id}/reject", s.rejectBatch)
		r.With(s.requirePermission(models.PermJobsManage)).Post("/jobs/{id}/approve", s.approveJob)
		r.With(s.requirePermission(models.PermJobsManage)).Post("/jobs/{id}/reject", s.rejectJob)
		r.With(s.requirePermission(models.PermJobsManage)).Post("/jobs/{id}/cancel", s.cancelJob)
		r.With(s.requirePermission(models.PermJobsManage)).Post("/jobs/{id}/complete", s.completeJob)

		r.With(s.requirePermission(models.PermAlertsView)).Get("/alerts", s.listAlerts)
		r.With(s.requirePermission(models.PermAlertsView)).Get("/alerts/{id}", s.getAlert)
		r.With(s.requirePermission(models.PermAlertsView)).Get("/alerts/{id}/events", s.listAlertEvents)
		r.With(s.requirePermission(models.PermAlertsView)).Get("/alert-suppressions", s.listAlertSuppressions)
		r.With(s.requirePermission(models.PermAlertsManage)).Post("/alerts/bulk", s.bulkAlertAction)
		r.With(s.requirePermission(models.PermAlertsManage)).Post("/alerts/{id}/ack", s.ackAlert)
		r.With(s.requirePermission(models.PermAlertsManage)).Post("/alerts/{id}/resolve", s.resolveAlert)
		r.With(s.requirePermission(models.PermAlertsManage)).Post("/alerts/{id}/suppress", s.suppressAlert)
		r.With(s.requirePermission(models.PermAlertsManage)).Post("/alert-suppressions", s.createAlertSuppression)
		r.With(s.requirePermission(models.PermAlertsManage)).Delete("/alert-suppressions/{id}", s.cancelAlertSuppression)

		r.With(s.requirePermission(models.PermChecksView)).Get("/checks", s.listChecks)
		r.With(s.requirePermission(models.PermChecksView)).Get("/checks/{id}/results", s.listCheckResults)
		r.With(s.requirePermission(models.PermChecksView)).Get("/checks/{id}/instances", s.listCheckInstances)
		r.With(s.requirePermission(models.PermChecksManage)).Post("/checks", s.createCheck)
		r.With(s.requirePermission(models.PermChecksManage)).Put("/checks/{id}", s.updateCheck)
		r.With(s.requirePermission(models.PermChecksManage)).Delete("/checks/{id}", s.deleteCheck)

		r.With(s.requirePermission(models.PermTasksView)).Get("/tasks", s.listTasks)
		r.With(s.requirePermission(models.PermTasksView)).Get("/tasks/{id}/targets", s.taskTargets)
		r.With(s.requirePermission(models.PermTasksManage)).Post("/tasks", s.createTask)
		r.With(s.requirePermission(models.PermTasksManage)).Put("/tasks/{id}", s.updateTask)
		r.With(s.requirePermission(models.PermTasksManage)).Post("/tasks/{id}/run", s.runTask)
		r.With(s.requirePermission(models.PermTasksManage)).Delete("/tasks/{id}", s.deleteTask)

		r.With(s.requirePermission(models.PermPoliciesView)).Get("/policies", s.listPolicies)
		r.With(s.requirePermission(models.PermPoliciesView)).Get("/policies/{id}/assignments", s.listPolicyAssignments)
		r.With(s.requirePermission(models.PermPoliciesManage)).Post("/policies", s.createPolicy)
		r.With(s.requirePermission(models.PermPoliciesManage)).Put("/policies/{id}", s.updatePolicy)
		r.With(s.requirePermission(models.PermPoliciesManage)).Delete("/policies/{id}", s.deletePolicy)
		r.With(s.requirePermission(models.PermPoliciesManage)).Post("/policies/{id}/assignments", s.createPolicyAssignment)
		r.With(s.requirePermission(models.PermPoliciesManage)).Delete("/policies/assignments/{aid}", s.deletePolicyAssignment)

		r.With(s.requirePermission(models.PermScriptsView)).Get("/scripts", s.listScripts)
		r.With(s.requirePermission(models.PermScriptsView)).Get("/scripts/{id}", s.getScript)
		r.With(s.requirePermission(models.PermJobsCreate)).Post("/devices/{id}/run-script", s.runLibraryScript)
		r.With(s.requirePermission(models.PermScriptsManage)).Post("/scripts", s.createScript)
		r.With(s.requirePermission(models.PermScriptsManage)).Put("/scripts/{id}", s.updateScript)
		r.With(s.requirePermission(models.PermScriptsManage)).Delete("/scripts/{id}", s.deleteScript)
		r.With(s.requirePermission(models.PermScriptsManage), s.requirePermission(models.PermAIAutonomyManage)).
			Post("/scripts/{id}/ai-release", s.setScriptAIRelease)
		// Their own prefix rather than /scripts/categories: a category name is not
		// a script id, and a sibling path would sit next to /scripts/{id} where
		// only chi's static-before-param rule keeps them apart.
		r.With(s.requirePermission(models.PermScriptsView)).Get("/script-categories", s.listScriptCategories)
		r.With(s.requirePermission(models.PermScriptsManage)).Post("/script-categories", s.createScriptCategory)
		r.With(s.requirePermission(models.PermScriptsManage)).Put("/script-categories/{name}", s.renameScriptCategory)
		r.With(s.requirePermission(models.PermScriptsManage)).Delete("/script-categories/{name}", s.deleteScriptCategory)

		r.With(s.requirePermission(models.PermPatchesView)).Get("/devices/{id}/patches", s.listDevicePatches)
		r.With(s.requirePermission(models.PermPatchesView)).Get("/patches", s.listPatchCatalog)
		r.With(s.requirePermission(models.PermPatchesManage)).Post("/devices/{id}/patches/scan", s.scanDevicePatches)
		r.With(s.requirePermission(models.PermPatchesManage)).Post("/devices/{id}/patches/install", s.installDevicePatches)
		r.With(s.requirePermission(models.PermPatchesManage)).Post("/devices/{id}/reboot", s.rebootDevice)
		r.With(s.requirePermission(models.PermPatchesManage)).Post("/patches/{id}/approve", s.approvePatch)
		r.With(s.requirePermission(models.PermPatchPoliciesManage)).Get("/patch-policies", s.listPatchPolicies)
		r.With(s.requirePermission(models.PermPatchPoliciesManage)).Post("/patch-policies", s.createPatchPolicy)
		r.With(s.requirePermission(models.PermPatchPoliciesManage)).Put("/patch-policies/{id}", s.updatePatchPolicy)
		r.With(s.requirePermission(models.PermPatchPoliciesManage)).Delete("/patch-policies/{id}", s.deletePatchPolicy)
		r.With(s.requirePermission(models.PermPatchPoliciesManage)).Get("/patch-policies/{id}/assignments", s.listPatchPolicyAssignments)
		r.With(s.requirePermission(models.PermPatchPoliciesManage)).Post("/patch-policies/{id}/assignments", s.addPatchPolicyAssignment)
		r.With(s.requirePermission(models.PermPatchPoliciesManage)).Delete("/patch-policies/assignments/{aid}", s.deletePatchPolicyAssignment)

		r.With(s.requirePermission(models.PermReportsView)).Get("/reports/software", s.reportSoftware)
		r.With(s.requirePermission(models.PermReportsView)).Get("/reports/hardware", s.reportHardware)
		r.With(s.requirePermission(models.PermReportsView)).Get("/reports/machines", s.reportMachines)
		r.With(s.requirePermissionAny(models.PermReportsView, models.PermPatchesView)).Get("/reports/compliance", s.reportCompliance)

		r.With(s.requirePermission(models.PermAIChatUse)).Get("/chat/sessions", s.listChatSessions)
		r.With(s.requirePermission(models.PermAIChatUse)).Post("/chat/sessions", s.createChatSession)
		r.With(s.requirePermission(models.PermAIChatUse)).Get("/chat/sessions/{id}/messages", s.listChatMessages)
		r.With(s.requirePermission(models.PermAIChatUse)).Delete("/chat/sessions/{id}", s.deleteChatSession)
		r.With(s.requirePermission(models.PermAIChatUse)).Post("/chat/sessions/{id}/messages", s.postChatMessage)

		r.With(s.requirePermission(models.PermAuditView)).Get("/audit", s.listAudit)
		r.With(s.requirePermission(models.PermAuditView)).Get("/audit/facets", s.listAuditFacets)
		// Authenticated like everything else: the signed token fixes the query,
		// it does not replace the session. A leaked link is useless on its own.
		r.Get("/exports/csv", s.exportCSV)
		r.With(s.requirePermission(models.PermSettingsManage)).Put("/config", s.updateConfig)
		// Two mail transports: the account one carries password resets, the alerts
		// one carries notifications and falls back to the account transport when it
		// names no host. Each handler is bound to its profile here rather than
		// sniffing the request path, so no routing quirk can send a save to the
		// wrong transport and overwrite password-reset delivery.
		r.With(s.requirePermission(models.PermSettingsManage)).Get("/config/smtp", s.getSMTPConfig(settings.ProfileAccount))
		r.With(s.requirePermission(models.PermSettingsManage)).Post("/config/smtp/test", s.testSMTPConfig(settings.ProfileAccount))
		r.With(s.requirePermission(models.PermSettingsManage)).Get("/config/smtp/alerts", s.getSMTPConfig(settings.ProfileAlerts))
		r.With(s.requirePermission(models.PermSettingsManage)).Put("/config/smtp/alerts", s.updateSMTPConfig(settings.ProfileAlerts))
		r.With(s.requirePermission(models.PermSettingsManage)).Post("/config/smtp/alerts/test", s.testSMTPConfig(settings.ProfileAlerts))
		r.With(s.requirePermission(models.PermEnrollmentManage)).Post("/enroll-tokens", s.createEnrollToken)
		r.With(s.requirePermission(models.PermAPITokensManage)).Get("/api-tokens", s.listAPITokens)
		r.With(s.requirePermission(models.PermAPITokensManage)).Post("/api-tokens", s.createAPIToken)
		r.With(s.requirePermission(models.PermAPITokensManage)).Delete("/api-tokens/{id}", s.deleteAPIToken)
		// MCP applications (OAuth clients) and the policy that admits them.
		r.With(s.requirePermission(models.PermAPITokensManage)).Get("/oauth-apps", s.listOAuthApps)
		r.With(s.requirePermission(models.PermAPITokensManage)).Post("/oauth-apps/{id}/approve", s.approveOAuthApp)
		r.With(s.requirePermission(models.PermAPITokensManage)).Delete("/oauth-apps/{id}", s.revokeOAuthApp)
		r.With(s.requirePermission(models.PermAPITokensManage)).Put("/config/mcp-oauth", s.updateMCPOAuthPolicy)
		r.With(s.requirePermission(models.PermUsersManage)).Get("/users", s.listUsers)
		r.With(s.requirePermission(models.PermUsersManage)).Post("/users", s.createUser)
		r.With(s.requirePermission(models.PermUsersManage)).Patch("/users/{id}", s.updateUser)
		// The address takes users.email.manage on top of users.manage: a password
		// reset resolves an account through it, so it is an authentication factor
		// rather than a contact detail.
		r.With(s.requirePermission(models.PermUsersManage), s.requirePermission(models.PermUsersEmailManage)).Put("/users/{id}/email", s.setUserEmail)
		r.With(s.requirePermission(models.PermUsersManage), s.requirePermission(models.PermUsersEmailManage)).Post("/users/{id}/email/verify", s.requestUserEmailVerification)
		r.With(s.requirePermission(models.PermUsersManage)).Get("/users/{id}/scopes", s.getUserScopes)
		r.With(s.requirePermission(models.PermUsersManage)).Put("/users/{id}/scopes", s.setUserScopes)
		r.With(s.requirePermission(models.PermUsersManage)).Post("/users/{id}/2fa/reset", s.reset2FA)
		r.With(s.requirePermission(models.PermUsersManage)).Put("/users/{id}/sso", s.setUserSSO)
		// Disable is the non-destructive counterpart to delete: the account stops
		// working, the record of what it did stays.
		r.With(s.requirePermission(models.PermUsersManage)).Post("/users/{id}/disable", s.setUserDisabled(true))
		r.With(s.requirePermission(models.PermUsersManage)).Post("/users/{id}/enable", s.setUserDisabled(false))
		r.With(s.requirePermission(models.PermUsersManage)).Delete("/users/{id}", s.deleteUser)
		r.With(s.requirePermissionAny(models.PermRolesManage, models.PermUsersManage, models.PermAPITokensManage)).Get("/roles", s.listRoles)
		r.With(s.requirePermission(models.PermRolesManage)).Get("/permissions", s.listPermissions)
		r.With(s.requirePermission(models.PermRolesManage)).Post("/roles", s.createRole)
		r.With(s.requirePermission(models.PermRolesManage)).Put("/roles/{key}", s.updateRole)
		r.With(s.requirePermission(models.PermRolesManage)).Delete("/roles/{key}", s.deleteRole)
	})

	return r
}

// ---------- middleware & helpers ----------

type authenticatedIdentity struct {
	actor, role, scopeUser, relayPrincipal string
}

func (s *Server) authenticateToken(ctx context.Context, path, token string) (authenticatedIdentity, error) {
	if token == "" {
		return authenticatedIdentity{}, store.ErrNotFound
	}
	if auth.ConstantTimeEqual(token, s.cfg.AdminToken) {
		return authenticatedIdentity{actor: "admin", role: models.RoleAdmin, relayPrincipal: "admin"}, nil
	}
	hash := auth.Hash(token)
	if uid, role, err := s.store.SessionPrincipal(ctx, hash); err == nil {
		scopeUser, err := s.effectiveScopeUser(ctx, uid)
		if err != nil {
			return authenticatedIdentity{}, err
		}
		return authenticatedIdentity{actor: uid, role: role, scopeUser: scopeUser, relayPrincipal: uid}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return authenticatedIdentity{}, err
	}
	if role, uid, ok, err := s.store.APITokenRole(ctx, hash); err != nil {
		return authenticatedIdentity{}, err
	} else if ok {
		principal := "api:" + hash
		scopeUser := ""
		if uid != "" {
			principal = uid
			scopeUser, err = s.effectiveScopeUser(ctx, uid)
			if err != nil {
				return authenticatedIdentity{}, err
			}
		}
		return authenticatedIdentity{actor: "api", role: role, scopeUser: scopeUser, relayPrincipal: principal}, nil
	}
	if uid, role, ok, err := s.store.OAuthTokenPrincipal(ctx, hash); err != nil {
		return authenticatedIdentity{}, err
	} else if ok {
		if !strings.HasPrefix(path, "/mcp") {
			return authenticatedIdentity{}, errTokenMCPOnly
		}
		scopeUser, err := s.effectiveScopeUser(ctx, uid)
		if err != nil {
			return authenticatedIdentity{}, err
		}
		return authenticatedIdentity{actor: uid, role: role, scopeUser: scopeUser, relayPrincipal: uid}, nil
	}
	return authenticatedIdentity{}, store.ErrNotFound
}

func (s *Server) effectiveScopeUser(ctx context.Context, userID string) (string, error) {
	restricted, err := s.store.UserHasScopes(ctx, userID)
	if err != nil || !restricted {
		return "", err
	}
	return userID, nil
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		identity, err := s.authenticateToken(r.Context(), r.URL.Path, token)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusUnauthorized, "invalid token")
			} else if errors.Is(err, errTokenMCPOnly) {
				writeErr(w, http.StatusForbidden, "this token is only valid for the MCP endpoint")
			} else {
				writeErr(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		perms, err := s.permissionsForRole(r.Context(), identity.role)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), actorKey, identity.actor)
		ctx = context.WithValue(ctx, roleKey, identity.role)
		ctx = context.WithValue(ctx, scopeUserKey, identity.scopeUser)
		ctx = context.WithValue(ctx, permsKey, permissionSet(perms))
		ctx = context.WithValue(ctx, relayPrincipalKey, identity.relayPrincipal)
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

func (s *Server) permissionsForRole(ctx context.Context, role string) ([]string, error) {
	if perms := models.DefaultRolePermissions(role); perms != nil {
		return perms, nil
	}
	return s.store.RolePermissions(ctx, role)
}

func permissionSet(perms []string) map[string]bool {
	out := make(map[string]bool, len(perms))
	for _, p := range perms {
		out[p] = true
	}
	return out
}

func hasPermission(r *http.Request, permission string) bool {
	if v, ok := r.Context().Value(permsKey).(map[string]bool); ok {
		return v[permission]
	}
	return false
}

func permissionsOf(r *http.Request) map[string]bool {
	if v, ok := r.Context().Value(permsKey).(map[string]bool); ok {
		out := make(map[string]bool, len(v))
		for k, allowed := range v {
			out[k] = allowed
		}
		return out
	}
	return nil
}

func (s *Server) requirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasPermission(r, permission) {
				writeErr(w, http.StatusForbidden, "missing permission: "+permission)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) requirePermissionAny(permissions ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range permissions {
				if hasPermission(r, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeErr(w, http.StatusForbidden, "missing required permission")
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
			// Everything the AI may do without approval, off at once for every
			// role (docs/23-ai-autonomy.md).
			"autonomy_paused": eff.AIAutonomyPaused,
		},
		"remote": map[string]any{
			"providers":             []string{"native", "terminal", "meshcentral"},
			"default":               eff.RemoteDefaultProvider,
			"meshcentral_url":       eff.MeshCentralURL,
			"meshcentral_user":      eff.MeshCentralUser,
			"meshcentral_token_set": eff.MeshCentralTokenKey != "",
			"meshcentral_enabled":   eff.MeshCentralEnabled,
			"rustdesk_enabled":      eff.RustDeskEnabled,
			"guest_access":          eff.RemoteGuestAccess,
		},
		"server": map[string]any{
			"version":               serverVersion(s.cfg),
			"base_url":              s.cfg.PublicURL,
			"allow_unapproved_jobs": s.cfg.AllowUnapprovedJobs,
			"macos_adhoc_bundle":    s.cfg.MacOSAdhocBundle,
			// Public keys, not secrets: every installer that pins them was
			// served by this same origin.
			"update_public_keys": updatePublicKeys(s.cfg),
			// The manifest's second signature, for installers whose OpenSSL
			// cannot check Ed25519 (a stock macOS, OpenSSL 1.1).
			"manifest_public_keys": nonNilKeys(s.cfg.ManifestPublicKeys),
		},
	})
}

// serverVersion is the release the running binary was built from, as stamped
// at build time; "dev" for a build that was not stamped.
func serverVersion(cfg *config.Config) string {
	if cfg == nil || cfg.Version == "" {
		return "dev"
	}
	return cfg.Version
}

// updatePublicKeys is never null in JSON, so the console can tell "none
// configured" from an older server that does not send the field at all.
func updatePublicKeys(cfg *config.Config) []string {
	return nonNilKeys(cfg.UpdatePublicKeys)
}

func nonNilKeys(keys []string) []string {
	if len(keys) == 0 {
		return []string{}
	}
	return keys
}

// updateConfig persists editable runtime config. Secret inputs are write-only:
// an empty secret field preserves the existing value, and responses only expose
// boolean "*_set" flags.
func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "global configuration") {
		return
	}
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
	if before.RemoteGuestAccess && !eff.RemoteGuestAccess {
		// Turning it off withdraws every invitation and ends every guest's
		// session now, not at their expiry.
		s.closeAllGuestShares(r.Context(), actorOf(r))
	}

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
	add("remote.guest_access", before.RemoteGuestAccess != after.RemoteGuestAccess)
	add("ai.autonomy_paused", before.AIAutonomyPaused != after.AIAutonomyPaused)
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

// relayPrincipalOf is a stable identity for binding a one-time viewer ticket to
// the caller that brokered it. It differs from the generic "api" audit actor for
// unbound API tokens, which must not be interchangeable at the relay.
func relayPrincipalOf(r *http.Request) string {
	if v, ok := r.Context().Value(relayPrincipalKey).(string); ok {
		return v
	}
	return ""
}

func roleOf(r *http.Request) string {
	if v, ok := r.Context().Value(roleKey).(string); ok {
		return v
	}
	return ""
}

// scopeUserID returns the user id whose access scope restricts this request, or
// "" when the principal has no user_scopes rows. Role and scope are orthogonal:
// an admin role bound to scope rows remains restricted.
func (s *Server) scopeUserID(r *http.Request) string {
	if v, ok := r.Context().Value(scopeUserKey).(string); ok {
		return v
	}
	return ""
}

func (s *Server) requireUnrestricted(w http.ResponseWriter, r *http.Request, resource string) bool {
	if s.scopeUserID(r) == "" {
		return true
	}
	writeErr(w, http.StatusForbidden, "restricted principals cannot mutate "+resource)
	return false
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
	if s.scopeUserID(r) == "" {
		if _, err := s.store.GetAgent(r.Context(), agentID); err != nil {
			writeErr(w, httpStatusFor(err), err.Error())
			return false
		}
		return true
	}
	if s.agentAllowed(r, agentID) {
		return true
	}
	writeErr(w, http.StatusForbidden, "device is outside your access scope")
	return false
}

// scopedAgentSet returns the visible agent ids for a restricted principal.
func (s *Server) scopedAgentSet(r *http.Request) (map[string]bool, bool, error) {
	uid := s.scopeUserID(r)
	if uid == "" {
		return nil, false, nil
	}
	agents, err := s.store.ListAgentsForUser(r.Context(), uid)
	if err != nil {
		return nil, true, err
	}
	set := make(map[string]bool, len(agents))
	for _, a := range agents {
		set[a.ID] = true
	}
	return set, true, nil
}

func inScope(set map[string]bool, id *string) bool { return id != nil && set[*id] }

func validScopeType(scopeType string) bool {
	return oneOf(scopeType, models.ScopeClient, models.ScopeSite, models.ScopeGroup, models.ScopeAgent)
}

// requireScope requires control of the complete referenced scope. This is
// stricter than visibility: an agent-only grant cannot mutate its parent site.
func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) bool {
	if !validScopeType(scopeType) || scopeID == "" {
		writeErr(w, http.StatusBadRequest, "invalid scope reference")
		return false
	}
	uid := s.scopeUserID(r)
	if uid == "" {
		ok, err := s.store.ScopeExists(r.Context(), scopeType, scopeID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if !ok {
			// Naming it matters: a request carries several scopes, and the one
			// that fails is often a stale reference the caller did not knowingly
			// send back. "scope not found" alone left no way to tell which.
			writeErr(w, http.StatusNotFound, "scope not found: "+scopeType+" "+scopeID)
			return false
		}
		return true
	}
	ok, err := s.store.ScopeAllowedForUser(r.Context(), uid, scopeType, scopeID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "scope is outside your access scope")
		return false
	}
	return true
}

func (s *Server) requireVisibleScope(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) bool {
	if !validScopeType(scopeType) || scopeID == "" {
		writeErr(w, http.StatusBadRequest, "invalid scope reference")
		return false
	}
	uid := s.scopeUserID(r)
	var ok bool
	var err error
	if uid == "" {
		ok, err = s.store.ScopeExists(r.Context(), scopeType, scopeID)
	} else {
		switch scopeType {
		case models.ScopeClient:
			var rows []models.Client
			rows, err = s.store.ListClientsForUser(r.Context(), uid)
			for _, row := range rows {
				ok = ok || row.ID == scopeID
			}
		case models.ScopeSite:
			var rows []models.Site
			rows, err = s.store.ListSitesForUser(r.Context(), uid, "")
			for _, row := range rows {
				ok = ok || row.ID == scopeID
			}
		case models.ScopeGroup:
			var rows []models.DeviceGroup
			rows, err = s.store.ListGroupsForUser(r.Context(), uid)
			for _, row := range rows {
				ok = ok || row.ID == scopeID
			}
		case models.ScopeAgent:
			ok, err = s.store.AgentAllowedForUser(r.Context(), uid, scopeID)
		}
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "scope is outside your access scope")
		return false
	}
	return true
}

func (s *Server) requirePolicy(w http.ResponseWriter, r *http.Request, policyID string, manage bool) bool {
	uid := s.scopeUserID(r)
	if manage && uid != "" {
		writeErr(w, http.StatusForbidden, "restricted principals cannot mutate global policies")
		return false
	}
	if uid == "" {
		ok, err := s.store.PolicyExists(r.Context(), policyID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "policy not found")
			return false
		}
		return true
	}
	var ok bool
	var err error
	if manage {
		ok, err = s.store.PolicyManageAllowedForUser(r.Context(), uid, policyID)
	} else {
		ok, err = s.store.PolicyAllowedForUser(r.Context(), uid, policyID)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "policy is outside your access scope")
		return false
	}
	return true
}

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
	if errors.Is(err, store.ErrConflict) {
		return http.StatusConflict
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
		// Optional: bind the token to a department so the agent enrols straight
		// into it rather than landing on the site and being moved by hand.
		GroupID *string `json:"group_id"`
	}
	_ = decode(r, &in)
	if in.Uses <= 0 {
		in.Uses = 1
	}
	if uid := s.scopeUserID(r); uid != "" {
		if in.SiteID == nil || *in.SiteID == "" {
			writeErr(w, http.StatusForbidden, "scoped users must bind enrollment tokens to an allowed site")
			return
		}
		allowed, err := s.store.SiteEnrollmentAllowedForUser(r.Context(), uid, *in.SiteID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !allowed {
			writeErr(w, http.StatusForbidden, "site is outside your enrollment scope")
			return
		}
	}
	if in.GroupID != nil && *in.GroupID != "" {
		// A department is reachable only through its site, so binding to one
		// without naming the site would leave the placement ambiguous.
		if in.SiteID == nil || *in.SiteID == "" {
			writeErr(w, http.StatusBadRequest, "site_id is required when binding a token to a department")
			return
		}
		if !s.requireScope(w, r, models.ScopeGroup, *in.GroupID) {
			return
		}
		// The database refuses an inconsistent pair (migration 010); saying so
		// here turns a constraint violation into an answer the operator can act on.
		g, err := s.store.GetGroup(r.Context(), *in.GroupID)
		if err != nil {
			writeErr(w, httpStatusFor(err), err.Error())
			return
		}
		if g.SiteID == nil || *g.SiteID != *in.SiteID {
			writeErr(w, http.StatusBadRequest, "that department does not belong to the site the token is bound to")
			return
		}
	}
	var expires *time.Time
	if in.TTLHours > 0 {
		t := time.Now().Add(time.Duration(in.TTLHours) * time.Hour)
		expires = &t
	}
	tok, err := s.store.CreateEnrollToken(r.Context(), auth.NewToken(), in.Description, in.Uses, expires, in.SiteID, in.GroupID)
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
	if siteID := r.URL.Query().Get("site_id"); siteID != "" && !s.requireVisibleScope(w, r, models.ScopeSite, siteID) {
		return
	}
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
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
		filtered := agents[:0]
		for _, agent := range agents {
			if set[agent.ID] {
				filtered = append(filtered, agent)
			}
		}
		agents = filtered
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

func (s *Server) getRemoteCapabilities(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	caps, err := s.remote.Capabilities(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, caps)
}

func (s *Server) startRemote(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	var in struct {
		Provider string `json:"provider"`
		// Replace ends the operator's desktop session on this endpoint so this
		// one can take its place ("continue here").
		Replace bool `json:"replace"`
		// Resume names the session this viewer had before its connection
		// dropped. That one may be replaced without asking; any other is the
		// operator's other tab, which is not ours to take.
		Resume string `json:"resume"`
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
	// Terminal and file-transfer providers are separate device sub-permissions
	// because they are more privileged than remote desktop observation.
	if in.Provider == "terminal" && !hasPermission(r, models.PermDevicesTerminal) {
		writeErr(w, http.StatusForbidden, "missing permission: "+models.PermDevicesTerminal)
		return
	}
	if in.Provider == "files" && !hasPermission(r, models.PermDevicesFiles) {
		writeErr(w, http.StatusForbidden, "missing permission: "+models.PermDevicesFiles)
		return
	}
	if in.Provider != "terminal" && in.Provider != "files" && !hasPermission(r, models.PermDevicesRemote) {
		writeErr(w, http.StatusForbidden, "missing permission: "+models.PermDevicesRemote)
		return
	}
	agent, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if agent.Status == models.AgentRevoked {
		writeErr(w, http.StatusConflict, "device credential is revoked; enroll it again to restore access")
		return
	}
	principal := relayPrincipalOf(r)
	if principal == "" {
		writeErr(w, http.StatusUnauthorized, "remote session requires an authenticated principal")
		return
	}
	if in.Provider == "native" {
		live := s.presence.ForUser(agent.ID, principal, "native")
		for _, v := range live {
			if v.SessionID != in.Resume && !in.Replace {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "you already have a remote session open on this device",
					"code":  "remote_session_active",
					"since": v.Since,
				})
				return
			}
		}
		for _, v := range live {
			s.endReplacedSession(r.Context(), v, principal)
		}
	}
	sess, err := s.remote.Start(r.Context(), in.Provider, agent, principal, remote.StartOptions{AllowFiles: hasPermission(r, models.PermDevicesFiles)})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

// endReplacedSession closes an operator's earlier desktop session on an
// endpoint so a new one can take its place. The old tab is told it was moved,
// which stops it from reconnecting and taking the session back.
func (s *Server) endReplacedSession(ctx context.Context, v remote.Viewer, principal string) {
	s.presence.Remove(v.AgentID, v.SessionID)
	s.relay.CloseSession(v.SessionID, remote.CloseSessionReplaced, "remote_session_replaced")
	_ = s.store.SetRemoteSessionStatus(ctx, v.SessionID, "ended")
	detail, _ := json.Marshal(map[string]string{"session": v.SessionID, "agent": v.AgentID})
	_ = s.store.AddAudit(ctx, principal, "remote.replace", v.AgentID, detail)
}

// ---------- jobs ----------

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID != "" && !s.requireAgent(w, r, agentID) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	js, err := s.store.ListJobs(r.Context(), agentID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
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
	// jobs and the caller can also manage jobs. jobs.create alone may only create
	// approval-gated jobs.
	require := true
	if in.RequireApproval != nil && s.cfg.AllowUnapprovedJobs && hasPermission(r, models.PermJobsManage) {
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
			if !s.requireAgent(w, r, id) {
				return
			}
			targets[id] = true
		}
	}
	if in.GroupID != "" {
		if !s.requireScope(w, r, models.ScopeGroup, in.GroupID) {
			return
		}
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
		if !s.requireScope(w, r, models.ScopeSite, in.SiteID) {
			return
		}
		ags, err := s.store.ListAgentsBySite(r.Context(), in.SiteID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, a := range ags {
			targets[a.ID] = true
		}
	}
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
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
	set, scoped, err := s.scopedAgentSet(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
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
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
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

// completeJob force-finalizes a stuck in-flight job. Body: {"success": bool}
// (defaults to true — mark completed).
func (s *Server) completeJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	body := struct {
		Success *bool `json:"success"`
	}{}
	_ = decode(r, &body)
	success := body.Success == nil || *body.Success
	job, err := s.jobs.CompleteManual(r.Context(), chi.URLParam(r, "id"), actorOf(r), success)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// ---------- audit ----------

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "restricted principals cannot read the global audit log")
		return
	}
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))

	// Times arrive as RFC3339. A malformed one is rejected rather than silently
	// ignored: quietly widening a filter is how an investigation concludes that
	// nothing happened that week.
	parseTime := func(name string) (time.Time, bool) {
		raw := strings.TrimSpace(query.Get(name))
		if raw == "" {
			return time.Time{}, true
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	}
	since, okSince := parseTime("since")
	until, okUntil := parseTime("until")
	if !okSince || !okUntil {
		writeErr(w, http.StatusBadRequest, "since and until must be RFC3339 timestamps")
		return
	}

	filter := store.AuditFilter{
		Actor:  strings.TrimSpace(query.Get("actor")),
		Action: strings.TrimSpace(query.Get("action")),
		Target: strings.TrimSpace(query.Get("target")),
		Since:  since,
		Until:  until,
		Limit:  limit,
		Offset: offset,
	}
	// A cursor, when given, replaces the offset. Both are accepted because the
	// trail is append-only and ordered newest-first: entries arrive at the top,
	// so an offset drifts under a reader who is paging while others work. The
	// cursor anchors on the last row seen and cannot drift.
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		at, id, ok := decodeAuditCursor(raw)
		if !ok {
			writeErr(w, http.StatusBadRequest, "malformed cursor")
			return
		}
		filter.CursorTime, filter.CursorID, filter.Offset = at, id, 0
	}

	entries, total, err := s.store.ListAudit(r.Context(), filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{
		"entries": entries,
		"total":   total,
		"limit":   len(entries),
		"offset":  offset,
	}
	// Handed back only when there is more to fetch, so an empty next_cursor is
	// an unambiguous end rather than one more request that returns nothing.
	if len(entries) > 0 && len(entries) == filter.Limit {
		last := entries[len(entries)-1]
		out["next_cursor"] = encodeAuditCursor(last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

// The cursor is opaque to the caller on purpose: it is a position, not a filter,
// and anything that reads it as data will eventually try to construct one.
func encodeAuditCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeAuditCursor(raw string) (time.Time, string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", false
	}
	stamp, id, found := strings.Cut(string(decoded), "|")
	if !found || id == "" {
		return time.Time{}, "", false
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, "", false
	}
	return at, id, true
}

// listAuditFacets offers the distinct actors and action families present, so the
// console can build its filter from what happened rather than from a text box
// the operator has to guess into.
func (s *Server) listAuditFacets(w http.ResponseWriter, r *http.Request) {
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "restricted principals cannot read the global audit log")
		return
	}
	actors, actions, err := s.store.AuditFacets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actors": actors, "actions": actions})
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
	reply, err := s.chat.Respond(r.Context(), sessionID, in.Content, actorOf(r), s.scopeUserID(r), permissionsOf(r))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reply)
}
