package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// MCP OAuth 2.1 Authorization Server — the RMM is its own AS + Resource Server so
// cloud MCP clients (Claude web, ChatGPT) can connect via the standard OAuth
// connector flow instead of a pasted static token. See docs/17-mcp-oauth.md.
//
// Tokens/codes are opaque, stored SHA-256 hashed (like api_tokens/sessions).
// PKCE (S256) is required; clients are public (no secret). Access tokens are
// bound to a user, so /mcp keeps the user's role + access scope. All gated by
// RMM_MCP_OAUTH_ENABLED.

const (
	oauthCodeTTL    = 5 * time.Minute
	oauthTokenTTL   = time.Hour           // access token
	oauthRefreshTTL = 30 * 24 * time.Hour // refresh token
	oauthScope      = "mcp"
	oauthChallenge  = "S256"
)

var (
	errBadCredentials = errors.New("invalid credentials")
	errTOTPRequired   = errors.New("two-factor code required")
	errSSORequired    = errors.New("this account signs in with single sign-on")
	errAuthStore      = errors.New("authentication store unavailable")
)

// issuerBase is the OAuth issuer — always the dedicated MCP origin, trimmed of a
// trailing slash. It must never point cloud clients at the public agents door.
func (s *Server) issuerBase() string {
	return strings.TrimRight(s.cfg.MCPPublicURL, "/")
}

// oauthAuthServerMeta serves RFC 8414 Authorization Server Metadata.
func (s *Server) oauthAuthServerMeta(w http.ResponseWriter, r *http.Request) {
	base := s.issuerBase()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{oauthChallenge}, // PKCE required
		"token_endpoint_auth_methods_supported": []string{"none"},         // public clients
		"scopes_supported":                      []string{oauthScope},
	})
}

// oauthProtectedResourceMeta serves RFC 9728 Protected Resource Metadata.
func (s *Server) oauthProtectedResourceMeta(w http.ResponseWriter, r *http.Request) {
	base := s.issuerBase()
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              base + "/mcp",
		"authorization_servers": []string{base},
	})
}

// ---------- Dynamic Client Registration (RFC 7591) ----------

func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := decode(r, &in); err != nil || len(in.RedirectURIs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_client_metadata", "error_description": "redirect_uris is required"})
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirectURI(u) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_redirect_uri"})
			return
		}
	}
	clientID := uuid.NewString()
	if err := s.store.CreateOAuthClient(r.Context(), clientID, in.ClientName, in.RedirectURIs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_name":                in.ClientName,
		"redirect_uris":              in.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

// validRedirectURI accepts https:// and http://localhost (for local dev/clients).
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return u.Host != ""
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1"
	}
	return false
}

// ---------- Authorization endpoint (login + consent) ----------

type authzParams struct {
	ResponseType, ClientID, RedirectURI, Scope, State string
	CodeChallenge, CodeChallengeMethod                string
}

func parseAuthzParams(q url.Values) authzParams {
	return authzParams{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
	}
}

// validateAuthz checks the client and returns a clear error string on failure.
// The bool reports whether the redirect_uri is trusted (so we may redirect errors
// back); when false the caller must render an error page instead.
func (s *Server) validateAuthz(ctx context.Context, p authzParams) (redirectOK bool, errCode string) {
	name, uris, ok, err := s.store.GetOAuthClient(ctx, p.ClientID)
	_ = name
	if err != nil {
		return false, "server_error"
	}
	if !ok {
		return false, "unauthorized_client"
	}
	matched := false
	for _, u := range uris {
		if u == p.RedirectURI {
			matched = true
			break
		}
	}
	if !matched {
		return false, "invalid_redirect_uri" // do NOT redirect to an unregistered URI
	}
	if p.ResponseType != "code" {
		return true, "unsupported_response_type"
	}
	if p.CodeChallenge == "" || p.CodeChallengeMethod != oauthChallenge {
		return true, "invalid_request" // PKCE S256 required
	}
	return true, ""
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
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
	name, _, _, _ := s.store.GetOAuthClient(r.Context(), p.ClientID)
	s.renderConsent(w, p, name, "")
}

