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

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/oidc"
	"github.com/p1ngu/rmm/internal/store"
)

// Sign in through an external identity provider.
//
// Three endpoints, and the split matters:
//
//	GET /api/auth/oidc          is SSO configured, and what to call the button
//	GET /api/auth/oidc/start    begin: redirect the browser to the provider
//	GET /api/auth/oidc/callback finish: verify, resolve the account, hand back a
//	                            session
//
// The console is a single-page app that authenticates with a bearer token, not
// a cookie, so the callback cannot simply set a session and redirect. It sends
// the browser back to the console with a short-lived, single-use handoff code
// in the fragment, which the page exchanges for the real token. The fragment is
// used rather than the query string because fragments are not sent to servers
// and do not land in proxy logs or Referer headers.

const (
	// Long enough for a password, a second factor and a consent screen; short
	// enough that an abandoned sign-in is not a lasting row.
	oidcLoginTTL = 10 * time.Minute
	// The handoff code exists only for the moment between the redirect landing
	// and the page reading it.
	oidcHandoffTTL = 60 * time.Second
)

// auditDetail marshals a small map for an audit row. auditLabel next door takes
// a single pair; these entries carry a few.
func auditDetail(kv map[string]string) []byte {
	b, _ := json.Marshal(kv)
	return b
}

// oidcProvider returns the discovered provider, or nil when SSO is off.
// Discovery happens once, at startup, in InitOIDC.
func (s *Server) oidcProvider() *oidc.Provider {
	s.oidcMu.RLock()
	defer s.oidcMu.RUnlock()
	return s.oidc
}

// InitOIDC discovers the provider at startup.
//
// A failure here is reported and not fatal: an identity provider that is down
// or slow to boot must not stop the RMM from starting, because agents and the
// scheduler do not depend on it and local sign-in still works. The console asks
// /api/auth/oidc whether to show the button, so a failed discovery shows no
// button rather than a broken one.
func (s *Server) InitOIDC(ctx context.Context) {
	if !s.cfg.OIDCEnabled {
		return
	}
	ctx = oidc.WithHTTPClient(ctx, oidc.HTTPClient())
	provider, err := oidc.Discover(ctx, oidc.Config{
		Issuer:        s.cfg.OIDCIssuer,
		ClientID:      s.cfg.OIDCClientID,
		ClientSecret:  s.cfg.OIDCClientSecret,
		RedirectURL:   s.cfg.OIDCRedirectURL,
		Scopes:        s.cfg.OIDCScopes,
		UsernameClaim: s.cfg.OIDCUsernameClaim,
	})
	if err != nil {
		log.Printf("oidc: single sign-on is configured but unavailable: %v", err)
		log.Printf("oidc: local sign-in is unaffected; fix the issuer and restart to enable it")
		return
	}
	s.oidcMu.Lock()
	s.oidc = provider
	s.oidcMu.Unlock()
	log.Printf("oidc: single sign-on ready (issuer %s, auto-provision=%v, default role %q)",
		s.cfg.OIDCIssuer, s.cfg.OIDCAutoProvision, s.cfg.OIDCDefaultRole)
}

// oidcStatus tells the console whether to offer the button, and what to put on
// it. Unauthenticated by necessity — it is read by the login screen.
func (s *Server) oidcStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.oidcProvider() != nil,
		"label":   s.cfg.OIDCButtonLabel,
	})
}

// oidcStart begins a sign-in.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	provider := s.oidcProvider()
	if provider == nil {
		writeErr(w, http.StatusNotFound, "single sign-on is not enabled")
		return
	}
	// Opportunistic, on the one route where a stale row could accumulate:
	// someone who opens the login page and walks away leaves one behind. Cheap
	// (indexed) and it keeps this out of the scheduler, which would otherwise
	// need to know about a feature that is off for most installs.
	if _, err := s.store.PurgeExpiredOIDCStates(r.Context()); err != nil {
		log.Printf("oidc: could not purge expired sign-in states: %v", err)
	}

	state, err1 := oidc.NewState()
	nonce, err2 := oidc.NewState()
	verifier, err3 := oidc.NewState()
	if err1 != nil || err2 != nil || err3 != nil {
		writeErr(w, http.StatusInternalServerError, "could not start sign-in")
		return
	}
	// Where to land afterwards. Only a path is accepted: an absolute URL here
	// would make this an open redirect, and an open redirect on a login page is
	// how a convincing phishing link is built.
	redirectTo := safeRedirectPath(r.URL.Query().Get("redirect_to"))

	if err := s.store.CreateOIDCState(r.Context(), auth.Hash(state), nonce, verifier,
		redirectTo, time.Now().Add(oidcLoginTTL)); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start sign-in")
		return
	}
	http.Redirect(w, r, provider.AuthCodeURL(state, nonce, verifier), http.StatusFound)
}

