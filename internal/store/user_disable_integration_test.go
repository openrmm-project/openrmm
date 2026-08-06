package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
)

// Disabling withdraws access. Every path that grants it is filtered in SQL
// rather than by a caller remembering to check, because a disable that leaves
// one door open is not a disable.
func TestDisabledUserCannotAuthenticateIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	name := "disable-" + suffix
	hash, err := auth.HashPassword("Correct horse battery staple!")
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, name, hash, models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, u.ID, "disable-"+suffix+"@example.test"); err != nil {
		t.Fatal(err)
	}

	sessionToken := auth.NewToken()
	if err := st.CreateSession(ctx, u.ID, auth.Hash(sessionToken), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	apiToken := auth.NewToken()
	boundUser := u.ID
	tokenID, err := st.CreateAPIToken(ctx, auth.Hash(apiToken), "bound-"+suffix, models.RoleTech, &boundUser, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteAPIToken(context.Background(), tokenID) })

	// Precondition: everything works while the account is active.
	if _, _, _, err := st.GetUserAuth(ctx, name); err != nil {
		t.Fatalf("login lookup before disabling: %v", err)
	}
	if _, _, err := st.SessionPrincipal(ctx, auth.Hash(sessionToken)); err != nil {
		t.Fatalf("session before disabling: %v", err)
	}
	if _, _, ok, err := st.APITokenRole(ctx, auth.Hash(apiToken)); err != nil || !ok {
		t.Fatalf("api token before disabling: ok=%t err=%v", ok, err)
	}
	if _, _, err := st.UserByEmail(ctx, "disable-"+suffix+"@example.test"); err != nil {
		t.Fatalf("password-reset lookup before disabling: %v", err)
	}

	disabled, err := st.SetUserDisabled(ctx, u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.DisabledAt == nil {
		t.Fatal("SetUserDisabled did not record when access was withdrawn")
	}
	firstStamp := *disabled.DisabledAt

	// Password login is refused.
	if _, _, _, err := st.GetUserAuth(ctx, name); err == nil {
		t.Error("a disabled user must not be able to sign in")
	}
	// A live session stops working, before it expires and whether or not the
	// rows were deleted.
	if _, _, err := st.SessionPrincipal(ctx, auth.Hash(sessionToken)); err == nil {
		t.Error("an existing session must stop working when the user is disabled")
	}
	// An API token bound to them goes with them.
	if _, _, ok, _ := st.APITokenRole(ctx, auth.Hash(apiToken)); ok {
		t.Error("an API token bound to a disabled user must stop working")
	}
	// And no password-reset link is issued, which would otherwise be a way back in.
	if _, _, err := st.UserByEmail(ctx, "disable-"+suffix+"@example.test"); err == nil {
		t.Error("a disabled user must not be able to request a password reset")
	}

	// Disabling twice keeps the original timestamp: the value an incident review
	// depends on is when access was actually withdrawn, not when someone last
	// clicked the button.
	again, err := st.SetUserDisabled(ctx, u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !again.DisabledAt.Equal(firstStamp) {
		t.Errorf("re-disabling moved the timestamp from %v to %v", firstStamp, *again.DisabledAt)
	}

	// Re-enabling restores exactly what was withdrawn.
	restored, err := st.SetUserDisabled(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.DisabledAt != nil {
		t.Error("enabling should clear the timestamp")
	}
	if _, _, _, err := st.GetUserAuth(ctx, name); err != nil {
		t.Errorf("login should work again after enabling: %v", err)
	}
	if _, _, ok, _ := st.APITokenRole(ctx, auth.Hash(apiToken)); !ok {
		t.Error("a bound API token should work again after enabling")
	}
}

// An unbound API token — one not tied to any user — must be unaffected by
// anyone being disabled. The join that filters bound tokens is easy to write in
// a way that drops these too.
func TestUnboundAPITokenSurvivesDisableIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	token := auth.NewToken()
	id, err := st.CreateAPIToken(ctx, auth.Hash(token), "unbound-"+suffix, models.RoleTech, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteAPIToken(context.Background(), id) })

	u, err := st.CreateUser(ctx, "other-"+suffix, "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := st.APITokenRole(ctx, auth.Hash(token)); err != nil || !ok {
		t.Errorf("an unbound API token must be unaffected: ok=%t err=%v", ok, err)
	}
}

// Alert mail stops. A disabled account still holds an address and a role that
// carries alerts.view, so nothing else in the delivery query would exclude it.
func TestDisabledUserReceivesNoAlertsIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `TRUNCATE users, clients, agents CASCADE`); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	agent, err := st.CreateAgent(ctx, "host-"+suffix, "linux", "amd64", "1.0.0", "tok-"+suffix, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "alerted-"+suffix, "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, u.ID, "alerted-"+suffix+"@example.test"); err != nil {
		t.Fatal(err)
	}

	recipients, err := st.RecipientsForAgentEvent(ctx, agent.ID, NotifyCheckFailed, DefaultSubscribed(NotifyCheckFailed))
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 {
		t.Fatalf("precondition: an active user should receive alerts, got %d recipients", len(recipients))
	}

	if _, err := st.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	recipients, err = st.RecipientsForAgentEvent(ctx, agent.ID, NotifyCheckFailed, DefaultSubscribed(NotifyCheckFailed))
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 0 {
		t.Errorf("a disabled user still receives alerts: %v", recipients)
	}
}
