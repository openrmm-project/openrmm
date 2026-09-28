package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/oidc/oidctest"
)

// Single sign-on end to end, against a provider that signs real tokens: the
// console sign-in, and authorizing an MCP client from the consent page. Every
// hop is followed by hand, so each one can be asserted and each failure lands
// where it is expected to.

type ssoFlow struct {
	*smtpE2E
	idp                   *oidctest.FakeIDP
	browser               *http.Client // never follows redirects
	clientID, redirectURI string
	verifier, challenge   string
}

const ssoUsername = "alice@example.com" // the fake provider's verified email

func newSSOFlow(t *testing.T, opts ...func(*config.Config)) *ssoFlow {
	t.Helper()
	idp := oidctest.New(t)
	base := func(c *config.Config) {
		c.OIDCEnabled, c.MCPOAuthEnabled = true, true
		c.OIDCIssuer, c.OIDCClientID, c.OIDCClientSecret = idp.Issuer, oidctest.ClientID, "secret"
		c.OIDCScopes, c.OIDCUsernameClaim, c.OIDCButtonLabel = []string{"openid", "email"}, "email", "Test IdP"
	}
	e := newSMTPE2E(t, append([]func(*config.Config){base}, opts...)...)
	// The redirect URL names this test's server, which exists only now.
	e.srv.cfg.PublicURL = e.url
	e.srv.cfg.MCPPublicURL = e.url
	e.srv.cfg.OIDCRedirectURL = e.url + "/api/auth/oidc/callback"
	e.srv.InitOIDC(e.ctx)
	if e.srv.cfg.OIDCEnabled && e.srv.oidcProvider() == nil {
		t.Fatal("the fake provider was not discovered")
	}

	f := &ssoFlow{
		smtpE2E: e, idp: idp, clientID: uuid.NewString(), redirectURI: "https://client.example.test/cb",
		browser: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if err := e.store.CreateOAuthClient(e.ctx, f.clientID, "Test client", []string{f.redirectURI}); err != nil {
		t.Fatal(err)
	}
	f.verifier = auth.NewToken()
	sum := sha256.Sum256([]byte(f.verifier))
	f.challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return f
}

// user creates the account the provider's identity will be linked to.
func (f *ssoFlow) user(role string, password string) string {
	f.t.Helper()
	hash := ""
	if password != "" {
		var err error
		if hash, err = auth.HashPassword(password); err != nil {
			f.t.Fatal(err)
		}
	}
	u, err := f.store.CreateUser(f.ctx, ssoUsername, hash, role)
	if err != nil {
		f.t.Fatal(err)
	}
	return u.ID
}

func (f *ssoFlow) authzQuery() url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {f.clientID}, "redirect_uri": {f.redirectURI},
		"scope": {"mcp"}, "state": {"client-state"}, "code_challenge": {f.challenge}, "code_challenge_method": {"S256"},
	}
}

