package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// The SMTP endpoints, exercised through the real router with real session
// tokens. The unit tests cover the rules; this covers the wiring around them —
// which route reaches which profile, and which middleware actually guards it.
//
// The profile each handler writes is now bound at route registration. That makes
// a whole class of bug unrepresentable, but nothing yet checked that the binding
// itself is right: registering both routes with ProfileAccount would compile,
// pass every other test, and silently overwrite password-reset delivery whenever
// an operator saved the alerts profile. These tests assert it by effect.
type smtpE2E struct {
	t     *testing.T
	ctx   context.Context
	store *store.Store
	url   string
	admin string // bearer token for an unscoped admin
}

func newSMTPE2E(t *testing.T) *smtpE2E {
	t.Helper()
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st := newTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}
	// Only the store and config are reachable from these endpoints; Router()
	// registers handlers without dereferencing the other services.
	srv := New(cfg, st, hub.New(), nil, nil, nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	e := &smtpE2E{t: t, ctx: ctx, store: st, url: ts.URL}
	e.admin = e.session(models.RoleAdmin, nil)
	return e
}

// session creates a user and returns a live bearer token, so requests go through
// the authentication middleware instead of an injected context.
func (e *smtpE2E) session(role string, scopes []models.UserScope) string {
	e.t.Helper()
	u, err := e.store.CreateUserWithScopes(e.ctx, role+"-"+uuid.NewString(), "hash", role, scopes)
	if err != nil {
		e.t.Fatal(err)
	}
	token := auth.NewToken()
	if err := e.store.CreateSession(e.ctx, u.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		e.t.Fatal(err)
	}
	return token
}

func (e *smtpE2E) do(method, path, body, bearer string) (int, map[string]any) {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequestWithContext(e.ctx, method, e.url+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

const (
	accountBody = `{"host":"smtp.account.test","port":587,"username":"rmm@account.test",` +
		`"password":"account-secret","from":"no-reply@account.test","from_name":"OpenRMM","tls_mode":"starttls"}`
	alertsRelayBody = `{"host":"relay.alerts.test","port":2525,"username":"alerts@alerts.test",` +
		`"password":"alerts-secret","from":"alerts@alerts.test","tls_mode":"tls"}`
)

// The load-bearing assertion: each route writes its own profile and leaves the
// other alone.
func TestSMTPRoutesWriteTheirOwnProfileIntegration(t *testing.T) {
	e := newSMTPE2E(t)

	if code, _ := e.do(http.MethodPut, "/api/config/smtp", accountBody, e.admin); code != http.StatusOK {
		t.Fatalf("saving the account profile: status %d", code)
	}
	if code, _ := e.do(http.MethodPut, "/api/config/smtp/alerts", alertsRelayBody, e.admin); code != http.StatusOK {
		t.Fatalf("saving the alerts profile: status %d", code)
	}

	code, account := e.do(http.MethodGet, "/api/config/smtp", "", e.admin)
	if code != http.StatusOK {
		t.Fatalf("reading the account profile: status %d", code)
	}
	if account["host"] != "smtp.account.test" || account["from"] != "no-reply@account.test" {
		t.Fatalf("the alerts save reached the account profile: %v", account)
	}
	if account["password_set"] != true {
		t.Error("the account password should still be stored")
	}

	code, alerts := e.do(http.MethodGet, "/api/config/smtp/alerts", "", e.admin)
	if code != http.StatusOK {
		t.Fatalf("reading the alerts profile: status %d", code)
	}
	if alerts["host"] != "relay.alerts.test" {
		t.Fatalf("the alerts profile did not persist through its own route: %v", alerts)
	}
	if alerts["inherits_transport"] != false {
		t.Error("a profile with its own host does not inherit")
	}
}

// A blank host on the alerts route means "ride the account transport", and the
// response says what will actually be used — an operator reading only the stored
// fields would be left guessing what an empty host means.
func TestSMTPAlertsInheritanceIsVisibleIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	if code, _ := e.do(http.MethodPut, "/api/config/smtp", accountBody, e.admin); code != http.StatusOK {
		t.Fatalf("saving the account profile: status %d", code)
	}

	code, alerts := e.do(http.MethodPut, "/api/config/smtp/alerts",
		`{"from":"alerts@account.test","from_name":"OpenRMM Alerts"}`, e.admin)
	if code != http.StatusOK {
		t.Fatalf("saving the inherited alerts profile: status %d", code)
	}
	if alerts["inherits_transport"] != true {
		t.Errorf("a profile with no host of its own should report inheritance: %v", alerts)
	}
	effective, ok := alerts["effective"].(map[string]any)
	if !ok {
		t.Fatalf("the alerts view should carry the effective transport, got %v", alerts)
	}
	if effective["host"] != "smtp.account.test" {
		t.Errorf("effective host = %v, want the account relay", effective["host"])
	}
	if effective["from"] != "alerts@account.test" {
		t.Errorf("effective from = %v, want the alert sender", effective["from"])
	}
	if effective["enabled"] != true {
		t.Error("an inherited transport with a sender is deliverable")
	}
}

// The password is write-only on both routes: the console sets it and never reads
// it back, so a stolen session cannot use these endpoints to recover a mailbox
// credential.
func TestSMTPResponsesNeverCarryThePasswordIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	e.do(http.MethodPut, "/api/config/smtp", accountBody, e.admin)
	e.do(http.MethodPut, "/api/config/smtp/alerts", alertsRelayBody, e.admin)

	for _, path := range []string{"/api/config/smtp", "/api/config/smtp/alerts"} {
		_, view := e.do(http.MethodGet, path, "", e.admin)
		raw, _ := json.Marshal(view)
		for _, secret := range []string{"account-secret", "alerts-secret"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Errorf("%s leaked a stored password", path)
			}
		}
		if view["password_set"] != true {
			t.Errorf("%s should report that a password is stored", path)
		}
	}
}

