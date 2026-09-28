package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
)

type ssoOAuthFixture struct {
	*smtpE2E
	clientID, redirectURI, verifier, challenge, userID string
}

func newSSOOAuthFixture(t *testing.T) *ssoOAuthFixture {
	t.Helper()
	e := newSMTPE2E(t, func(c *config.Config) { c.MCPOAuthEnabled = true })
	f := &ssoOAuthFixture{smtpE2E: e, clientID: uuid.NewString(), redirectURI: "https://client.example.test/cb"}
	if err := e.store.CreateOAuthClient(e.ctx, f.clientID, "Test client", []string{f.redirectURI}); err != nil {
		t.Fatal(err)
	}
	f.verifier = auth.NewToken()
	sum := sha256.Sum256([]byte(f.verifier))
	f.challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	u, err := e.store.CreateUser(e.ctx, "sso-"+uuid.NewString()[:8], "", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	f.userID = u.ID
	return f
}

// grant mints a ticket the way the SSO callback does.
func (f *ssoOAuthFixture) grant(clientID, challenge string, ttl time.Duration) string {
	f.t.Helper()
	ticket := auth.NewToken()
	if err := f.store.CreateOAuthSSOGrant(f.ctx, auth.Hash(ticket), f.userID, clientID, f.redirectURI, challenge, time.Now().Add(ttl)); err != nil {
		f.t.Fatal(err)
	}
	return ticket
}

// submit posts the consent form and reports the redirect it answered with, if
// any, and the body otherwise.
func (f *ssoOAuthFixture) submit(ticket, action, challenge string) (location *url.URL, body string) {
	f.t.Helper()
	form := url.Values{
		"response_type": {"code"}, "client_id": {f.clientID}, "redirect_uri": {f.redirectURI},
		"scope": {"mcp"}, "state": {"s1"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"sso_ticket": {ticket}, "action": {action},
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.PostForm(f.url+"/oauth/authorize", form)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusFound {
		loc, err := url.Parse(res.Header.Get("Location"))
		if err != nil {
			f.t.Fatal(err)
		}
		return loc, ""
	}
	return nil, string(raw)
}

func TestOAuthSSOGrantAuthorizesOnceIntegration(t *testing.T) {
	f := newSSOOAuthFixture(t)
	ticket := f.grant(f.clientID, f.challenge, time.Minute)

	loc, body := f.submit(ticket, "allow", f.challenge)
	if loc == nil || loc.Query().Get("code") == "" || loc.Query().Get("state") != "s1" {
		t.Fatalf("a valid grant did not authorize: %v %s", loc, body)
	}
	// The code it produced is this user's, and PKCE still has to match.
	tokenRes, err := http.PostForm(f.url+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "code_verifier": {f.verifier},
		"client_id": {f.clientID}, "redirect_uri": {f.redirectURI},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(tokenRes.Body).Decode(&tok)
	tokenRes.Body.Close()
	if tok["access_token"] == nil {
		t.Fatalf("the SSO-issued code did not exchange: %v", tok)
	}

	// Replayed: spent.
	if loc, body := f.submit(ticket, "allow", f.challenge); loc != nil || !strings.Contains(body, "expired or was for another request") {
		t.Fatalf("a replayed grant authorized again: %v", loc)
	}
}

func TestOAuthSSOGrantIsBoundToItsRequestIntegration(t *testing.T) {
	f := newSSOOAuthFixture(t)

	// Presented for another PKCE challenge: refused, and spent by the attempt.
	ticket := f.grant(f.clientID, f.challenge, time.Minute)
	if loc, _ := f.submit(ticket, "allow", "another-challenge"); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("a grant authorized a request with another PKCE challenge")
	}
	if loc, _ := f.submit(ticket, "allow", f.challenge); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("a grant survived a mismatched attempt and was used afterwards")
	}

	// Minted for another client: refused.
	other := uuid.NewString()
	if err := f.store.CreateOAuthClient(f.ctx, other, "Other", []string{f.redirectURI}); err != nil {
		t.Fatal(err)
	}
	if loc, _ := f.submit(f.grant(other, f.challenge, time.Minute), "allow", f.challenge); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("a grant minted for another client authorized this one")
	}

	// Expired: refused.
	if loc, _ := f.submit(f.grant(f.clientID, f.challenge, -time.Second), "allow", f.challenge); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("an expired grant authorized")
	}
}

func TestOAuthSSODenySpendsTheGrantIntegration(t *testing.T) {
	f := newSSOOAuthFixture(t)
	ticket := f.grant(f.clientID, f.challenge, time.Minute)
	loc, _ := f.submit(ticket, "deny", f.challenge)
	if loc == nil || loc.Query().Get("error") != "access_denied" {
		t.Fatalf("deny: %v, want access_denied", loc)
	}
	if loc, _ := f.submit(ticket, "allow", f.challenge); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("a denied grant was still usable")
	}
}

func TestOAuthSSOGrantOfADisabledUserIsRefusedIntegration(t *testing.T) {
	f := newSSOOAuthFixture(t)
	ticket := f.grant(f.clientID, f.challenge, time.Minute)
	if _, err := f.store.SetUserDisabled(f.ctx, f.userID, true); err != nil {
		t.Fatal(err)
	}
	if loc, _ := f.submit(ticket, "allow", f.challenge); loc != nil && loc.Query().Get("code") != "" {
		t.Fatal("a user disabled after signing in still authorized a client")
	}
}

// The OIDC state carries the authorization request for a consent-form sign-in,
// and nothing for a console one.
func TestOIDCStateCarriesTheOAuthRequestIntegration(t *testing.T) {
	f := newSSOOAuthFixture(t)
	req, _ := json.Marshal(sampleAuthz())
	if err := f.store.CreateOIDCStateForOAuth(f.ctx, "h-oauth", "n", "v", req, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateOIDCState(f.ctx, "h-console", "n", "v", "/devices", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ConsumeOIDCState(f.ctx, "h-oauth")
	if err != nil {
		t.Fatal(err)
	}
	var p authzParams
	if err := json.Unmarshal(got.OAuthRequest, &p); err != nil || p != sampleAuthz() {
		t.Fatalf("request did not survive the trip: %v %+v", err, p)
	}
	console, err := f.store.ConsumeOIDCState(f.ctx, "h-console")
	if err != nil {
		t.Fatal(err)
	}
	if console.OAuthRequest != nil || console.RedirectTo != "/devices" {
		t.Fatalf("a console sign-in picked up an OAuth request: %+v", console)
	}
}
