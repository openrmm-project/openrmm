package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// A managed service marks its owner's account as signing in through single
// sign-on only as soon as it creates it. Before the first single sign-on links
// it, the account must already have no password path — no password sign-in, no
// "Forgot password" mail — and the login screen's first step must send it to
// the provider. An unmarked account, and a name nobody holds, still get the
// password field.
func TestSSORequiredAccountBeforeFirstSignInIntegration(t *testing.T) {
	relay := newDialRecorder(t)
	f := newSSOFlow(t, withAccountSMTP("127.0.0.1", relay.port, "no-reply@account.test"))
	e := f.smtpE2E
	ownerID, owner, ownerSession := e.resetUser("owner@example.test")
	_, other, techSession := e.resetUser("tech@example.test")

	method := func(username string) string {
		t.Helper()
		code, out := e.do(http.MethodPost, "/api/auth/method", fmt.Sprintf(`{"username":%q}`, username), "")
		if code != http.StatusOK {
			t.Fatalf("auth method for %q: status %d (%v)", username, code, out)
		}
		m, _ := out["method"].(string)
		return m
	}
	mark := func(required bool, bearer string) int {
		t.Helper()
		code, _ := e.do(http.MethodPut, "/api/users/"+ownerID+"/sso", fmt.Sprintf(`{"required":%t}`, required), bearer)
		return code
	}

	if got := method(owner); got != "password" {
		t.Fatalf("before the mark: %q, want password", got)
	}
	if code := mark(true, techSession); code != http.StatusForbidden {
		t.Fatalf("a technician marked an account: status %d, want 403", code)
	}
	if code, _ := e.do(http.MethodGet, "/api/me", "", ownerSession); code != http.StatusOK {
		t.Fatalf("the owner's password session before the mark: status %d", code)
	}
	if code := mark(true, e.admin); code != http.StatusOK {
		t.Fatalf("marking: status %d", code)
	}
	// A session opened with a password before the mark ends with it, as a
	// disable ends one; otherwise it would outlive the mark by up to a week.
	if code, _ := e.do(http.MethodGet, "/api/me", "", ownerSession); code != http.StatusUnauthorized {
		t.Errorf("the owner's password session after the mark: status %d, want 401", code)
	}

	if got := method(owner); got != "sso" {
		t.Errorf("a marked account: %q, want sso", got)
	}
	for _, name := range []string{other, "nobody-" + owner} {
		if got := method(name); got != "password" {
			t.Errorf("%q: %q, want password", name, got)
		}
	}

	if code, out := e.login(owner, resetTestPassword); code != http.StatusForbidden || out["code"] != "sso_required" {
		t.Errorf("password sign-in of a marked account: status %d (%v), want 403 sso_required", code, out)
	}
	if code, _ := e.login(owner, "Wrong-Passw0rd!"); code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", code)
	}

	before, err := e.store.PasswordResetsSince(e.ctx, ownerID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e.do(http.MethodPost, "/api/auth/password-reset", `{"email":"owner@example.test"}`, "")
	time.Sleep(time.Second)
	after, err := e.store.PasswordResetsSince(e.ctx, ownerID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if after != before || relay.dialled() != 0 {
		t.Errorf("a marked account was sent reset mail (resets %d→%d, dials %d)", before, after, relay.dialled())
	}

	// The users screen shows it.
	users, err := e.store.ListUsers(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if (u.ID == ownerID) != u.SSO {
			t.Errorf("user %s listed with sso=%v", u.Username, u.SSO)
		}
	}

	// Cleared, the account has its password back; the change is audited both ways.
	if code := mark(false, e.admin); code != http.StatusOK {
		t.Fatalf("clearing: status %d", code)
	}
	if code, out := e.login(owner, resetTestPassword); code != http.StatusOK {
		t.Errorf("after clearing: status %d (%v), want 200", code, out)
	}
	if got := method(owner); got != "password" {
		t.Errorf("after clearing: %q, want password", got)
	}
}

// With single sign-on off the mark does nothing: the account signs in with its
// password, and the first step never answers "sso" — that is the break-glass.
func TestSSORequiredMarkIsInertWithSSOOffIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	id, username, _ := e.resetUser("glass-owner@example.test")
	if err := e.store.SetUserSSORequired(e.ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if code, out := e.do(http.MethodPost, "/api/auth/method", fmt.Sprintf(`{"username":%q}`, username), ""); code != http.StatusOK || out["method"] != "password" {
		t.Errorf("auth method with SSO off: status %d (%v), want password", code, out)
	}
	if code, out := e.login(username, resetTestPassword); code != http.StatusOK {
		t.Errorf("with SSO off a marked account should sign in with its password: status %d (%v)", code, out)
	}
}