// Every SMTP route carries settings.manage. A route registered without the
// middleware would otherwise be reachable by any signed-in technician.
func TestSMTPRoutesRequireSettingsManageIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	tech := e.session(models.RoleTech, nil)

	for _, tt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/config/smtp", ""},
		{http.MethodPut, "/api/config/smtp", accountBody},
		{http.MethodPost, "/api/config/smtp/test", `{"to":"x@example.test"}`},
		{http.MethodGet, "/api/config/smtp/alerts", ""},
		{http.MethodPut, "/api/config/smtp/alerts", alertsRelayBody},
		{http.MethodPost, "/api/config/smtp/alerts/test", `{"to":"x@example.test"}`},
	} {
		if code, _ := e.do(tt.method, tt.path, tt.body, tech); code != http.StatusForbidden {
			t.Errorf("%s %s as tech: status %d, want 403", tt.method, tt.path, code)
		}
	}
	// And the account transport is untouched by those attempts.
	if _, view := e.do(http.MethodGet, "/api/config/smtp", "", e.admin); view["host"] != "" {
		t.Errorf("a rejected request still wrote the transport: %v", view)
	}
}

// Mail configuration is instance-wide, so a user confined to one client or site
// must not edit it even holding settings.manage.
func TestSMTPWritesRejectScopedPrincipalsIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	client, err := e.store.CreateClient(e.ctx, "tenant-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	scoped := e.session(models.RoleAdmin, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})

	for _, tt := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/config/smtp", accountBody},
		{http.MethodPut, "/api/config/smtp/alerts", alertsRelayBody},
		{http.MethodPost, "/api/config/smtp/test", `{"to":"x@example.test"}`},
		{http.MethodPost, "/api/config/smtp/alerts/test", `{"to":"x@example.test"}`},
	} {
		if code, _ := e.do(tt.method, tt.path, tt.body, scoped); code != http.StatusForbidden {
			t.Errorf("%s %s as a scoped admin: status %d, want 403", tt.method, tt.path, code)
		}
	}
	if _, view := e.do(http.MethodGet, "/api/config/smtp", "", e.admin); view["host"] != "" {
		t.Errorf("a scoped principal wrote the transport: %v", view)
	}
}

// The audit distinguishes the two transports, so the log answers "who moved
// alert delivery" separately from "who moved password-reset delivery".
func TestSMTPAuditNamesTheProfileIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	e.do(http.MethodPut, "/api/config/smtp", accountBody, e.admin)
	e.do(http.MethodPut, "/api/config/smtp/alerts", alertsRelayBody, e.admin)

	rows, err := e.store.ListAudit(e.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range rows {
		seen[entry.Action] = true
		if bytes.Contains(entry.Detail, []byte("account-secret")) ||
			bytes.Contains(entry.Detail, []byte("alerts-secret")) {
			t.Errorf("audit detail for %s copied a password", entry.Action)
		}
	}
	for _, action := range []string{"config.smtp.update", "config.smtp.alerts.update"} {
		if !seen[action] {
			t.Errorf("missing audit action %q; saw %v", action, seen)
		}
	}
}

