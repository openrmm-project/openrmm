package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

func TestRelayRejectsViewerTicketInURL(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, "/ws/remote?session=session-1&role=viewer&token=secret", nil)
	req.Header.Set("Origin", "https://console.example.test")
	rr := httptest.NewRecorder()

	s.handleRelayWS(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestRelayRejectsLegacyAgentTicketByDefault(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, "/ws/remote?role=agent&token=secret", nil)
	rr := httptest.NewRecorder()

	s.handleRelayWS(rr, req)
	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUpgradeRequired)
	}
}

func TestSameOriginUsesTrustedForwardedHost(t *testing.T) {
	s := &Server{cfg: &config.Config{TrustProxyHeaders: true}}
	req := httptest.NewRequest(http.MethodGet, "http://server:8080/ws/remote", nil)
	req.Header.Set("Origin", "https://console.example.test:9443")
	req.Header.Set("X-Forwarded-Host", "console.example.test:9443")

	if !s.sameOrigin(req) {
		t.Fatal("trusted forwarded console origin was rejected")
	}
}

func TestRelayRejectsViewerFromAgentsIngress(t *testing.T) {
	s := &Server{cfg: &config.Config{EnforceConsoleRelayIngress: true}}
	req := httptest.NewRequest(http.MethodGet, "/ws/remote?session=session-1&role=viewer", nil)
	req.Header.Set("Origin", "https://agents.example.test")
	req.Header.Set("X-Forwarded-Host", "agents.example.test")
	req.Header.Set("X-RMM-Relay-Ingress", "agents")
	rr := httptest.NewRecorder()

	s.handleRelayWS(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestRemoteCapabilitiesRouteIsRegistered(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/devices/device-1/remote-capabilities", nil)

	s.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (registered route protected by auth)", rr.Code, http.StatusUnauthorized)
	}
}
