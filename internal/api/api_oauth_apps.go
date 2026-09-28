package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// MCP applications, as an administrator sees and controls them.
//
// Applications register themselves (Dynamic Client Registration). Here an
// administrator lists them, approves pending ones, revokes any — which kills
// its tokens at once — and sets the policy: open or only listed hosts, and
// whether new applications wait for approval. All of it is instance-wide, so
// changing it needs an unscoped principal holding api_tokens.manage, the
// permission that already governs connector tokens.

const (
	msgAppPending    = "This application is waiting for an administrator's approval. Ask an administrator of this OpenRMM instance to approve it, then connect again."
	msgAppNotAllowed = "This application's address is not allowed on this OpenRMM instance. Ask an administrator to add its host to the allowed list."
)

// oauthAppRefusal says why an application may not authorize right now, or ""
// when it may. Checked on every step of the authorization — the policy or the
// application's status can change between them.
func (s *Server) oauthAppRefusal(ctx context.Context, p authzParams) (string, error) {
	app, err := s.store.GetOAuthClientInfo(ctx, p.ClientID)
	if err != nil {
		return "", err
	}
	if app.Status != store.OAuthClientApproved {
		return msgAppPending, nil
	}
	policy, err := settings.LoadMCPOAuthPolicy(ctx, s.store)
	if err != nil {
		return "", err
	}
	if !policy.HostAllowed(redirectHost(p.RedirectURI)) {
		return msgAppNotAllowed, nil
	}
	return "", nil
}

// refuseOAuthApp ends the request with the refusal page when the application
// may not authorize, and reports whether it did.
func (s *Server) refuseOAuthApp(w http.ResponseWriter, r *http.Request, p authzParams) bool {
	msg, err := s.oauthAppRefusal(r.Context(), p)
	if err != nil {
		renderOAuthMessage(w, http.StatusInternalServerError, "Something went wrong. Try again.")
		return true
	}
	if msg != "" {
		renderOAuthMessage(w, http.StatusForbidden, msg)
		return true
	}
	return false
}

func (s *Server) listOAuthApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListOAuthClients(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	policy, err := settings.LoadMCPOAuthPolicy(r.Context(), s.store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"apps":        apps,
		"policy":      policy,
		"known_hosts": settings.KnownMCPHosts,
	})
}

func (s *Server) approveOAuthApp(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "MCP applications") {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.store.ApproveOAuthClient(r.Context(), id, actorOf(r)); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "oauth.app.approve", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// revokeOAuthApp removes an application with its tokens, codes and SSO grants.
// An application that registers again comes back under the current policy.
func (s *Server) revokeOAuthApp(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "MCP applications") {
		return
	}
	id := chi.URLParam(r, "id")
	app, err := s.store.GetOAuthClientInfo(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if err := s.store.DeleteOAuthClient(r.Context(), id); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "oauth.app.revoke", id,
		auditDetail(map[string]string{"name": app.Name, "tokens": strconv.Itoa(app.ActiveTokens)}))
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) updateMCPOAuthPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "MCP application policy") {
		return
	}
	var in settings.MCPOAuthPolicy
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	saved, err := settings.SaveMCPOAuthPolicy(r.Context(), s.store, in)
	if err != nil {
		if errors.Is(err, settings.ErrInvalidMCPHost) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "invalid_host"})
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	approval := "false"
	if saved.RequireApproval {
		approval = "true"
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "config.mcp_oauth.update", "app_settings",
		auditDetail(map[string]string{"registration": saved.Registration, "require_approval": approval}))
	writeJSON(w, http.StatusOK, map[string]any{"policy": saved})
}
