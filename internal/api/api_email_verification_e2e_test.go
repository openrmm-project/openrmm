package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify/smtptest"
)

// withAlertService points the instance at a shared alert service on relay.
func withAlertService(relay *smtptest.Server) func(*config.Config) {
	return func(c *config.Config) {
		c.PublicURL = "https://acme.openrmm.test"
		c.AlertsSMTPHost, c.AlertsSMTPPort, c.AlertsSMTPTLSMode = "127.0.0.1", relay.Port, "none"
		c.AlertsSMTPFrom, c.AlertsSMTPFromName, c.AlertsHourlyLimit = "alerts@notify.openrmm.test", "OpenRMM Alerts · acme", 50
	}
}

var verifyLinkRE = regexp.MustCompile(`verify-email=([A-Za-z0-9_-]+)`)

// waitForMail waits for the relay to hold n messages and returns them.
func waitForMail(t *testing.T, relay *smtptest.Server, n int) []smtptest.Message {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for len(relay.Messages()) < n && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	msgs := relay.Messages()
	if len(msgs) < n {
		t.Fatalf("%d message(s) arrived, want %d", len(msgs), n)
	}
	return msgs
}

// sessionFor opens a session for an existing user.
func (e *smtpE2E) sessionFor(userID string) string {
	e.t.Helper()
	token := auth.NewToken()
	if err := e.store.CreateSession(e.ctx, userID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		e.t.Fatal(err)
	}
	return token
}

