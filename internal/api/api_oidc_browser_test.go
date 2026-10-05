package api

import (
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/oidc"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOIDCBrowserCookieAndCallbackBinding(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: "https://rmm.example"}, oidc: &oidc.Provider{}}
	req := httptest.NewRequest("GET", "https://rmm.example/api/auth/oidc/start", nil)
	w := httptest.NewRecorder()
	s.setOIDCBrowser(w, req, "synthetic-state", 600)
	c := w.Result().Cookies()[0]
	if c.Name != "__Host-rmm_oidc_login" || !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe cookie: %+v", c)
	}
	req.AddCookie(c)
	if !s.oidcBrowserMatches(req, "synthetic-state") || s.oidcBrowserMatches(req, "different-state") {
		t.Fatal("browser binding did not enforce state")
	}
	for _, cookie := range []string{"", "__Host-rmm_oidc_login=another-browser"} {
		r := httptest.NewRequest("GET", "https://rmm.example/api/auth/oidc/callback?state=synthetic-state&code=synthetic", nil)
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		s.oidcCallback(w, r)
		// No store/provider exchange is available: upstream rejection must occur first.
		if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "sso_error=sso_failed") {
			t.Fatalf("unbound callback accepted: %d", w.Code)
		}
	}
}

func TestOIDCExchangeRejectsUnboundRequestsBeforeDatabase(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: "https://rmm.example"}}
	for _, origin := range []string{"", "https://attacker.example", "https://rmm.example"} {
		r := httptest.NewRequest("POST", "https://rmm.example/api/auth/oidc/exchange", strings.NewReader(`{"code":"synthetic"}`))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.oidcExchange(w, r)
		want := http.StatusForbidden
		if origin == "https://rmm.example" {
			want = http.StatusUnauthorized
		}
		if w.Code != want {
			t.Fatalf("origin %q: %d, want %d", origin, w.Code, want)
		}
	}
}

func TestOIDCUsesConsoleOriginWhenAgentsAndConsoleAreSeparate(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: "https://agents.example", OIDCRedirectURL: "HTTPS://console.example/base/api/auth/oidc/callback"}}
	if got := s.consoleURL("/base/devices"); got != "https://console.example/base/devices" {
		t.Fatalf("console URL: %s", got)
	}
	r := httptest.NewRequest("POST", "http://backend/api/auth/oidc/exchange", strings.NewReader(`{"code":"synthetic"}`))
	r.Header.Set("Origin", "https://console.example")
	if name := s.oidcBrowserCookieName(r); name != "__Host-rmm_oidc_login" {
		t.Fatalf("cookie behind TLS termination: %s", name)
	}
	w := httptest.NewRecorder()
	s.oidcExchange(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("legitimate console origin rejected: %d", w.Code)
	}
}

func TestOIDCCallbackPathDoesNotPrefixConsoleDestination(t *testing.T) {
	for _, callback := range []string{"https://console.example/api/auth/oidc/callback", "https://console.example/base/api/auth/oidc/callback", "https://console.example/oidc-callback"} {
		s := &Server{cfg: &config.Config{PublicURL: "https://agents.example", OIDCRedirectURL: callback}}
		if got := s.consoleURL("/console/devices?tab=all"); got != "https://console.example/console/devices?tab=all" {
			t.Fatalf("callback %s: %s", callback, got)
		}
	}
}
