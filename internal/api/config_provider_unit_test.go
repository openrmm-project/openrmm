package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

// The console offers exactly the providers GET /api/config lists, so that list
// must be what PUT accepts as the default. A default the server refuses is
// rejected before any setting is read or written: this server has no store,
// and reaching it would panic.
func TestDefaultRemoteProviderRejectedBeforePersistence(t *testing.T) {
	if slices.Contains(defaultRemoteProviders, "terminal") || slices.Contains(defaultRemoteProviders, "rustdesk") {
		t.Fatalf("offered defaults include a provider that is not a remote session default: %v", defaultRemoteProviders)
	}
	for _, accepted := range []string{"native", "meshcentral"} {
		if !slices.Contains(defaultRemoteProviders, accepted) {
			t.Fatalf("default %q is no longer offered: %v", accepted, defaultRemoteProviders)
		}
	}
	s := &Server{cfg: &config.Config{}}
	for _, provider := range []string{"terminal", "rustdesk", "files", "NATIVE", "native "} {
		req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"remote":{"default":"`+provider+`","meshcentral_token_key":"must-not-save-test-token"}}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.updateConfig(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid remote provider") {
			t.Fatalf("default %q = %d %s", provider, rec.Code, rec.Body.String())
		}
	}
}
