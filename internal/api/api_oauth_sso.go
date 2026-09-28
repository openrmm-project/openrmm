package api

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/oidc"
	"github.com/p1ngu/rmm/internal/store"
)

// Authorizing an MCP client through single sign-on.
//
// The consent form took only a password, and an account linked to single
// sign-on has no password path while SSO is on (see signsInWithSSO). So the
// form offers the provider too:
//
//  1. "Continue with SSO" (GET /oauth/authorize/sso) validates the request and
//     starts an ordinary provider sign-in, with the request stored beside its
//     state instead of a console destination.
//  2. The provider returns to the usual callback. Finding a request on the
//     state, the callback does not open a console session: it resolves the
//     account exactly as a console sign-in would (provider MFA, disabled
//     accounts, linking rules), then mints a grant ticket bound to that request
//     and shows the consent page again, signed in.
//  3. Authorize posts the ticket back to /oauth/authorize, which redeems it —
//     once, for that client, redirect URI and PKCE challenge only — and issues
//     the code as it would after a password.
//
// The ticket travels in the form body, never a URL, so it does not land in
// proxy or access logs. The callback page lives on the console origin and
// posts to the MCP origin; that is a top-level form submission, which needs no
// cross-origin permission.

const oauthSSOGrantTTL = 5 * time.Minute

// oauthAuthorizeSSO starts a provider sign-in for an authorization request.
func (s *Server) oauthAuthorizeSSO(w http.ResponseWriter, r *http.Request) {
	p := parseAuthzParams(r.URL.Query())
	redirectOK, errCode := s.validateAuthz(r.Context(), p)
	if errCode != "" {
		if redirectOK {
			redirectAuthzError(w, r, p.RedirectURI, p.State, errCode)
		} else {
			http.Error(w, "OAuth error: "+errCode, http.StatusBadRequest)
		}
		return
	}
	provider := s.oidcProvider()
	if provider == nil {
		http.Error(w, "single sign-on is not enabled", http.StatusNotFound)
		return
	}
	// Opportunistic cleanup, as on the console's start route.
	if _, err := s.store.PurgeExpiredOIDCStates(r.Context()); err != nil {
		log.Printf("oidc: could not purge expired sign-in states: %v", err)
	}
	if _, err := s.store.PurgeExpiredOAuthSSOGrants(r.Context()); err != nil {
		log.Printf("oauth: could not purge expired SSO grants: %v", err)
	}
	state, err1 := oidc.NewState()
	nonce, err2 := oidc.NewState()
	verifier, err3 := oidc.NewState()
	request, err4 := json.Marshal(p)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	if err := s.store.CreateOIDCStateForOAuth(r.Context(), auth.Hash(state), nonce, verifier, request,
		time.Now().Add(oidcLoginTTL)); err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, provider.AuthCodeURL(state, nonce, verifier), http.StatusFound)
}