// Setting an address asks its owner to confirm it, through the shared service;
// the link confirms it once; a changed address needs a new confirmation.
func TestAlertRecipientConfirmationFlowIntegration(t *testing.T) {
	relay := smtptest.New(t)
	e := newSMTPE2E(t, withAlertService(relay))
	u, err := e.store.CreateUser(e.ctx, "tech-"+uuid.NewString()[:8], "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}

	if code, out := e.do(http.MethodPut, "/api/users/"+u.ID+"/email", `{"email":"ana@example.test"}`, e.admin); code != http.StatusOK {
		t.Fatalf("set email: %d %v", code, out)
	}
	msgs := waitForMail(t, relay, 1)
	if msgs[0].To[0] != "ana@example.test" || msgs[0].From != "alerts@notify.openrmm.test" {
		t.Fatalf("confirmation went %v from %s", msgs[0].To, msgs[0].From)
	}
	m := verifyLinkRE.FindStringSubmatch(msgs[0].Data)
	if m == nil || !strings.Contains(msgs[0].Data, "https://acme.openrmm.test/?verify-email=") {
		t.Fatalf("no confirmation link in %q", msgs[0].Data)
	}

	// Not confirmed yet: the user's settings say so.
	session := e.sessionFor(u.ID)
	_, prefs := e.do(http.MethodGet, "/api/me/notifications", "", session)
	if prefs["verification_required"] != true || prefs["email_verified"] != false {
		t.Errorf("before confirming: %v", prefs)
	}

	if code, out := e.do(http.MethodPost, "/api/auth/verify-email", fmt.Sprintf(`{"token":%q}`, m[1]), ""); code != http.StatusOK {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if code, _ := e.do(http.MethodPost, "/api/auth/verify-email", fmt.Sprintf(`{"token":%q}`, m[1]), ""); code != http.StatusBadRequest {
		t.Errorf("a replayed link: %d, want 400", code)
	}
	_, prefs = e.do(http.MethodGet, "/api/me/notifications", "", session)
	if prefs["email_verified"] != true {
		t.Errorf("after confirming: %v", prefs)
	}
	if e.auditCount("user.email.verify", u.ID) != 1 || e.auditCount("user.email.verify_sent", u.ID) != 1 {
		t.Error("sending and confirming are not both on the audit trail")
	}

	// A corrected address starts unconfirmed and gets its own link at once —
	// the throttle is per address, so fixing a typo is not held up.
	e.do(http.MethodPut, "/api/users/"+u.ID+"/email", `{"email":"ana.new@example.test"}`, e.admin)
	_, prefs = e.do(http.MethodGet, "/api/me/notifications", "", session)
	if prefs["email_verified"] != false {
		t.Error("a changed address kept the confirmation")
	}
	if msgs := waitForMail(t, relay, 2); msgs[1].To[0] != "ana.new@example.test" {
		t.Errorf("the corrected address got no link: %+v", msgs[1].To)
	}
}

// Asking again is throttled per account, and an administrator may ask for a
// user. Without a shared service there is nothing to confirm.
func TestAlertRecipientConfirmationRequestsIntegration(t *testing.T) {
	relay := smtptest.New(t)
	e := newSMTPE2E(t, withAlertService(relay))
	u, err := e.store.CreateUser(e.ctx, "tech-"+uuid.NewString()[:8], "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.UpdateUserEmail(e.ctx, u.ID, "bob@example.test"); err != nil {
		t.Fatal(err)
	}
	session := e.sessionFor(u.ID)
	if code, _ := e.do(http.MethodPost, "/api/me/email/verify", "", session); code != http.StatusAccepted {
		t.Fatalf("first request: %d", code)
	}
	if code, _ := e.do(http.MethodPost, "/api/me/email/verify", "", session); code != http.StatusTooManyRequests {
		t.Errorf("a second request inside the cooldown: %d, want 429", code)
	}
	if code, _ := e.do(http.MethodPost, "/api/users/"+u.ID+"/email/verify", "", e.admin); code != http.StatusTooManyRequests {
		t.Errorf("an administrator's request shares the throttle: %d, want 429", code)
	}
	// A technician may not ask on someone else's behalf.
	other := e.sessionFor(u.ID)
	if code, _ := e.do(http.MethodPost, "/api/users/"+u.ID+"/email/verify", "", other); code != http.StatusForbidden {
		t.Errorf("a technician reached the admin route: %d", code)
	}

	plain := newSMTPE2E(t)
	pu, err := plain.store.CreateUser(plain.ctx, "tech-"+uuid.NewString()[:8], "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.store.UpdateUserEmail(plain.ctx, pu.ID, "carol@example.test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := plain.do(http.MethodPost, "/api/me/email/verify", "", plain.sessionFor(pu.ID)); code != http.StatusConflict {
		t.Errorf("without a shared service: %d, want 409", code)
	}
}

// The email screen shows the shared service with its fixed sender, and its test
// button mails only the caller's own confirmed address.
func TestAlertServiceOnTheEmailScreenIntegration(t *testing.T) {
	relay := smtptest.New(t)
	e := newSMTPE2E(t, withAlertService(relay))
	_, view := e.do(http.MethodGet, "/api/config/smtp/alerts", "", e.admin)
	service, _ := view["service"].(map[string]any)
	if view["enabled"] != false || service["enabled"] != true || service["from"] != "alerts@notify.openrmm.test" {
		t.Fatalf("alerts view: %v", view)
	}

	admin, err := e.store.CreateUser(e.ctx, "admin-"+uuid.NewString()[:8], "hash", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.UpdateUserEmail(e.ctx, admin.ID, "me@example.test"); err != nil {
		t.Fatal(err)
	}
	session := e.sessionFor(admin.ID)
	if code, _ := e.do(http.MethodPost, "/api/config/smtp/alerts/test", `{"to":"stranger@example.test"}`, session); code != http.StatusForbidden {
		t.Errorf("service test to someone else: %d, want 403", code)
	}
	if code, _ := e.do(http.MethodPost, "/api/config/smtp/alerts/test", `{}`, session); code != http.StatusForbidden {
		t.Errorf("service test to an unconfirmed own address: %d, want 403", code)
	}
	if err := e.store.CreateEmailVerification(e.ctx, admin.ID, "me@example.test", "h-"+admin.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.ConfirmEmailVerification(e.ctx, "h-"+admin.ID); err != nil {
		t.Fatal(err)
	}
	if code, out := e.do(http.MethodPost, "/api/config/smtp/alerts/test", `{}`, session); code != http.StatusOK {
		t.Fatalf("service test to the own confirmed address: %d %v", code, out)
	}
	if msgs := relay.Messages(); len(msgs) != 1 || msgs[0].To[0] != "me@example.test" {
		t.Errorf("test mail: %+v", msgs)
	}
}