// Rejected at save time rather than discovered later as a stuck outbox — and
// rejected per profile, so a bad alerts profile cannot be stored either.
func TestSMTPValidationRejectsBadConfigIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	for _, tt := range []struct {
		name, path, body string
	}{
		{"invalid sender", "/api/config/smtp", `{"host":"h.test","port":587,"from":"not-an-address","tls_mode":"starttls"}`},
		{"credentials over cleartext", "/api/config/smtp", `{"host":"h.test","port":25,"from":"a@b.test","password":"p","tls_mode":"none"}`},
		{"unknown tls mode", "/api/config/smtp/alerts", `{"host":"h.test","port":587,"from":"a@b.test","tls_mode":"quantum"}`},
		{"alerts credentials over cleartext", "/api/config/smtp/alerts", `{"host":"h.test","port":25,"from":"a@b.test","password":"p","tls_mode":"none"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if code, _ := e.do(http.MethodPut, tt.path, tt.body, e.admin); code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", code)
			}
		})
	}
	// Nothing was stored by any of them.
	if _, view := e.do(http.MethodGet, "/api/config/smtp", "", e.admin); view["host"] != "" {
		t.Errorf("a rejected save still wrote the account transport: %v", view)
	}
	if _, view := e.do(http.MethodGet, "/api/config/smtp/alerts", "", e.admin); view["host"] != "" {
		t.Errorf("a rejected save still wrote the alerts transport: %v", view)
	}
}

// A test send with no transport must fail as a configuration error the operator
// can act on, not as a hang or a 500.
func TestSMTPTestWithoutTransportIsRejectedIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	for _, path := range []string{"/api/config/smtp/test", "/api/config/smtp/alerts/test"} {
		code, body := e.do(http.MethodPost, path, `{"to":"someone@example.test"}`, e.admin)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %v)", path, code, body)
		}
	}
}

// The permission that guards a user's address is wired to the route, not merely
// defined. Dropping the middleware would hand every user manager the ability to
// redirect a password reset.
func TestUserEmailRouteRequiresEmailPermissionIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	target, err := e.store.CreateUser(e.ctx, "target-"+uuid.NewString(), "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}

	// A custom role holding users.manage but not users.email.manage.
	roleKey := "usermgr" + uuid.NewString()[:8]
	if _, err := e.store.CreateRole(e.ctx, &models.Role{
		Key: roleKey, Name: "User manager",
		Permissions: []string{models.PermUsersManage, models.PermDevicesView},
	}); err != nil {
		t.Fatal(err)
	}
	manager := e.session(roleKey, nil)

	code, _ := e.do(http.MethodPut, "/api/users/"+target.ID+"/email", `{"email":"attacker@example.test"}`, manager)
	if code != http.StatusForbidden {
		t.Fatalf("users.manage alone reached the address route: status %d", code)
	}
	got, err := e.store.GetUserByID(e.ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "" {
		t.Fatalf("the address was written despite the 403: %q", got.Email)
	}

	// An admin holds both and succeeds.
	if code, _ := e.do(http.MethodPut, "/api/users/"+target.ID+"/email", `{"email":"tech@example.test"}`, e.admin); code != http.StatusOK {
		t.Fatalf("admin should be able to set the address, status %d", code)
	}
	if got, _ = e.store.GetUserByID(e.ctx, target.ID); got.Email != "tech@example.test" {
		t.Errorf("address = %q, want it set", got.Email)
	}
}

// Choosing which events you receive stays self-service; moving the address does
// not, because it is where a password reset is delivered.
func TestMyNotificationsRejectsAddressChangeIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	tech := e.session(models.RoleTech, nil)

	code, body := e.do(http.MethodPut, "/api/me/notifications",
		`{"subscriptions":{"job_failed":true}}`, tech)
	if code != http.StatusOK {
		t.Fatalf("subscriptions should be self-service: status %d (%v)", code, body)
	}
	subs, _ := body["subscriptions"].([]any)
	if len(subs) == 0 {
		t.Error("the response should carry the merged preferences")
	}

	if code, _ := e.do(http.MethodPut, "/api/me/notifications",
		`{"email":"attacker@example.test","subscriptions":{}}`, tech); code != http.StatusForbidden {
		t.Errorf("changing one's own address: status %d, want 403", code)
	}
}

// settings.ProfileAlerts is what the alerts routes are bound to; if that binding
// were lost the two would share storage, which the profile-isolation test above
// would catch. This pins the constant itself against an accidental edit.
func TestAlertsProfileConstantIsNested(t *testing.T) {
	if settings.ProfileAlerts == settings.ProfileAccount {
		t.Fatal("the profiles must not share a key prefix")
	}
}

// The outbox gauges are the signal that a stopped queue is detectable from
// outside. A typo in a gauge name breaks a dashboard silently, so the names are
// asserted here rather than trusted.
func TestMetricsExposeTheOutboxIntegration(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st := newTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		SettingsEncryptionKey: "0123456789abcdef0123456789abcdef",
		MetricsToken:          "metrics-token",
	}
	ts := httptest.NewServer(New(cfg, st, hub.New(), nil, nil, nil, nil, nil, nil).Router())
	defer ts.Close()

	// One queued message, so pending and the age gauge are non-trivial.
	if err := st.EnqueueNotification(ctx, &models.Notification{
		EventType: store.NotifyAgentOffline, Recipient: "someone@example.test",
		Subject: "s", Body: "b",
	}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer metrics-token")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	body := string(raw)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body %s", res.StatusCode, body)
	}
	for _, gauge := range []string{
		"rmm_notifications_pending",
		"rmm_notifications_failed",
		"rmm_notifications_oldest_pending_seconds",
	} {
		if !strings.Contains(body, "# TYPE "+gauge+" gauge") {
			t.Errorf("missing gauge %q in:\n%s", gauge, body)
		}
	}
	if !strings.Contains(body, "rmm_notifications_pending 1") {
		t.Errorf("pending gauge should count the queued message:\n%s", body)
	}
}

// Password resets leave through the account transport, never the alerts one.
// Nothing else pins this: pointing deliverPasswordReset at AlertSMTPConfig would
// compile and pass every other test, and reset links would start going out
// through a relay chosen for monitoring mail.
func TestPasswordResetUsesTheAccountTransportIntegration(t *testing.T) {
	e := newSMTPE2E(t)

	// Two listeners that accept and hang up. Which port is dialled identifies
	// the transport; the send fails either way, which is fine — the question is
	// only which relay was chosen.
	accountRelay := newDialRecorder(t)
	alertsRelay := newDialRecorder(t)

	save := func(path string, port int, from string) {
		body := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"from":%q,"tls_mode":"none"}`, port, from)
		if code, out := e.do(http.MethodPut, path, body, e.admin); code != http.StatusOK {
			t.Fatalf("saving %s: status %d (%v)", path, code, out)
		}
	}
	save("/api/config/smtp", accountRelay.port, "no-reply@account.test")
	save("/api/config/smtp/alerts", alertsRelay.port, "alerts@alerts.test")

	target, err := e.store.CreateUser(e.ctx, "reset-"+uuid.NewString(), "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.UpdateUserEmail(e.ctx, target.ID, "reset-target@example.test"); err != nil {
		t.Fatal(err)
	}

	// The endpoint answers before doing any work — that is its enumeration
	// defence — so the delivery attempt happens in the background.
	code, _ := e.do(http.MethodPost, "/api/auth/password-reset", `{"email":"reset-target@example.test"}`, "")
	if code != http.StatusAccepted {
		t.Fatalf("password reset request: status %d, want 202", code)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && accountRelay.dialled() == 0 && alertsRelay.dialled() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if accountRelay.dialled() == 0 {
		t.Error("the reset was not sent through the account transport")
	}
	if alertsRelay.dialled() != 0 {
		t.Errorf("the reset went through the alerts relay (%d dials)", alertsRelay.dialled())
	}
}

// dialRecorder accepts TCP connections and immediately closes them, recording
// how many arrived. Its port is its identity.
type dialRecorder struct {
	port  int
	mu    sync.Mutex
	conns int
}

func newDialRecorder(t *testing.T) *dialRecorder {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &dialRecorder{port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			d.mu.Lock()
			d.conns++
			d.mu.Unlock()
			_ = conn.Close()
		}
	}()
	return d
}

func (d *dialRecorder) dialled() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns
}