// finishOAuthSSO completes a provider sign-in that was started from the consent
// form. Called by oidcCallback once it has consumed a state carrying a request.
//
// Failures end on a page of their own rather than the console: the person is
// in the middle of connecting an MCP client, and a console login screen would
// be a confusing place to land. As on the console path, the reason is logged
// and audited, never shown.
func (s *Server) finishOAuthSSO(w http.ResponseWriter, r *http.Request, provider *oidc.Provider, login *store.OIDCLogin, code string) {
	var p authzParams
	if err := json.Unmarshal(login.OAuthRequest, &p); err != nil {
		log.Printf("oauth sso: unreadable authorization request on a sign-in state: %v", err)
		renderOAuthMessage(w, http.StatusBadRequest, oauthSSOFailed)
		return
	}
	// The client may have been removed while the user was at the provider.
	if _, errCode := s.validateAuthz(r.Context(), p); errCode != "" {
		renderOAuthMessage(w, http.StatusBadRequest, oauthSSOFailed)
		return
	}
	ctx := oidc.WithHTTPClient(r.Context(), oidc.HTTPClient())
	identity, err := provider.Exchange(ctx, code, login.CodeVerifier, login.Nonce)
	if err != nil {
		log.Printf("oauth sso: %v", err)
		renderOAuthMessage(w, http.StatusBadRequest, oauthSSOFailed)
		return
	}
	user, err := s.resolveOIDCUser(r.Context(), identity)
	if err != nil {
		log.Printf("oauth sso: %s (%s) could not sign in: %v", identity.Username, identity.Subject, err)
		_ = s.store.AddAudit(r.Context(), identity.Username, "user.sso.denied", identity.Subject,
			auditDetail(map[string]string{"issuer": identity.Issuer, "reason": err.Error(), "for": "mcp_authorize"}))
		renderOAuthMessage(w, http.StatusForbidden, oauthSSOFailed)
		return
	}
	if ok, err := s.userMayUseMCP(r.Context(), user.ID); err != nil || !ok {
		if err != nil {
			renderOAuthMessage(w, http.StatusInternalServerError, oauthSSOFailed)
			return
		}
		renderOAuthMessage(w, http.StatusForbidden, noMCPPermission)
		return
	}
	ticket := auth.NewToken()
	if err := s.store.CreateOAuthSSOGrant(r.Context(), auth.Hash(ticket), user.ID, p.ClientID, p.RedirectURI,
		p.CodeChallenge, time.Now().Add(oauthSSOGrantTTL)); err != nil {
		renderOAuthMessage(w, http.StatusInternalServerError, oauthSSOFailed)
		return
	}
	_ = s.store.AddAudit(r.Context(), user.Username, "user.sso.login", user.ID,
		auditDetail(map[string]string{"issuer": identity.Issuer, "for": "mcp_authorize", "client": p.ClientID}))

	name, _, _, _ := s.store.GetOAuthClient(r.Context(), p.ClientID)
	renderConsentPage(w, consentView{
		P: p, ClientName: name, Action: s.oauthAuthorizeAction(),
		SSOUser: user.Username, Ticket: ticket, RedirectHost: redirectHost(p.RedirectURI),
	})
}

// redeemOAuthSSOGrant spends a grant ticket and reports whether it authorizes
// this request: minted for the same client, redirect URI and PKCE challenge,
// not expired, and its user not disabled since. The ticket is spent whatever
// the answer, so a mismatched attempt cannot be retried.
func (s *Server) redeemOAuthSSOGrant(ctx context.Context, ticket string, p authzParams) (userID string, ok bool, err error) {
	g, err := s.store.ConsumeOAuthSSOGrant(ctx, auth.Hash(ticket))
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !g.Live || !g.UserActive || g.ClientID != p.ClientID || g.RedirectURI != p.RedirectURI || g.CodeChallenge != p.CodeChallenge {
		return "", false, nil
	}
	return g.UserID, true, nil
}

// oauthAuthorizeAction is where the signed-in consent page posts. It is shown
// on the console origin (the provider's callback) and must reach the MCP one.
func (s *Server) oauthAuthorizeAction() string {
	if base := s.issuerBase(); base != "" {
		return base + "/oauth/authorize"
	}
	return "/oauth/authorize"
}

const oauthSSOFailed = "Single sign-on did not complete. Close this window and connect again from your MCP client."

const noMCPPermission = "Your role does not allow connecting MCP clients. Ask an administrator for the MCP permission."

// userMayUseMCP reports whether a user's role holds mcp.use, the permission
// /mcp checks on every call.
func (s *Server) userMayUseMCP(ctx context.Context, userID string) (bool, error) {
	u, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return false, err
	}
	perms, err := s.permissionsForRole(ctx, u.Role)
	if err != nil {
		return false, err
	}
	return permissionSet(perms)[models.PermMCPUse], nil
}

// oauthCallbackFailure ends a callback that failed before its state was
// consumed. The state is consumed here — the sign-in is over either way — so a
// consent-form sign-in gets the MCP message instead of the console's login
// screen, which would be a confusing place to land mid-connection. It reports
// whether it handled the response.
func (s *Server) oauthCallbackFailure(w http.ResponseWriter, r *http.Request, state string) bool {
	if state == "" {
		return false
	}
	login, err := s.store.ConsumeOIDCState(r.Context(), auth.Hash(state))
	if err != nil || login.OAuthRequest == nil {
		return false
	}
	renderOAuthMessage(w, http.StatusBadRequest, oauthSSOFailed)
	return true
}

var oauthMessageTmpl = template.Must(template.New("oauthmsg").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authorize · OpenRMM</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0e14;color:#e6e9ef;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
.card{background:#131722;border:1px solid #232a3a;border-radius:14px;padding:28px;width:340px;font-size:14px;line-height:1.5}</style>
</head><body><div class="card">{{.}}</div></body></html>`))

func renderOAuthMessage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = oauthMessageTmpl.Execute(w, strings.TrimSpace(msg))
}