func (s *Server) oauthAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	p := authzParams{
		ResponseType:        r.PostForm.Get("response_type"),
		ClientID:            r.PostForm.Get("client_id"),
		RedirectURI:         r.PostForm.Get("redirect_uri"),
		Scope:               r.PostForm.Get("scope"),
		State:               r.PostForm.Get("state"),
		CodeChallenge:       r.PostForm.Get("code_challenge"),
		CodeChallengeMethod: r.PostForm.Get("code_challenge_method"),
	}
	redirectOK, errCode := s.validateAuthz(r.Context(), p)
	if errCode != "" {
		if redirectOK {
			redirectAuthzError(w, r, p.RedirectURI, p.State, errCode)
		} else {
			http.Error(w, "OAuth error: "+errCode, http.StatusBadRequest)
		}
		return
	}
	name, _, _, _ := s.store.GetOAuthClient(r.Context(), p.ClientID)

	ssoTicket := r.PostForm.Get("sso_ticket")

	// User denied consent. A single sign-on grant is spent either way.
	if r.PostForm.Get("action") == "deny" {
		if ssoTicket != "" {
			_, _ = s.store.ConsumeOAuthSSOGrant(r.Context(), auth.Hash(ssoTicket))
		}
		redirectAuthzError(w, r, p.RedirectURI, p.State, "access_denied")
		return
	}

	// Who is authorizing: someone who just signed in through the identity
	// provider for this very request (see api_oauth_sso.go), or someone typing
	// a password here.
	var userID string
	if ssoTicket != "" {
		id, ok, err := s.redeemOAuthSSOGrant(r.Context(), ssoTicket, p)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if !ok {
			s.renderConsent(w, p, name, "Your single sign-on expired or was for another request. Sign in again.")
			return
		}
		userID = id
	} else {
		id, _, err := s.verifyUserCredentials(r.Context(),
			r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("code"))
		if err != nil {
			if errors.Is(err, errAuthStore) {
				http.Error(w, "server error", http.StatusInternalServerError)
				return
			}
			msg := "Invalid username or password."
			if errors.Is(err, errTOTPRequired) {
				msg = "Enter your two-factor code."
			}
			if errors.Is(err, errSSORequired) {
				msg = "This account signs in with single sign-on. Use the button above."
			}
			s.renderConsent(w, p, name, msg)
			return
		}
		userID = id
	}

	// A token for someone whose role cannot use MCP would be refused by /mcp on
	// its first call; say so here, where the person can still act on it.
	if ok, err := s.userMayUseMCP(r.Context(), userID); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	} else if !ok {
		s.renderConsent(w, p, name, noMCPPermission)
		return
	}

	// Issue a single-use authorization code bound to this user + PKCE challenge.
	code := auth.NewToken()
	if err := s.store.CreateOAuthCode(r.Context(), auth.Hash(code), p.ClientID, userID,
		p.RedirectURI, p.CodeChallenge, oauthScope, time.Now().Add(oauthCodeTTL)); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	_ = s.store.AddAudit(r.Context(), userID, "oauth.authorize", p.ClientID, nil)

	u, _ := url.Parse(p.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	if p.State != "" {
		q.Set("state", p.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func redirectAuthzError(w http.ResponseWriter, r *http.Request, redirectURI, state, errCode string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "OAuth error: "+errCode, http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", errCode)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// verifyUserCredentials mirrors login: bcrypt password + optional TOTP / recovery
// code. Returns the user id and role, or a sentinel error.
func (s *Server) verifyUserCredentials(ctx context.Context, username, password, code string) (userID, role string, err error) {
	id, rl, hash, e := s.store.GetUserAuth(ctx, username)
	if e != nil {
		auth.CheckPassword("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidin", password)
		if !errors.Is(e, store.ErrNotFound) {
			return "", "", errAuthStore
		}
		return "", "", errBadCredentials
	}
	if !auth.CheckPassword(hash, password) {
		return "", "", errBadCredentials
	}
	if sso, e := s.signsInWithSSO(ctx, id); e != nil {
		return "", "", errAuthStore
	} else if sso {
		return "", "", errSSORequired
	}
	encSecret, enabled, e := s.store.GetUserTOTP(ctx, id)
	if e != nil {
		return "", "", errAuthStore
	}
	if enabled {
		secret, derr := settings.Decrypt(s.cfg, encSecret)
		ok := derr == nil && auth.ValidateTOTP(secret, code)
		if !ok {
			used, uerr := s.store.UseRecoveryCode(ctx, id, auth.Hash(auth.NormalizeRecoveryCode(code)))
			if uerr != nil {
				return "", "", errAuthStore
			}
			if used {
				ok = true
				_ = s.store.AddAudit(ctx, id, "user.2fa.recovery.used", id, nil)
			}
		}
		if !ok {
			return "", "", errTOTPRequired
		}
	}
	return id, rl, nil
}

// ---------- Token endpoint ----------

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthTokenErr(w, "invalid_request")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.oauthTokenFromCode(w, r)
	case "refresh_token":
		s.oauthTokenFromRefresh(w, r)
	default:
		oauthTokenErr(w, "unsupported_grant_type")
	}
}

func (s *Server) oauthTokenFromCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	verifier := r.PostForm.Get("code_verifier")
	clientID := r.PostForm.Get("client_id")
	redirectURI := r.PostForm.Get("redirect_uri")
	if code == "" || verifier == "" {
		oauthTokenErr(w, "invalid_request")
		return
	}
	cID, userID, rURI, challenge, scope, ok, err := s.store.ConsumeOAuthCode(r.Context(), auth.Hash(code))
	if err != nil {
		oauthTokenErr(w, "server_error")
		return
	}
	if !ok || cID != clientID || rURI != redirectURI || !verifyPKCE(verifier, challenge) {
		oauthTokenErr(w, "invalid_grant")
		return
	}
	s.issueOAuthTokens(w, r, clientID, userID, scope)
}

func (s *Server) oauthTokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := r.PostForm.Get("refresh_token")
	if refresh == "" {
		oauthTokenErr(w, "invalid_request")
		return
	}
	clientID, userID, scope, ok, err := s.store.ConsumeOAuthRefresh(r.Context(), auth.Hash(refresh))
	if err != nil {
		oauthTokenErr(w, "server_error")
		return
	}
	if !ok {
		oauthTokenErr(w, "invalid_grant")
		return
	}
	s.issueOAuthTokens(w, r, clientID, userID, scope)
}

