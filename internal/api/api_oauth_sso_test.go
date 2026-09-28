package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/oidc"
)

func sampleAuthz() authzParams {
	return authzParams{
		ResponseType: "code", ClientID: "client-1", RedirectURI: "https://client.example.test/cb?x=1",
		Scope: "mcp", State: "st&ate", CodeChallenge: "challenge", CodeChallengeMethod: "S256",
	}
}

// The consent page must never be framed: a consent button under someone
// else's page is how a click is stolen.
func TestConsentPageRefusesToBeFramed(t *testing.T) {
	rr := httptest.NewRecorder()
	renderConsentPage(rr, (&Server{cfg: &config.Config{}}).passwordConsent(sampleAuthz(), "Client", ""))
	if rr.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(rr.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("consent page can be framed: %v", rr.Header())
	}
	if !strings.Contains(rr.Body.String(), "client.example.test") {
		t.Error("the page should name the host the client redirects to")
	}
}

// The SSO button appears only when a provider is available, and carries the
// whole request so the provider trip can come back to it.
func TestConsentPageOffersSSOOnlyWithAProvider(t *testing.T) {
	s := &Server{cfg: &config.Config{OIDCButtonLabel: "Okta"}}
	rr := httptest.NewRecorder()
	renderConsentPage(rr, s.passwordConsent(sampleAuthz(), "", ""))
	if strings.Contains(rr.Body.String(), "/oauth/authorize/sso") {
		t.Fatal("SSO button shown with no provider")
	}

	s.oidc = &oidc.Provider{}
	rr = httptest.NewRecorder()
	renderConsentPage(rr, s.passwordConsent(sampleAuthz(), "", ""))
	body := rr.Body.String()
	if !strings.Contains(body, "Continue with Okta") {
		t.Fatal("SSO button missing with a provider")
	}
	i := strings.Index(body, `href="/oauth/authorize/sso?`)
	if i < 0 {
		t.Fatal("SSO link missing")
	}
	href := body[i+len(`href="`):]
	href = strings.ReplaceAll(href[:strings.Index(href, `"`)], "&amp;", "&")
	u, err := url.Parse(href)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseAuthzParams(u.Query()); got != sampleAuthz() {
		t.Fatalf("the SSO link lost the request: %+v", got)
	}
}

// Once signed in through SSO the page carries the ticket, asks for no password,
// and posts to the MCP origin (it is shown on the console's).
func TestSignedInConsentPageCarriesTheTicket(t *testing.T) {
	s := &Server{cfg: &config.Config{MCPPublicURL: "https://mcp.example.test/"}}
	rr := httptest.NewRecorder()
	renderConsentPage(rr, consentView{P: sampleAuthz(), SSOUser: "ana@example.test", Ticket: "tkt", Action: s.oauthAuthorizeAction()})
	body := rr.Body.String()
	for _, want := range []string{`name="sso_ticket" value="tkt"`, `action="https://mcp.example.test/oauth/authorize"`, "ana@example.test"} {
		if !strings.Contains(body, want) {
			t.Errorf("signed-in page lacks %s", want)
		}
	}
	if strings.Contains(body, `name="password"`) {
		t.Error("signed-in page still asks for a password")
	}
}

// Without a provider the SSO route says so instead of redirecting anywhere.
func TestOAuthAuthorizeSSORouteIsGated(t *testing.T) {
	rr := httptest.NewRecorder()
	(&Server{cfg: &config.Config{}}).Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/oauth/authorize/sso", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("with MCP OAuth off the route must not exist: status %d", rr.Code)
	}
}

// The warning is part of the page whenever the view says the host is unknown,
// and absent otherwise; which hosts are unknown is the policy's business
// (settings.MCPOAuthPolicy.HostKnown).
func TestConsentPageWarnsAboutAnUnknownHost(t *testing.T) {
	v := (&Server{cfg: &config.Config{}}).passwordConsent(sampleAuthz(), "Claude", "")
	rr := httptest.NewRecorder()
	renderConsentPage(rr, v)
	if strings.Contains(rr.Body.String(), "not a known MCP client") {
		t.Fatal("warned about a host the view marked as known")
	}
	v.UnknownHost = true
	rr = httptest.NewRecorder()
	renderConsentPage(rr, v)
	if !strings.Contains(rr.Body.String(), "not a known MCP client") || !strings.Contains(rr.Body.String(), "client.example.test") {
		t.Fatal("no warning naming the unknown host")
	}
}