// get fetches a URL without following redirects.
func (f *ssoFlow) get(u string) (*http.Response, string) {
	f.t.Helper()
	res, err := f.browser.Get(u)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

// toProvider starts the consent-form sign-in and returns the provider URL it
// sends the browser to.
func (f *ssoFlow) toProvider() *url.URL {
	f.t.Helper()
	res, body := f.get(f.url + "/oauth/authorize/sso?" + f.authzQuery().Encode())
	if res.StatusCode != http.StatusFound {
		f.t.Fatalf("SSO start: status %d (%s)", res.StatusCode, body)
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if !strings.HasPrefix(loc.String(), f.idp.Issuer+"/authorize") {
		f.t.Fatalf("SSO start went to %s, not the provider", loc)
	}
	return loc
}

// throughProvider follows the provider back to the callback and returns what
// the callback answered.
func (f *ssoFlow) throughProvider(providerURL *url.URL) (*http.Response, string) {
	f.t.Helper()
	res, _ := f.get(providerURL.String())
	if res.StatusCode != http.StatusFound {
		f.t.Fatalf("provider: status %d", res.StatusCode)
	}
	return f.get(res.Header.Get("Location"))
}

var ticketRE = regexp.MustCompile(`name="sso_ticket" value="([^"]+)"`)

func TestMCPAuthorizationThroughSSOEndToEndIntegration(t *testing.T) {
	f := newSSOFlow(t)
	userID := f.user(models.RoleTech, "")

	// The consent page offers the provider.
	res, body := f.get(f.url + "/oauth/authorize?" + f.authzQuery().Encode())
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "Continue with Test IdP") {
		t.Fatalf("consent page without the SSO button: %d", res.StatusCode)
	}

	res, body = f.throughProvider(f.toProvider())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("callback: status %d (%s)", res.StatusCode, body)
	}
	if !strings.Contains(body, "Signed in as <b>"+ssoUsername+"</b>") || strings.Contains(body, `name="password"`) {
		t.Fatal("the callback did not show the signed-in consent page")
	}
	if res.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("the signed-in consent page can be framed")
	}
	// client.example.test is no known MCP client: the signed-in page warns too.
	if !strings.Contains(body, "not a known MCP client") {
		t.Error("the signed-in consent page does not warn about an unknown host")
	}
	m := ticketRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no grant ticket on the signed-in consent page")
	}

	form := f.authzQuery()
	form.Set("sso_ticket", m[1])
	form.Set("action", "allow")
	authRes, err := f.browser.PostForm(f.url+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	authRes.Body.Close()
	back, _ := url.Parse(authRes.Header.Get("Location"))
	if authRes.StatusCode != http.StatusFound || back.Query().Get("code") == "" || back.Query().Get("state") != "client-state" {
		t.Fatalf("authorize: status %d, Location %s", authRes.StatusCode, back)
	}

	tokRes, err := http.PostForm(f.url+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "code_verifier": {f.verifier},
		"client_id": {f.clientID}, "redirect_uri": {f.redirectURI},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(tokRes.Body).Decode(&tok)
	tokRes.Body.Close()
	access, _ := tok["access_token"].(string)
	if access == "" {
		t.Fatalf("token exchange failed: %v", tok)
	}

	// The token is the SSO user's and reaches /mcp.
	req, _ := http.NewRequest(http.MethodPost, f.url+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	mcpRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mcpRes.Body.Close()
	if mcpRes.StatusCode == http.StatusUnauthorized || mcpRes.StatusCode == http.StatusForbidden {
		t.Fatalf("/mcp refused the SSO-issued token: %d", mcpRes.StatusCode)
	}

	// Linked on this first sign-in, and the sign-in is on the record.
	linked, err := f.store.UserLinkedToSSO(f.ctx, userID)
	if err != nil || !linked {
		t.Errorf("the account was not linked to the provider identity (%v)", err)
	}
}

// Each failure ends where the sign-in started: a consent-form sign-in on the
// MCP message page, a console sign-in back on the console.
func TestSSOCallbackFailuresLandWhereTheyStartedIntegration(t *testing.T) {
	t.Run("provider refuses an MCP sign-in", func(t *testing.T) {
		f := newSSOFlow(t)
		f.user(models.RoleTech, "")
		f.idp.Deny = "access_denied"
		res, body := f.throughProvider(f.toProvider())
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Single sign-on did not complete") {
			t.Fatalf("status %d, want the MCP failure page", res.StatusCode)
		}
	})
	t.Run("MCP sign-in comes back without a code", func(t *testing.T) {
		f := newSSOFlow(t)
		state := f.toProvider().Query().Get("state")
		res, body := f.get(f.url + "/api/auth/oidc/callback?state=" + url.QueryEscape(state))
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Single sign-on did not complete") {
			t.Fatalf("status %d, want the MCP failure page", res.StatusCode)
		}
	})
	t.Run("console sign-in comes back without a code", func(t *testing.T) {
		f := newSSOFlow(t)
		res, _ := f.get(f.url + "/api/auth/oidc/start")
		state := mustURL(t, res.Header.Get("Location")).Query().Get("state")
		res, _ = f.get(f.url + "/api/auth/oidc/callback?state=" + url.QueryEscape(state))
		if res.StatusCode != http.StatusFound || !strings.HasSuffix(res.Header.Get("Location"), "#sso_error=sso_failed") {
			t.Fatalf("status %d Location %q, want the console with sso_failed", res.StatusCode, res.Header.Get("Location"))
		}
	})
	t.Run("forged state", func(t *testing.T) {
		f := newSSOFlow(t)
		res, _ := f.get(f.url + "/api/auth/oidc/callback?code=x&state=forged")
		if res.StatusCode != http.StatusFound || !strings.HasSuffix(res.Header.Get("Location"), "#sso_error=sso_expired") {
			t.Fatalf("status %d Location %q, want sso_expired", res.StatusCode, res.Header.Get("Location"))
		}
	})
	t.Run("provider did not satisfy the required MFA", func(t *testing.T) {
		f := newSSOFlow(t, func(c *config.Config) { c.OIDCRequiredAMR = []string{"mfa"} })
		f.user(models.RoleTech, "")
		f.idp.Claims = func(c map[string]any) map[string]any { c["amr"] = []string{"pwd"}; return c }
		res, body := f.throughProvider(f.toProvider())
		if res.StatusCode != http.StatusBadRequest || strings.Contains(body, "sso_ticket") {
			t.Fatalf("status %d: a sign-in without MFA got a grant", res.StatusCode)
		}
	})
	t.Run("disabled account", func(t *testing.T) {
		f := newSSOFlow(t)
		id := f.user(models.RoleTech, "")
		if _, err := f.store.SetUserDisabled(f.ctx, id, true); err != nil {
			t.Fatal(err)
		}
		res, body := f.throughProvider(f.toProvider())
		if res.StatusCode != http.StatusForbidden || strings.Contains(body, "sso_ticket") {
			t.Fatalf("status %d: a disabled account got a grant", res.StatusCode)
		}
	})
}

