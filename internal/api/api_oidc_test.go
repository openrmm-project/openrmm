package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/oidc"
	"github.com/p1ngu/rmm/internal/store"
)

// safeRedirectPath is the one place a caller-supplied destination reaches a
// redirect. An open redirect here is not a cosmetic bug: it makes a phishing
// link that starts on the real console domain, survives a careful look at the
// address bar, and lands on someone else's login form.
func TestSafeRedirectPathKeepsTheBrowserOnThisSite(t *testing.T) {
	kept := []struct{ in, want string }{
		{"/devices", "/devices"},
		{"/devices?tab=alerts", "/devices?tab=alerts"},
		{"/", "/"},
	}
	for _, c := range kept {
		if got := safeRedirectPath(c.in); got != c.want {
			t.Errorf("safeRedirectPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Each of these is a way to leave the site. They are discarded, not
	// repaired: a "corrected" hostile redirect is still a redirect someone
	// crafted, and the cost of dropping it is one extra click for a legitimate
	// caller who sent something malformed.
	rejected := []string{
		"https://evil.example/steal",  // absolute
		"http://evil.example",         // absolute, plain
		"//evil.example",              // protocol-relative: a browser treats this as a host
		"///evil.example",             // three slashes, same trick
		"\\\\evil.example",            // backslashes, which some parsers fold to slashes
		"/\\evil.example",             // mixed, same reason
		"javascript:alert(1)",         // not a path at all
		"devices",                     // relative, ambiguous against the base
		"",                            // nothing asked for
		"/devices\r\nSet-Cookie: a=b", // header injection through the Location value
		"/devices\nX: y",
	}
	for _, in := range rejected {
		if got := safeRedirectPath(in); got != "" {
			t.Errorf("safeRedirectPath(%q) = %q, want it discarded", in, got)
		}
	}
}

// consoleURL builds the address the browser is sent back to. It must never be
// able to point off-site, whatever it is handed.
func TestConsoleURLStaysOnTheConsole(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: "https://rmm.example.com/"}}

	if got := s.consoleURL(""); got != "https://rmm.example.com/" {
		t.Errorf("consoleURL(\"\") = %q", got)
	}
	if got := s.consoleURL("/devices"); got != "https://rmm.example.com/devices" {
		t.Errorf("consoleURL(\"/devices\") = %q", got)
	}
	// Whatever safeRedirectPath already refused cannot arrive here, but the
	// concatenation is checked anyway: "//evil.example" appended to a bare base
	// is a protocol-relative URL.
	if got := s.consoleURL(safeRedirectPath("//evil.example")); got != "https://rmm.example.com/" {
		t.Errorf("consoleURL of a rejected path = %q, want the console root", got)
	}
}

func TestOIDCDisabledEndpoints(t *testing.T) {
	s := &Server{cfg: &config.Config{OIDCButtonLabel: "Company SSO"}}

	status := httptest.NewRecorder()
	s.oidcStatus(status, httptest.NewRequest(http.MethodGet, "/api/auth/oidc", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"enabled":false`) {
		t.Fatalf("status response = %d %s", status.Code, status.Body.String())
	}

	for name, handler := range map[string]http.HandlerFunc{
		"start": s.oidcStart, "callback": s.oidcCallback,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", w.Code)
			}
		})
	}
}

func TestOIDCCallbackRejectsIncompleteOrDeniedResponses(t *testing.T) {
	s := &Server{
		cfg:  &config.Config{PublicURL: "https://rmm.example.com"},
		oidc: &oidc.Provider{},
	}

	status := httptest.NewRecorder()
	s.oidcStatus(status, httptest.NewRequest(http.MethodGet, "/api/auth/oidc", nil))
	if !strings.Contains(status.Body.String(), `"enabled":true`) {
		t.Fatalf("enabled status = %s", status.Body.String())
	}

	for _, tc := range []struct {
		name, query, reason string
	}{
		{"provider denied", "?error=access_denied&error_description=no", "sso_denied"},
		{"missing state", "?code=code", "sso_failed"},
		{"missing code", "?state=state", "sso_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.oidcCallback(w, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback"+tc.query, nil))
			if w.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", w.Code)
			}
			if got := w.Header().Get("Location"); got != "https://rmm.example.com/#sso_error="+tc.reason {
				t.Fatalf("Location = %q", got)
			}
		})
	}
}

func TestOIDCFirstLinkRequiresVerifiedEmail(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   oidc.Identity
		ok   bool
	}{
		{"verified email", oidc.Identity{Username: "Alice@Example.com", Email: "alice@example.com", EmailVerified: true}, true},
		{"unverified email", oidc.Identity{Username: "alice@example.com", Email: "alice@example.com"}, false},
		{"different verified email", oidc.Identity{Username: "victim@example.com", Email: "attacker@example.com", EmailVerified: true}, false},
		{"non-email directory username", oidc.Identity{Username: "alice"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := oidcFirstLinkAllowed(&tc.id) == nil; got != tc.ok {
				t.Fatalf("allowed = %v, want %v", got, tc.ok)
			}
		})
	}
}

func TestResolveOIDCUserIntegration(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newTestStore(t, databaseURL)
	s := &Server{
		cfg:   &config.Config{OIDCAutoProvision: true, OIDCDefaultRole: "viewer"},
		store: st,
	}

	local, err := st.CreateUser(ctx, "alice@example.com", "unused-hash", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	unverified := &oidc.Identity{
		Issuer: "https://issuer.example", Subject: "alice-sub",
		Username: "alice@example.com", Email: "alice@example.com",
	}
	if _, err := s.resolveOIDCUser(ctx, unverified); err == nil {
		t.Fatal("an unverified address linked a local account")
	}
	if _, err := st.GetUserByOIDC(ctx, unverified.Issuer, unverified.Subject); err != store.ErrNotFound {
		t.Fatalf("unverified identity lookup = %v, want not found", err)
	}

	verified := *unverified
	verified.EmailVerified = true
	got, err := s.resolveOIDCUser(ctx, &verified)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != local.ID {
		t.Fatalf("linked user = %q, want existing %q", got.ID, local.ID)
	}

	// Once linked, issuer+subject is authoritative. A provider omitting the
	// optional email_verified claim later must not lock the account out.
	got, err = s.resolveOIDCUser(ctx, unverified)
	if err != nil || got.ID != local.ID {
		t.Fatalf("already-linked identity = %#v, %v", got, err)
	}

	if _, err := s.resolveOIDCUser(ctx, &oidc.Identity{
		Issuer: "https://issuer.example", Subject: "new-email-sub",
		Username: "new@example.com", Email: "new@example.com",
	}); err == nil {
		t.Fatal("automatic provisioning accepted an unverified email")
	}
	provisioned, err := s.resolveOIDCUser(ctx, &oidc.Identity{
		Issuer: "https://issuer.example", Subject: "directory-sub", Username: "directory-user",
	})
	if err != nil || provisioned.Username != "directory-user" {
		t.Fatalf("directory username was not provisioned: %#v, %v", provisioned, err)
	}
}
