package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/settings"
)

const resetTestPassword = "Original-Passw0rd!"

// resetUser creates a user with a real password and an address, and returns
// its id, username and a live session token.
func (e *smtpE2E) resetUser(email string) (id, username, session string) {
	e.t.Helper()
	hash, err := auth.HashPassword(resetTestPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	username = "reset-" + uuid.NewString()[:8]
	u, err := e.store.CreateUser(e.ctx, username, hash, models.RoleTech)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.store.UpdateUserEmail(e.ctx, u.ID, email); err != nil {
		e.t.Fatal(err)
	}
	session = auth.NewToken()
	if err := e.store.CreateSession(e.ctx, u.ID, auth.Hash(session), time.Now().Add(time.Hour)); err != nil {
		e.t.Fatal(err)
	}
	return u.ID, username, session
}

// issueReset creates a reset link directly, as the mail would carry it.
func (e *smtpE2E) issueReset(userID string) string {
	e.t.Helper()
	token := auth.NewToken()
	if err := e.store.CreatePasswordReset(e.ctx, userID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		e.t.Fatal(err)
	}
	return token
}

func (e *smtpE2E) login(username, password string) (int, map[string]any) {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, username, password), "")
}

func (e *smtpE2E) confirmReset(token, password string) (int, map[string]any) {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/auth/password-reset/confirm", fmt.Sprintf(`{"token":%q,"password":%q}`, token, password), "")
}

// The confirm step, end to end through the router.
func TestPasswordResetFlowIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	id, username, session := e.resetUser("flow@example.test")

	// Two-factor is on before the reset, and must still be required after it:
	// a reset replaces the password, not the second factor.
	cfg := &config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}
	enc, err := settings.Encrypt(cfg, auth.GenerateTOTPSecret())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetPendingUserTOTP(e.ctx, id, enc); err != nil {
		t.Fatal(err)
	}
	if err := e.store.EnablePendingUserTOTP(e.ctx, id, enc, nil); err != nil {
		t.Fatal(err)
	}

	token := e.issueReset(id)

	// A password that fails the rules leaves the link usable, as the error says.
	code, out := e.confirmReset(token, "weak")
	if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(out["error"]), "still valid") {
		t.Fatalf("weak password: status %d (%v), want 400 saying the link is still valid", code, out)
	}
	const newPassword = "Brand-New-Passw0rd!"
	if code, out := e.confirmReset(token, newPassword); code != http.StatusOK {
		t.Fatalf("confirm after a rejected password: status %d (%v), want 200 — the link was burned", code, out)
	}

	// Every session is dropped: whoever prompted the reset may hold one.
	if code, _ := e.do(http.MethodGet, "/api/me", "", session); code != http.StatusUnauthorized {
		t.Errorf("a session from before the reset still works: status %d", code)
	}
	// The link is single-use.
	if code, _ := e.confirmReset(token, "Another-Passw0rd!"); code != http.StatusBadRequest {
		t.Errorf("replayed link: status %d, want 400", code)
	}
	// The old password is gone; the new one still needs the second factor.
	if code, _ := e.login(username, resetTestPassword); code != http.StatusUnauthorized {
		t.Errorf("old password still signs in: status %d", code)
	}
	code, out = e.login(username, newPassword)
	if code != http.StatusUnauthorized || out["totp_required"] != true {
		t.Errorf("new password without a code: status %d (%v), want 401 totp_required", code, out)
	}
}

// A request for an existing account answers exactly like one for an unknown
// address; the difference shows only in what is sent.
func TestPasswordResetRequestHidesWhetherTheAccountExistsIntegration(t *testing.T) {
	relay := newDialRecorder(t)
	e := newSMTPE2E(t, withAccountSMTP("127.0.0.1", relay.port, "no-reply@account.test"))
	e.resetUser("exists@example.test")

	_, known := e.do(http.MethodPost, "/api/auth/password-reset", `{"email":"exists@example.test"}`, "")
	_, unknown := e.do(http.MethodPost, "/api/auth/password-reset", `{"email":"nobody@example.test"}`, "")
	if fmt.Sprint(known) != fmt.Sprint(unknown) {
		t.Fatalf("answers differ:\n%v\n%v", known, unknown)
	}
}

