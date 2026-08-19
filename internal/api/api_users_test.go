package api

import (
	"context"
	"encoding/json"
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

// usersFixture is a router plus two signed-in operators: a full admin doing the
// managing, and a target account to manage. Router-level rather than handler-level
// because the guards being tested (self-disable, the built-in admin, session
// termination) only line up once auth middleware has resolved the actor.
type usersFixture struct {
	t       *testing.T
	store   *store.Store
	handler http.Handler
	admin   *models.User
	target  *models.User
	token   string // the admin's session token
}

func newUsersFixture(t *testing.T) *usersFixture {
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

	suffix := uuid.NewString()
	admin, err := st.CreateUser(ctx, "manager-"+suffix, "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateUser(ctx, "target-"+suffix, "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := st.CreateSession(ctx, admin.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return &usersFixture{
		t:       t,
		store:   st,
		handler: (&Server{cfg: &config.Config{APIRateLimit: 10000}, store: st}).Router(),
		admin:   admin,
		target:  target,
		token:   token,
	}
}

func (f *usersFixture) do(method, target, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *usersFixture) user(id string) *models.User {
	f.t.Helper()
	u, err := f.store.GetUserByID(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *usersFixture) scopes(userID string) []models.UserScope {
	f.t.Helper()
	scopes, err := f.store.ListUserScopes(context.Background(), userID)
	if err != nil {
		f.t.Fatal(err)
	}
	return scopes
}

// audit returns the most recent entry with the given action, or nil.
func (f *usersFixture) audit(action string) *models.AuditEntry {
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

// The console's Disable button is the whole point of the endpoint, so the round
// trip is asserted end to end: the flag lands, live sessions die, the audit
// records who did it, and Enable puts it back.
func TestDisableAndEnableUserEndpointIntegration(t *testing.T) {
	f := newUsersFixture(t)
	ctx := context.Background()

	targetToken := auth.NewToken()
	if err := f.store.CreateSession(ctx, f.target.ID, auth.Hash(targetToken), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	rec := f.do(http.MethodPost, "/api/users/"+f.target.ID+"/disable", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var disabled models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.DisabledAt == nil {
		t.Fatal("disable response did not carry disabled_at")
	}
	if f.user(f.target.ID).DisabledAt == nil {
		t.Fatal("disabled_at was not persisted")
	}

	// A disable that leaves an open tab working is not a disable.
	if _, _, err := f.store.SessionPrincipal(ctx, auth.Hash(targetToken)); err == nil {
		t.Error("the disabled user's session still authenticates")
	}

	entry := f.audit("user.disable")
	if entry == nil {
		t.Fatal("no user.disable audit entry")
	}
	if entry.Actor != f.admin.ID || entry.Target != f.target.ID {
		t.Errorf("audit actor/target = %q/%q, want %q/%q", entry.Actor, entry.Target, f.admin.ID, f.target.ID)
	}

	// Re-disabling keeps the original stamp: a second click must not rewrite when
	// access was withdrawn, which is the question an incident review asks.
	first := *f.user(f.target.ID).DisabledAt
	if rec := f.do(http.MethodPost, "/api/users/"+f.target.ID+"/disable", ""); rec.Code != http.StatusOK {
		t.Fatalf("second disable status = %d", rec.Code)
	}
	if again := *f.user(f.target.ID).DisabledAt; !again.Equal(first) {
		t.Errorf("re-disabling moved disabled_at from %s to %s", first, again)
	}

	if rec := f.do(http.MethodPost, "/api/users/"+f.target.ID+"/enable", ""); rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if f.user(f.target.ID).DisabledAt != nil {
		t.Error("enable did not clear disabled_at")
	}
	if f.audit("user.enable") == nil {
		t.Error("no user.enable audit entry")
	}
}

// Locking yourself out is never intended, and the built-in admin is the account
// that recovers the instance.
func TestDisableUserRefusesSelfAndBuiltinAdminIntegration(t *testing.T) {
	f := newUsersFixture(t)
	ctx := context.Background()

	rec := f.do(http.MethodPost, "/api/users/"+f.admin.ID+"/disable", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-disable status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if f.user(f.admin.ID).DisabledAt != nil {
		t.Fatal("the acting admin disabled themselves")
	}

	builtin, err := f.store.CreateUser(ctx, "admin", "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodPost, "/api/users/"+builtin.ID+"/disable", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("built-in admin disable status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if f.user(builtin.ID).DisabledAt != nil {
		t.Fatal("the built-in admin account was disabled")
	}

	// Enable carries no such guard — it only ever restores access.
	if rec := f.do(http.MethodPost, "/api/users/"+builtin.ID+"/enable", ""); rec.Code != http.StatusOK {
		t.Errorf("enable on the built-in admin status = %d, want 200", rec.Code)
	}
}

// A scope change is the only user mutation that used to leave no trace, and the
// widest one it can make — full fleet access — is an empty list, which is also
// what an interrupted "restrict them to…" produces. The audit has to name it.
func TestSetUserScopesAuditsTheChangeIntegration(t *testing.T) {
	f := newUsersFixture(t)
	ctx := context.Background()

	client, err := f.store.CreateClient(ctx, "acme-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	body := `{"scopes":[{"scope_type":"client","scope_id":"` + client.ID + `"}]}`
	if rec := f.do(http.MethodPut, "/api/users/"+f.target.ID+"/scopes", body); rec.Code != http.StatusOK {
		t.Fatalf("set scopes status = %d, body=%s", rec.Code, rec.Body.String())
	}
	scopes, err := f.store.ListUserScopes(ctx, f.target.ID)
	if err != nil || len(scopes) != 1 || scopes[0].ScopeID != client.ID {
		t.Fatalf("scopes = %#v, err=%v, want the one client scope", scopes, err)
	}
	entry := f.audit("user.scopes.update")
	if entry == nil {
		t.Fatal("no user.scopes.update audit entry")
	}
	var raw map[string]any
	if err := json.Unmarshal(entry.Detail, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["username"] != f.target.Username {
		t.Errorf("audit username = %v, want %q", raw["username"], f.target.Username)
	}
	if raw["from"] != float64(0) || raw["to"] != float64(1) {
		t.Errorf("audit from/to = %v/%v, want 0/1", raw["from"], raw["to"])
	}
	if raw["full_access"] != false {
		t.Errorf("a one-scope grant was audited as full_access=%v, want false", raw["full_access"])
	}

	// Emptying the list is a widening, not a no-op, and is what the console now
	// refuses to send by accident. When it does arrive it must be legible.
	if rec := f.do(http.MethodPut, "/api/users/"+f.target.ID+"/scopes", `{"scopes":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("clearing scopes status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if scopes, err := f.store.ListUserScopes(ctx, f.target.ID); err != nil || len(scopes) != 0 {
		t.Fatalf("scopes after clearing = %#v, err=%v", scopes, err)
	}
	entry = f.audit("user.scopes.update")
	if err := json.Unmarshal(entry.Detail, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["full_access"] != true {
		t.Errorf("clearing every scope was audited as full_access=%v, want true", raw["full_access"])
	}
}

// The journey that was impossible: a user scoped to a device, the device is
// deleted, and the operator goes to give them a different one. The stale row
// used to survive the deletion, and since the console loads the stored scopes
// and sends them back, every save for that account failed validation on a row
// the operator could neither see nor uncheck.
func TestScopeSurvivesDeletingItsTargetDeviceIntegration(t *testing.T) {
	f := newUsersFixture(t)
	ctx := context.Background()

	client, err := f.store.CreateClient(ctx, "acme-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	site, err := f.store.CreateSite(ctx, client.ID, "site-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := f.store.CreateAgent(ctx, "doomed", "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := f.store.CreateAgent(ctx, "replacement", "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	seed := `{"scopes":[{"scope_type":"agent","scope_id":"` + doomed.ID + `"},{"scope_type":"client","scope_id":"` + client.ID + `"}]}`
	if rec := f.do(http.MethodPut, "/api/users/"+f.target.ID+"/scopes", seed); rec.Code != http.StatusOK {
		t.Fatalf("seeding scopes failed: %s", rec.Body.String())
	}
	if err := f.store.DeleteAgent(ctx, doomed.ID); err != nil {
		t.Fatal(err)
	}

	// What the console reloads must no longer name the deleted device...
	rec := f.do(http.MethodGet, "/api/users/"+f.target.ID+"/scopes", "")
	if strings.Contains(rec.Body.String(), doomed.ID) {
		t.Fatalf("the scope editor would still load the deleted device: %s", rec.Body.String())
	}
	// ...and sending back what it loaded, plus the new device, has to succeed.
	update := `{"scopes":[{"scope_type":"client","scope_id":"` + client.ID + `"},{"scope_type":"agent","scope_id":"` + replacement.ID + `"}]}`
	if rec := f.do(http.MethodPut, "/api/users/"+f.target.ID+"/scopes", update); rec.Code != http.StatusOK {
		t.Fatalf("re-scoping after the deletion failed with %d: %s", rec.Code, rec.Body.String())
	}
	if got := len(f.scopes(f.target.ID)); got != 2 {
		t.Errorf("stored scopes = %d, want 2", got)
	}
}

// A scope that does not resolve must fail the whole request rather than land the
// user somewhere narrower — or wider — than the operator asked for.
func TestSetUserScopesRejectsUnknownScopeIntegration(t *testing.T) {
	f := newUsersFixture(t)
	ctx := context.Background()

	client, err := f.store.CreateClient(ctx, "acme-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	seed := `{"scopes":[{"scope_type":"client","scope_id":"` + client.ID + `"}]}`
	if rec := f.do(http.MethodPut, "/api/users/"+f.target.ID+"/scopes", seed); rec.Code != http.StatusOK {
		t.Fatalf("seeding scopes failed: %s", rec.Body.String())
	}

	// The message has to name the offending entry: a request carries several
	// scopes, and the one that fails is usually a stale reference the console
	// sent back without the operator knowing it was there.
	missing := uuid.NewString()
	rec := f.do(http.MethodPut, "/api/users/"+f.target.ID+"/scopes",
		`{"scopes":[{"scope_type":"client","scope_id":"`+client.ID+`"},{"scope_type":"agent","scope_id":"`+missing+`"}]}`)
	if !strings.Contains(rec.Body.String(), missing) || !strings.Contains(rec.Body.String(), "agent") {
		t.Errorf("error did not name the failing scope: %s", rec.Body.String())
	}

	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"unknown id", `{"scopes":[{"scope_type":"client","scope_id":"` + uuid.NewString() + `"}]}`, http.StatusNotFound},
		{"unknown type", `{"scopes":[{"scope_type":"planet","scope_id":"` + client.ID + `"}]}`, http.StatusBadRequest},
		{"blank reference", `{"scopes":[{"scope_type":"","scope_id":""}]}`, http.StatusBadRequest},
		// Silently dropped by the store's empty-field skip, so a partly blank list
		// would otherwise write a narrower scope set than was sent.
		{"one good one blank", `{"scopes":[{"scope_type":"client","scope_id":"` + client.ID + `"},{"scope_type":"site","scope_id":""}]}`, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(http.MethodPut, "/api/users/"+f.target.ID+"/scopes", tt.body)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tt.want, rec.Body.String())
			}
			scopes, err := f.store.ListUserScopes(ctx, f.target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(scopes) != 1 || scopes[0].ScopeID != client.ID {
				t.Errorf("a rejected update changed the stored scopes to %#v", scopes)
			}
		})
	}
}