// A role without mcp.use is told at consent, through either path, instead of
// receiving a token /mcp would refuse.
func TestMCPConsentRequiresTheMCPPermissionIntegration(t *testing.T) {
	t.Run("single sign-on", func(t *testing.T) {
		f := newSSOFlow(t)
		f.user(models.RoleReadOnly, "")
		res, body := f.throughProvider(f.toProvider())
		if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "does not allow connecting MCP clients") {
			t.Fatalf("status %d, want the missing-permission page", res.StatusCode)
		}
		if f.auditCount("oauth.authorize.refuse", f.clientID) != 1 {
			t.Error("the refusal is not on the audit trail")
		}
	})
	t.Run("password", func(t *testing.T) {
		f := newSSOFlow(t, func(c *config.Config) { c.OIDCEnabled = false })
		f.user(models.RoleReadOnly, "Readonly-Passw0rd!")
		form := f.authzQuery()
		form.Set("username", ssoUsername)
		form.Set("password", "Readonly-Passw0rd!")
		form.Set("action", "allow")
		res, err := f.browser.PostForm(f.url+"/oauth/authorize", form)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusFound || !strings.Contains(string(body), "does not allow connecting MCP clients") {
			t.Fatalf("status %d: a role without mcp.use was authorized", res.StatusCode)
		}
		if f.auditCount("oauth.authorize.refuse", f.clientID) != 1 {
			t.Error("the refusal is not on the audit trail")
		}
	})
}

// The console sign-in, end to end: start, provider, callback, a session the
// API accepts.
func TestConsoleSignInThroughSSOEndToEndIntegration(t *testing.T) {
	f := newSSOFlow(t)
	f.user(models.RoleTech, "")

	res, _ := f.get(f.url + "/api/auth/oidc/start?redirect_to=/devices")
	if res.StatusCode != http.StatusFound {
		t.Fatalf("start: status %d", res.StatusCode)
	}
	res, _ = f.throughProvider(mustURL(t, res.Header.Get("Location")))
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(loc, f.url+"/devices#sso_token=") {
		t.Fatalf("callback: status %d Location %q", res.StatusCode, loc)
	}
	token, _ := url.QueryUnescape(strings.TrimPrefix(loc, f.url+"/devices#sso_token="))
	code, me := f.do(http.MethodGet, "/api/me", "", token)
	if code != http.StatusOK || me["username"] != ssoUsername {
		t.Fatalf("/api/me with the SSO session: %d %v", code, me)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// A pending application cannot be authorized through single sign-on either:
// the refusal comes before the trip to the provider.
func TestPendingAppCannotStartSSOIntegration(t *testing.T) {
	f := newSSOFlow(t)
	if _, err := f.store.GetOAuthClientInfo(f.ctx, f.clientID); err != nil {
		t.Fatal(err)
	}
	pending := uuid.NewString()
	if err := f.store.CreateOAuthClientWithStatus(f.ctx, pending, "Pending", []string{f.redirectURI}, "pending"); err != nil {
		t.Fatal(err)
	}
	f.clientID = pending
	res, body := f.get(f.url + "/oauth/authorize/sso?" + f.authzQuery().Encode())
	if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "waiting for an administrator") {
		t.Fatalf("SSO start for a pending application: %d", res.StatusCode)
	}
}
