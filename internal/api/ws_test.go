package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/p1ngu/rmm/internal/config"
)

// A viewer offers the ticket and auth token as extra subprotocols. If the 101
// response echoes none of them the browser fails the connection (RFC 6455 4.1)
// and the console loops on "reconnecting", so the echo is part of the contract.
func TestRelayUpgraderEchoesViewerSubprotocol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := relayUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	dialer := websocket.Dialer{Subprotocols: []string{relaySubprotocol, "rmm-ticket.secret", "rmm-auth.token"}}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != relaySubprotocol {
		t.Fatalf("Sec-WebSocket-Protocol = %q, want %q", got, relaySubprotocol)
	}
	if got := conn.Subprotocol(); got != relaySubprotocol {
		t.Fatalf("negotiated subprotocol = %q, want %q", got, relaySubprotocol)
	}
}

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
