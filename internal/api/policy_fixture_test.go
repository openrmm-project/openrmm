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

// policyAPIFixture is a router plus a signed-in admin. Router-level rather than
// calling handlers directly, because the permission and scope middleware is
// where a policy request is accepted or refused.
type policyAPIFixture struct {
	t       *testing.T
	store   *store.Store
	handler http.Handler
	admin   *models.User
	token   string
}

func newPolicyAPIFixture(t *testing.T) *policyAPIFixture {
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
	admin, err := st.CreateUser(ctx, "policy-admin-"+uuid.NewString(), "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := st.CreateSession(ctx, admin.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return &policyAPIFixture{
		t:       t,
		store:   st,
		handler: (&Server{cfg: &config.Config{APIRateLimit: 10000}, store: st}).Router(),
		admin:   admin,
		token:   token,
	}
}

func (f *policyAPIFixture) do(method, target, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *policyAPIFixture) policies() []models.Policy {
	f.t.Helper()
	list, err := f.store.ListPolicies(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

// audit returns the most recent entry with the given action, or nil.
func (f *policyAPIFixture) audit(action string) *models.AuditEntry {
	f.t.Helper()
	entries, _, err := f.store.ListAudit(context.Background(), store.AuditFilter{Limit: 200})
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