// Reset mail to one account is throttled whatever address the requests come
// from: the per-IP limit alone let anyone flood a mailbox through the
// operator's relay.
func TestPasswordResetMailIsThrottledPerAccountIntegration(t *testing.T) {
	relay := newDialRecorder(t)
	e := newSMTPE2E(t, withAccountSMTP("127.0.0.1", relay.port, "no-reply@account.test"))
	id, _, _ := e.resetUser("throttle@example.test")

	count := func() int {
		n, err := e.store.PasswordResetsSince(e.ctx, id, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	e.do(http.MethodPost, "/api/auth/password-reset", `{"email":"throttle@example.test"}`, "")
	deadline := time.Now().Add(10 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if count() != 1 {
		t.Fatalf("first request issued %d resets, want 1", count())
	}
	for i := 0; i < 3; i++ {
		e.do(http.MethodPost, "/api/auth/password-reset", `{"email":"throttle@example.test"}`, "")
	}
	time.Sleep(time.Second)
	if n := count(); n != 1 {
		t.Fatalf("requests inside the cooldown issued %d resets in total, want 1", n)
	}
}

// With single sign-on on, an account linked to it signs in only through the
// provider: no password sign-in, no reset mail, and a link issued before SSO
// was turned on cannot set a password either. Otherwise a password would step
// around the provider's MFA and its offboarding.
func TestSSOLinkedAccountsHaveNoPasswordPathIntegration(t *testing.T) {
	relay := newDialRecorder(t)
	e := newSMTPE2E(t, withAccountSMTP("127.0.0.1", relay.port, "no-reply@account.test"),
		func(c *config.Config) { c.OIDCEnabled = true })
	id, username, _ := e.resetUser("sso@example.test")
	oldLink := e.issueReset(id)
	if err := e.store.LinkOIDCIdentity(e.ctx, id, "https://idp.example.test", "subject-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	code, out := e.login(username, resetTestPassword)
	if code != http.StatusForbidden || out["code"] != "sso_required" {
		t.Errorf("password sign-in of an SSO account: status %d (%v), want 403 sso_required", code, out)
	}
	// A wrong password still reads as a wrong password: the SSO answer is only
	// for someone who already holds the right one.
	if code, _ := e.login(username, "Wrong-Passw0rd!"); code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", code)
	}

	if code, out := e.confirmReset(oldLink, "Brand-New-Passw0rd!"); code != http.StatusForbidden || out["code"] != "sso_required" {
		t.Errorf("pre-SSO reset link: status %d (%v), want 403 sso_required", code, out)
	}

	before, err := e.store.PasswordResetsSince(e.ctx, id, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e.do(http.MethodPost, "/api/auth/password-reset", `{"email":"sso@example.test"}`, "")
	time.Sleep(time.Second)
	after, err := e.store.PasswordResetsSince(e.ctx, id, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if after != before || relay.dialled() != 0 {
		t.Errorf("an SSO account was sent reset mail (resets %d→%d, dials %d)", before, after, relay.dialled())
	}
}

// Turning single sign-on off is the break-glass: linked accounts sign in with
// their passwords again.
func TestSSOLinkedAccountsSignInWithPasswordWhenSSOIsOffIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	id, username, _ := e.resetUser("glass@example.test")
	if err := e.store.LinkOIDCIdentity(e.ctx, id, "https://idp.example.test", "subject-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if code, out := e.login(username, resetTestPassword); code != http.StatusOK {
		t.Fatalf("with SSO off a linked account should sign in with its password: status %d (%v)", code, out)
	}
}
