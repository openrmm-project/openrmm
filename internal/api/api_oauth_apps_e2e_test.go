package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// MCP applications under an administrator's control: the policy at
// registration and authorization, approval, and revocation that kills tokens
// at once.

type appsFixture struct {
	*smtpE2E
	browser            *http.Client
	username, password string
	verifier           string
	challenge          string
}

func newAppsFixture(t *testing.T) *appsFixture {
	t.Helper()
	e := newSMTPE2E(t, func(c *config.Config) { c.MCPOAuthEnabled = true })
	f := &appsFixture{
		smtpE2E: e, password: "Connector-Passw0rd!",
		browser: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	hash, err := auth.HashPassword(f.password)
	if err != nil {
		t.Fatal(err)
	}
	f.username = "tech-" + uuid.NewString()[:8]
	if _, err := e.store.CreateUser(e.ctx, f.username, hash, models.RoleTech); err != nil {
		t.Fatal(err)
	}
	f.verifier = auth.NewToken()
	sum := sha256.Sum256([]byte(f.verifier))
	f.challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return f
}

// register performs Dynamic Client Registration, as an MCP client would.
func (f *appsFixture) register(redirectURI string) (int, string) {
	f.t.Helper()
	res, err := http.Post(f.url+"/oauth/register", "application/json",
		strings.NewReader(fmt.Sprintf(`{"client_name":"Claude","redirect_uris":[%q]}`, redirectURI)))
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	id, _ := out["client_id"].(string)
	return res.StatusCode, id
}

func (f *appsFixture) policy(body string) int {
	f.t.Helper()
	code, _ := f.do(http.MethodPut, "/api/config/mcp-oauth", body, f.admin)
	return code
}

func (f *appsFixture) authzQuery(clientID, redirectURI string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"scope": {"mcp"}, "state": {"s"}, "code_challenge": {f.challenge}, "code_challenge_method": {"S256"},
	}
}

func (f *appsFixture) consentPage(clientID, redirectURI string) (int, string) {
	f.t.Helper()
	res, err := f.browser.Get(f.url + "/oauth/authorize?" + f.authzQuery(clientID, redirectURI).Encode())
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// connect runs the password consent and the code exchange, returning the
// access and refresh tokens.
func (f *appsFixture) connect(clientID, redirectURI string) (access, refresh string) {
	f.t.Helper()
	form := f.authzQuery(clientID, redirectURI)
	form.Set("username", f.username)
	form.Set("password", f.password)
	form.Set("action", "allow")
	res, err := f.browser.PostForm(f.url+"/oauth/authorize", form)
	if err != nil {
		f.t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	code := loc.Query().Get("code")
	if code == "" {
		f.t.Fatalf("consent did not issue a code: %d %s", res.StatusCode, loc)
	}
	tok := f.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {f.verifier},
		"client_id": {clientID}, "redirect_uri": {redirectURI}})
	access, _ = tok["access_token"].(string)
	refresh, _ = tok["refresh_token"].(string)
	if access == "" || refresh == "" {
		f.t.Fatalf("code exchange failed: %v", tok)
	}
	return access, refresh
}

func (f *appsFixture) token(form url.Values) map[string]any {
	f.t.Helper()
	res, err := http.PostForm(f.url+"/oauth/token", form)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out
}