// issueOAuthTokens mints and stores a fresh access + refresh pair (rotation).
func (s *Server) issueOAuthTokens(w http.ResponseWriter, r *http.Request, clientID, userID, scope string) {
	access := auth.NewToken()
	refresh := auth.NewToken()
	rh := auth.Hash(refresh)
	now := time.Now()
	if err := s.store.CreateOAuthToken(r.Context(), auth.Hash(access), &rh, clientID, userID, scope,
		now.Add(oauthTokenTTL), now.Add(oauthRefreshTTL)); err != nil {
		oauthTokenErr(w, "server_error")
		return
	}
	_ = s.store.AddAudit(r.Context(), userID, "oauth.token", clientID, nil)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(oauthTokenTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         scope,
	})
}

func oauthTokenErr(w http.ResponseWriter, code string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

// verifyPKCE checks base64url(SHA256(verifier)) == stored S256 challenge.
func verifyPKCE(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

// ---------- Consent page ----------

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Authorize · OpenRMM</title>
<style>
 body{font-family:system-ui,sans-serif;background:#0b0e14;color:#e6e9ef;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
 .card{background:#131722;border:1px solid #232a3a;border-radius:14px;padding:28px;width:340px;box-shadow:0 10px 40px rgba(0,0,0,.4)}
 h1{font-size:16px;margin:0 0 4px}.sub{color:#94a0b4;font-size:13px;margin:0 0 18px}
 label{display:block;font-size:12px;color:#94a0b4;margin:12px 0 4px}
 input{width:100%;box-sizing:border-box;background:#0b0e14;border:1px solid #232a3a;border-radius:8px;padding:9px 11px;color:#e6e9ef;font-size:14px}
 .row{display:flex;gap:10px;margin-top:18px}
 button{flex:1;border:0;border-radius:8px;padding:10px;font-size:14px;font-weight:600;cursor:pointer}
 .primary{background:#4c7dff;color:#fff}.ghost{background:#232a3a;color:#e6e9ef}
 .err{background:rgba(248,113,113,.12);border:1px solid rgba(248,113,113,.3);color:#fca5a5;font-size:12.5px;border-radius:8px;padding:8px 10px;margin-bottom:12px}
 .sso{display:block;text-align:center;text-decoration:none;background:#232a3a;color:#e6e9ef;border-radius:8px;padding:10px;font-size:14px;font-weight:600}
 .or{text-align:center;color:#94a0b4;font-size:12px;margin:14px 0 2px}
 .who{background:#0b0e14;border:1px solid #232a3a;border-radius:8px;padding:9px 11px;font-size:13px;color:#94a0b4}
 .dest{color:#94a0b4;font-size:12px;margin:14px 0 0}
 b{color:#e6e9ef}
</style></head><body>
<form class="card" method="post" action="{{.Action}}">
 <h1>Authorize access</h1>
 <p class="sub">Allow <b>{{if .ClientName}}{{.ClientName}}{{else}}this MCP client{{end}}</b> to access OpenRMM as you. It can only propose actions — you still approve every job.</p>
 {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
 {{if .SSOUser}}
 <div class="who">Signed in as <b>{{.SSOUser}}</b> through single sign-on.</div>
 <input type="hidden" name="sso_ticket" value="{{.Ticket}}">
 {{else}}
 {{if .SSOURL}}<a class="sso" href="{{.SSOURL}}">Continue with {{.SSOLabel}}</a><div class="or">or sign in with a password</div>{{end}}
 <label>Username</label><input name="username" {{if not .SSOURL}}autofocus {{end}}autocomplete="username">
 <label>Password</label><input name="password" type="password" autocomplete="current-password">
 <label>Two-factor code (if enabled)</label><input name="code" autocomplete="one-time-code" inputmode="numeric">
 {{end}}
 <input type="hidden" name="response_type" value="{{.P.ResponseType}}">
 <input type="hidden" name="client_id" value="{{.P.ClientID}}">
 <input type="hidden" name="redirect_uri" value="{{.P.RedirectURI}}">
 <input type="hidden" name="scope" value="{{.P.Scope}}">
 <input type="hidden" name="state" value="{{.P.State}}">
 <input type="hidden" name="code_challenge" value="{{.P.CodeChallenge}}">
 <input type="hidden" name="code_challenge_method" value="{{.P.CodeChallengeMethod}}">
 {{if .RedirectHost}}<p class="dest">Authorizing sends you back to <b>{{.RedirectHost}}</b>.</p>{{end}}
 <div class="row">
  <button class="ghost" type="submit" name="action" value="deny">Deny</button>
  <button class="primary" type="submit" name="action" value="allow">Authorize</button>
 </div>
</form></body></html>`))

// consentView is everything the consent page shows.
type consentView struct {
	P                 authzParams
	ClientName, Error string
	Action            string // where the form posts
	SSOURL, SSOLabel  string // the single sign-on button, when SSO is available
	SSOUser, Ticket   string // set once the user has signed in through SSO
	RedirectHost      string
}

// renderConsent shows the password form, with the single sign-on button when a
// provider is available.
func (s *Server) renderConsent(w http.ResponseWriter, p authzParams, clientName, errMsg string) {
	v := consentView{P: p, ClientName: clientName, Error: errMsg, Action: "/oauth/authorize", RedirectHost: redirectHost(p.RedirectURI)}
	if s.oidcProvider() != nil {
		v.SSOURL = "/oauth/authorize/sso?" + authzQuery(p).Encode()
		v.SSOLabel = s.cfg.OIDCButtonLabel
	}
	renderConsentPage(w, v)
}

// renderConsentPage writes the page. It must never be framed: a consent button
// under someone else's page is how a click is stolen (clickjacking).
func renderConsentPage(w http.ResponseWriter, v consentView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_ = consentTmpl.Execute(w, v)
}

// redirectHost is the host the client will be sent back to, shown on the page
// so a user can notice a client that is not what it claims.
func redirectHost(redirectURI string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return ""
	}
	return u.Host
}

// authzQuery is an authorization request as query parameters again.
func authzQuery(p authzParams) url.Values {
	q := url.Values{}
	for k, v := range map[string]string{
		"response_type": p.ResponseType, "client_id": p.ClientID, "redirect_uri": p.RedirectURI,
		"scope": p.Scope, "state": p.State, "code_challenge": p.CodeChallenge,
		"code_challenge_method": p.CodeChallengeMethod,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return q
}
