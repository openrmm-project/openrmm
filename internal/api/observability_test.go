package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

func TestBearerEquals(t *testing.T) {
	cases := []struct {
		header, expected string
		want             bool
	}{
		{"Bearer secret", "secret", true},
		{"Bearer  secret ", "secret", true},
		{"Bearer wrong", "secret", false},
		{"secret", "secret", false}, // missing scheme
		{"Basic secret", "secret", false},
		{"Bearer ", "", false}, // empty token never matches (endpoint is disabled anyway)
	}
	for _, c := range cases {
		if got := bearerEquals(c.header, c.expected); got != c.want {
			t.Errorf("bearerEquals(%q,%q)=%v want %v", c.header, c.expected, got, c.want)
		}
	}
}

func TestMetricsDisabledWithoutToken(t *testing.T) {
	s := &Server{cfg: &config.Config{}} // no MetricsToken
	rec := httptest.NewRecorder()
	s.metrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when metrics disabled", rec.Code)
	}
}

func TestMetricsRejectsWrongToken(t *testing.T) {
	s := &Server{cfg: &config.Config{MetricsToken: "right"}}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	s.metrics(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a wrong token", rec.Code)
	}
}
