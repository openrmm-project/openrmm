package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

// The first admin of an empty instance used to belong to whoever asked first.
// These run without a database on purpose: every refusal must happen before
// the store is touched, so a nil store proves it was not.
func TestBootstrapRequiresAdminToken(t *testing.T) {
	const adminToken = "0123456789abcdef0123456789abcdef-admin"
	srv := &Server{cfg: &config.Config{AdminToken: adminToken, APIRateLimit: 10000}}
	body := `{"username":"first","password":"a-long-enough-password-1"}`

	cases := []struct {
		name, authorization string
		want                int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"wrong token", "Bearer " + strings.Repeat("x", len(adminToken)), http.StatusUnauthorized},
		{"token prefix", "Bearer " + adminToken[:len(adminToken)-1], http.StatusUnauthorized},
		{"token without the Bearer scheme", adminToken, http.StatusUnauthorized},
		{"a session-shaped token", "Bearer rmm_session_not_the_admin_token", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()
			srv.bootstrapAdmin(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	// With the right token the request gets past the gate — here to the body
	// check, which an empty body fails without reaching the store either.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.bootstrapAdmin(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("admin token: status = %d, want 400 from the body check (%s)", rec.Code, rec.Body.String())
	}
}

// A server that somehow has no admin token must not read that as "no token
// needed": an empty expected value would otherwise match an empty header.
func TestBootstrapRefusesWhenNoAdminTokenIsConfigured(t *testing.T) {
	srv := &Server{cfg: &config.Config{APIRateLimit: 10000}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap",
		strings.NewReader(`{"username":"first","password":"a-long-enough-password-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	srv.bootstrapAdmin(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