// oidcCallback finishes a sign-in.
//
// Everything that can go wrong here ends the same way: the browser goes back to
// the console with a generic message. The specific reason is logged and
// audited, never shown — "no account for bob@example.com" on a login page is an
// account enumerator.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	provider := s.oidcProvider()
	if provider == nil {
		writeErr(w, http.StatusNotFound, "single sign-on is not enabled")
		return
	}
	q := r.URL.Query()
	// A provider that refuses says so here. Its text is not echoed back to the
	// browser; it goes to the log where an operator can read it.
	if e := q.Get("error"); e != "" {
		log.Printf("oidc: provider refused the sign-in: %s (%s)", e, q.Get("error_description"))
		s.oidcFail(w, r, "", "sso_denied")
		return
	}
	state, code := q.Get("state"), q.Get("code")
	if state == "" || code == "" {
		s.oidcFail(w, r, "", "sso_failed")
		return
	}

	// Consuming the state deletes it, so a replayed callback finds nothing.
	login, err := s.store.ConsumeOIDCState(r.Context(), auth.Hash(state))
	if err != nil {
		log.Printf("oidc: callback with an unknown or expired state")
		s.oidcFail(w, r, "", "sso_expired")
		return
	}

	ctx := oidc.WithHTTPClient(r.Context(), oidc.HTTPClient())
	identity, err := provider.Exchange(ctx, code, login.CodeVerifier, login.Nonce)
	if err != nil {
		log.Printf("oidc: %v", err)
		s.oidcFail(w, r, login.RedirectTo, "sso_failed")
		return
	}

	user, err := s.resolveOIDCUser(r.Context(), identity)
	if err != nil {
		log.Printf("oidc: %s (%s) could not sign in: %v", identity.Username, identity.Subject, err)
		_ = s.store.AddAudit(r.Context(), identity.Username, "user.sso.denied", identity.Subject,
			auditDetail(map[string]string{"issuer": identity.Issuer, "reason": err.Error()}))
		s.oidcFail(w, r, login.RedirectTo, "sso_no_account")
		return
	}

	token := auth.NewToken()
	if err := s.store.CreateSession(r.Context(), user.ID, auth.Hash(token), time.Now().Add(sessionTTL)); err != nil {
		s.oidcFail(w, r, login.RedirectTo, "sso_failed")
		return
	}
	_ = s.store.AddAudit(r.Context(), user.Username, "user.sso.login", user.ID,
		auditDetail(map[string]string{"issuer": identity.Issuer}))

	// The token goes back in the fragment: fragments are never sent to a server,
	// so it stays out of proxy logs, access logs and Referer headers on the way
	// to the console.
	target := s.consoleURL(login.RedirectTo)
	http.Redirect(w, r, target+"#sso_token="+url.QueryEscape(token), http.StatusFound)
}

// resolveOIDCUser turns a verified identity into a console account.
//
// Three cases, in order:
//
//  1. the identity is already linked — sign in
//  2. a local account has the same username — link it, once, on first use. This
//     is the migration path: an operator creates people the way they always
//     have and they start signing in through the provider.
//  3. nobody — create the account only if the operator asked for that.
//
// Case 3 is off by default and the reason is worth stating: with it on, every
// account the provider will authenticate has an account here, which for a
// Google Workspace or Entra tenant is the entire company.
func (s *Server) resolveOIDCUser(ctx context.Context, id *oidc.Identity) (*models.User, error) {
	user, err := s.store.GetUserByOIDC(ctx, id.Issuer, id.Subject)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// A local account with this username: adopt it rather than creating a
	// duplicate. It keeps the role and scope an operator already set.
	if existingID, role, _, lerr := s.store.GetUserAuth(ctx, id.Username); lerr == nil {
		if err := s.store.LinkOIDCIdentity(ctx, existingID, id.Issuer, id.Subject); err != nil {
			// Already linked to a different identity. Refusing is the point: this
			// is someone arriving with a matching username at a provider,
			// claiming an account that belongs to another identity.
			return nil, errors.New("that username is already linked to a different identity")
		}
		_ = s.store.AddAudit(ctx, id.Username, "user.sso.linked", existingID,
			auditDetail(map[string]string{"issuer": id.Issuer, "subject": id.Subject}))
		return &models.User{ID: existingID, Username: id.Username, Role: role}, nil
	}

	if !s.cfg.OIDCAutoProvision {
		return nil, errors.New("no account, and automatic provisioning is off")
	}
	created, err := s.store.CreateOIDCUser(ctx, id.Username, s.cfg.OIDCDefaultRole, id.Issuer, id.Subject)
	if err != nil {
		return nil, err
	}
	_ = s.store.AddAudit(ctx, id.Username, "user.sso.provisioned", created.ID,
		auditDetail(map[string]string{"issuer": id.Issuer, "role": s.cfg.OIDCDefaultRole}))
	return created, nil
}

// oidcFail sends the browser back to the console with a code the login screen
// turns into a message in the reader's own language.
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, redirectTo, reason string) {
	http.Redirect(w, r, s.consoleURL(redirectTo)+"#sso_error="+url.QueryEscape(reason), http.StatusFound)
}

// consoleURL is where the browser is sent back to. Only a path from the
// original request is honoured, so this can never point off-site.
func (s *Server) consoleURL(path string) string {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		base = "/"
	}
	if path == "" {
		return base + "/"
	}
	return base + path
}

// safeRedirectPath keeps a caller-supplied destination on this site.
//
// Anything absolute, protocol-relative ("//evil.example") or not starting with
// a single slash is discarded rather than corrected. An open redirect on the
// login route is the classic ingredient of a phishing link that starts on the
// real domain.
func safeRedirectPath(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	if strings.Contains(raw, "\\") || strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return ""
	}
	return u.RequestURI()
}
