package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// scriptFixture is a router plus a signed-in admin. Router-level because the
// category routes are gated by permission middleware, and because a category
// name has to survive the URL on the way in.
type scriptFixture struct {
	t       *testing.T
	store   *store.Store
	handler http.Handler
	token   string
}

func newScriptFixture(t *testing.T) *scriptFixture {
	t.Helper()
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
	admin, err := st.CreateUser(ctx, "script-admin-"+uuid.NewString(), "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := st.CreateSession(ctx, admin.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return &scriptFixture{
		t:       t,
		store:   st,
		handler: (&Server{cfg: &config.Config{APIRateLimit: 10000}, store: st}).Router(),
		token:   token,
	}
}

func (f *scriptFixture) do(method, target, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// audit returns the most recent entry with the given action, or nil.
func (f *scriptFixture) audit(action string) *models.AuditEntry {
	f.t.Helper()
	entries, err := f.store.ListAudit(context.Background(), 200)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := range entries {
		if entries[i].Action == action {
			return &entries[i]
		}
	}
	return nil
}
