package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
)

func TestServerVersionComesFromTheBuild(t *testing.T) {
	if got := serverVersion(&config.Config{Version: "0.6.2"}); got != "0.6.2" {
		t.Errorf("stamped build = %q, want 0.6.2", got)
	}
	// Unstamped says so, rather than a number that looks like a release.
	for _, cfg := range []*config.Config{nil, {}} {
		if got := serverVersion(cfg); got != "dev" {
			t.Errorf("unstamped build = %q, want dev", got)
		}
	}
}

// Settings ▸ Configuration shows server.version. It was the literal "0.2.1" on
// every release; it is the version the binary was built with.
func TestConfigReportsTheBuildVersionIntegration(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st := newTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, "version-"+uuid.NewString(), "hash", models.RoleReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := st.CreateSession(ctx, user.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{cfg: &config.Config{APIRateLimit: 10000, Version: "9.8.7"}, store: st}).Router()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Server struct {
			Version string `json:"version"`
		} `json:"server"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Server.Version != "9.8.7" {
		t.Errorf("server.version = %q, want the build's 9.8.7", out.Server.Version)
	}
}