func (f *appsFixture) mcpStatus(access string) int {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.url+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func (f *appsFixture) app(id string) *store.OAuthClient {
	f.t.Helper()
	app, err := f.store.GetOAuthClientInfo(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return app
}

// Revoking an application kills what it holds, now: the access token stops
// reaching /mcp, the refresh token cannot mint another, and it is gone from
// the list — without waiting for anything to expire.
func TestRevokingAnMCPAppKillsItsTokensIntegration(t *testing.T) {
	f := newAppsFixture(t)
	const cb = "https://claude.ai/api/mcp/auth_callback"
	code, id := f.register(cb)
	if code != http.StatusCreated {
		t.Fatalf("register: %d", code)
	}
	access, refresh := f.connect(id, cb)
	if s := f.mcpStatus(access); s == http.StatusUnauthorized || s == http.StatusForbidden {
		t.Fatalf("a fresh token was refused by /mcp: %d", s)
	}
	if a := f.app(id); a.ActiveTokens != 1 || a.LastUsedAt == nil {
		t.Errorf("the list should show one active token and a last use: %+v", a)
	}

	if code, out := f.do(http.MethodDelete, "/api/oauth-apps/"+id, "", f.admin); code != http.StatusOK {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if s := f.mcpStatus(access); s != http.StatusUnauthorized {
		t.Errorf("the revoked application's access token still reaches /mcp: %d", s)
	}
	if tok := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); tok["error"] != "invalid_grant" {
		t.Errorf("the revoked application's refresh token still works: %v", tok)
	}
	_, list := f.do(http.MethodGet, "/api/oauth-apps", "", f.admin)
	if strings.Contains(fmt.Sprint(list["apps"]), id) {
		t.Error("a revoked application is still listed")
	}
	entries, _, err := f.store.ListAudit(f.ctx, store.AuditFilter{Action: "oauth.app.revoke", Target: id, Limit: 5})
	if err != nil || len(entries) != 1 {
		t.Errorf("revocation not audited: %v %d", err, len(entries))
	}
}

// Revoked while someone is between the provider and the Authorize button: the
// grant goes with the application.
func TestRevokingAnMCPAppMidSSOFlowIntegration(t *testing.T) {
	f := newAppsFixture(t)
	const cb = "https://claude.ai/cb"
	_, id := f.register(cb)
	userID, _, _, err := f.store.GetUserAuth(f.ctx, f.username)
	if err != nil {
		t.Fatal(err)
	}
	ticket := auth.NewToken()
	if err := f.store.CreateOAuthSSOGrant(f.ctx, auth.Hash(ticket), userID, id, cb, f.challenge, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.do(http.MethodDelete, "/api/oauth-apps/"+id, "", f.admin); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	form := f.authzQuery(id, cb)
	form.Set("sso_ticket", ticket)
	form.Set("action", "allow")
	res, err := f.browser.PostForm(f.url+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc, _ := url.Parse(res.Header.Get("Location")); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("a revoked application completed an authorization")
	}
	if _, err := f.store.ConsumeOAuthSSOGrant(f.ctx, auth.Hash(ticket)); err == nil {
		t.Error("the revoked application's SSO grant survived")
	}
}

// With approval required, a new application waits: its consent page says so
// and issues nothing, until an administrator approves it.
func TestMCPAppApprovalIntegration(t *testing.T) {
	f := newAppsFixture(t)
	if code := f.policy(`{"registration":"open","allowed_hosts":[],"require_approval":true}`); code != http.StatusOK {
		t.Fatalf("policy: %d", code)
	}
	const cb = "https://claude.ai/cb"
	code, id := f.register(cb)
	if code != http.StatusCreated || f.app(id).Status != store.OAuthClientPending {
		t.Fatalf("a new application should register as pending: %d", code)
	}
	status, body := f.consentPage(id, cb)
	if status != http.StatusForbidden || !strings.Contains(body, "waiting for an administrator") || strings.Contains(body, `name="password"`) {
		t.Fatalf("pending application's consent page: %d", status)
	}
	// Posting the form anyway issues nothing.
	form := f.authzQuery(id, cb)
	form.Set("username", f.username)
	form.Set("password", f.password)
	form.Set("action", "allow")
	res, err := f.browser.PostForm(f.url+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc, _ := url.Parse(res.Header.Get("Location")); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("a pending application received a code")
	}

	if code, _ := f.do(http.MethodPost, "/api/oauth-apps/"+id+"/approve", "", f.admin); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	if a := f.app(id); a.Status != store.OAuthClientApproved || a.ApprovedBy == "" || a.ApprovedAt == nil {
		t.Errorf("approval not recorded: %+v", a)
	}
	f.connect(id, cb)
}

// With only listed hosts allowed, other applications can neither register nor
// — if they registered before the change — authorize.
func TestMCPAppAllowlistIntegration(t *testing.T) {
	f := newAppsFixture(t)
	const outside = "https://attacker.test/cb"
	_, earlier := f.register(outside) // while the instance is still open

	if code := f.policy(`{"registration":"allowlist","allowed_hosts":[],"require_approval":false}`); code != http.StatusBadRequest {
		t.Errorf("an empty allowlist would lock everyone out and must be refused: %d", code)
	}
	if code := f.policy(`{"registration":"allowlist","allowed_hosts":["https://claude.ai"],"require_approval":false}`); code != http.StatusBadRequest {
		t.Errorf("a URL is not a host: %d", code)
	}
	if code := f.policy(`{"registration":"allowlist","allowed_hosts":["claude.ai"],"require_approval":false}`); code != http.StatusOK {
		t.Fatalf("policy: %d", code)
	}
	if code, _ := f.register(outside); code != http.StatusBadRequest {
		t.Errorf("an unlisted host registered: %d", code)
	}
	if code, _ := f.register("https://claude.ai/cb"); code != http.StatusCreated {
		t.Errorf("a listed host was refused: %d", code)
	}
	if status, body := f.consentPage(earlier, outside); status != http.StatusForbidden || !strings.Contains(body, "not allowed on this OpenRMM instance") {
		t.Errorf("an application registered before the allowlist can still authorize: %d", status)
	}
}

// The consent page warns about a host that is not a known MCP client, and not
// about one that is.
func TestConsentWarnsAboutUnknownHostsIntegration(t *testing.T) {
	f := newAppsFixture(t)
	_, known := f.register("https://claude.ai/cb")
	_, unknown := f.register("https://attacker.test/cb")
	if _, body := f.consentPage(known, "https://claude.ai/cb"); strings.Contains(body, "not a known MCP client") {
		t.Error("warned about claude.ai")
	}
	if _, body := f.consentPage(unknown, "https://attacker.test/cb"); !strings.Contains(body, "not a known MCP client") {
		t.Error("no warning for attacker.test")
	}
}

// The list and its controls take api_tokens.manage, and changes to them an
// unscoped principal: the policy and the applications are instance-wide.
func TestMCPAppRoutesArePermissionedIntegration(t *testing.T) {
	f := newAppsFixture(t)
	_, id := f.register("https://claude.ai/cb")
	tech := f.session(models.RoleTech, nil)
	client, err := f.store.CreateClient(f.ctx, "tenant-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	scoped := f.session(models.RoleAdmin, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	for _, who := range []struct {
		name, token string
	}{{"tech", tech}, {"scoped admin", scoped}} {
		for _, tt := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/oauth-apps/" + id + "/approve", ""},
			{http.MethodDelete, "/api/oauth-apps/" + id, ""},
			{http.MethodPut, "/api/config/mcp-oauth", `{"registration":"open","allowed_hosts":[],"require_approval":true}`},
		} {
			if code, _ := f.do(tt.method, tt.path, tt.body, who.token); code != http.StatusForbidden {
				t.Errorf("%s %s as %s: %d, want 403", tt.method, tt.path, who.name, code)
			}
		}
	}
	if code, _ := f.do(http.MethodGet, "/api/oauth-apps", "", tech); code != http.StatusForbidden {
		t.Errorf("tech listed the applications: %d", code)
	}
	if f.app(id).Status != store.OAuthClientApproved {
		t.Error("a refused request changed the application")
	}
}
