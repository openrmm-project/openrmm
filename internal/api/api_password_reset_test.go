package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

// The request endpoint is unauthenticated and sends mail through the
// operator's relay, so it sits behind the strict auth limiter. These requests
// carry no valid address, so nothing past the limiter touches the store.
func TestPasswordResetRequestIsRateLimited(t *testing.T) {
	h := (&Server{cfg: &config.Config{}}).Router()
	var last int
	for i := 0; i < authRateLimit+1; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/password-reset", strings.NewReader(`{"email":""}`))
		req.RemoteAddr = "203.0.113.7:4000"
		h.ServeHTTP(rr, req)
		last = rr.Code
		if i < authRateLimit && rr.Code != http.StatusAccepted {
			t.Fatalf("request %d: status %d, want 202", i+1, rr.Code)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("request %d: status %d, want 429", authRateLimit+1, last)
	}
}

// Whatever was sent — nothing, garbage, a well-formed address — the answer is
// the same, so the endpoint cannot be used to learn anything about accounts.
// (The well-formed case, which reaches the store, is covered with a database
// in TestPasswordResetFlowIntegration.)
func TestPasswordResetRequestAnswersIdentically(t *testing.T) {
	h := (&Server{cfg: &config.Config{}}).Router()
	var first string
	for i, body := range []string{`{}`, `{"email":""}`, `{"email":"not an address"}`, `not json`} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/password-reset", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.8:4000"
		h.ServeHTTP(rr, req)
		got := rr.Body.String()
		if rr.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d, want 202", body, rr.Code)
		}
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("%s answered differently:\n%s\nvs\n%s", body, got, first)
		}
	}
}

// The link is built from RMM_PUBLIC_URL, never from the request: a Host header
// an attacker controls must not decide where a victim's reset link points.
func TestPasswordResetURLComesFromConfig(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: "https://console.example.test/"}}
	if got := s.passwordResetURL("tok"); got != "https://console.example.test/?reset=tok" {
		t.Fatalf("passwordResetURL = %q", got)
	}
}
