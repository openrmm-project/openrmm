package settings

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/store"
)

// These run against a real Postgres because the behaviour under test *is* the
// persistence: which rows a save writes, and which it leaves alone.
func smtpStore(t *testing.T) (context.Context, *store.Store, *config.Config) {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := store.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AdminToken: "a-long-admin-token-used-as-key-material"}
	// Start from a known state: both profiles cleared.
	for _, profile := range []string{ProfileAccount, ProfileAlerts} {
		for _, key := range SMTPKeys(profile) {
			if err := st.UpsertAppSetting(ctx, key, "", smtpSecret(key)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return ctx, st, cfg
}

func saveAccount(t *testing.T, ctx context.Context, st *store.Store, cfg *config.Config) {
	t.Helper()
	if _, err := SaveSMTP(ctx, st, cfg, ProfileAccount, SMTPSaveRequest{
		Host: "smtp.account.test", Port: 587, Username: "rmm@account.test",
		Password: "account-secret", From: "no-reply@account.test", FromName: "OpenRMM",
		TLSMode: "starttls",
	}); err != nil {
		t.Fatalf("save account profile: %v", err)
	}
}

// Saving one profile must not disturb the other. A save that reached the wrong
// keys would silently take out password-reset delivery.
func TestSaveSMTPProfilesAreIsolatedIntegration(t *testing.T) {
	ctx, st, cfg := smtpStore(t)
	saveAccount(t, ctx, st, cfg)

	if _, err := SaveSMTP(ctx, st, cfg, ProfileAlerts, SMTPSaveRequest{
		Host: "relay.alerts.test", Port: 2525, Username: "alerts@alerts.test",
		Password: "alerts-secret", From: "alerts@alerts.test", TLSMode: "tls",
	}); err != nil {
		t.Fatalf("save alerts profile: %v", err)
	}

	account, err := SMTPConfig(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if account.Host != "smtp.account.test" || account.From != "no-reply@account.test" {
		t.Fatalf("the alerts save changed the account profile: %+v", account)
	}
	if account.Password != "account-secret" || account.Port != 587 {
		t.Errorf("account credentials/port were disturbed: port=%d password-intact=%t", account.Port, account.Password == "account-secret")
	}

	alerts, err := AlertSMTPConfig(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if alerts.Host != "relay.alerts.test" || alerts.Password != "alerts-secret" || alerts.Port != 2525 {
		t.Errorf("alerts profile did not persist: %+v", alerts)
	}
}

// A blank host on the alerts profile means "ride the account transport". The
// sender identity must survive that, and the credentials must not: a profile
// that names no server has no business holding a password for one.
func TestSaveSMTPAlertsBlankHostKeepsSenderDropsCredentialsIntegration(t *testing.T) {
	ctx, st, cfg := smtpStore(t)
	saveAccount(t, ctx, st, cfg)

	// First give alerts a relay of its own, so there is a password to drop.
	if _, err := SaveSMTP(ctx, st, cfg, ProfileAlerts, SMTPSaveRequest{
		Host: "relay.alerts.test", Port: 2525, Username: "alerts@alerts.test",
		Password: "alerts-secret", From: "alerts@alerts.test", TLSMode: "tls",
	}); err != nil {
		t.Fatal(err)
	}
	// Now withdraw the host, keeping the sender.
	if _, err := SaveSMTP(ctx, st, cfg, ProfileAlerts, SMTPSaveRequest{
		From: "alerts@account.test", FromName: "OpenRMM Alerts",
	}); err != nil {
		t.Fatalf("save inherited alerts profile: %v", err)
	}

	stored, err := SMTPProfile(ctx, st, cfg, ProfileAlerts)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Host != "" {
		t.Errorf("host = %q, want it cleared", stored.Host)
	}
	if stored.Username != "" || stored.Password != "" {
		t.Errorf("credentials survived a host withdrawal: user=%q password-set=%t", stored.Username, stored.Password != "")
	}
	if stored.From != "alerts@account.test" || stored.FromName != "OpenRMM Alerts" {
		t.Errorf("the sender identity should survive, got %q <%q>", stored.FromName, stored.From)
	}

	// Effectively: the account connection, under the alert sender.
	effective, err := AlertSMTPConfig(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Host != "smtp.account.test" || effective.Password != "account-secret" {
		t.Errorf("alerts should now ride the account transport, got host=%q", effective.Host)
	}
	if effective.From != "alerts@account.test" {
		t.Errorf("From = %q, want the alert sender", effective.From)
	}
}

// Clearing the account host turns email off and drops its password, and must not
// take the alerts profile's own stored settings with it.
func TestSaveSMTPClearingAccountLeavesAlertsIntegration(t *testing.T) {
	ctx, st, cfg := smtpStore(t)
	saveAccount(t, ctx, st, cfg)
	if _, err := SaveSMTP(ctx, st, cfg, ProfileAlerts, SMTPSaveRequest{
		Host: "relay.alerts.test", Port: 2525, From: "alerts@alerts.test", TLSMode: "tls",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := SaveSMTP(ctx, st, cfg, ProfileAccount, SMTPSaveRequest{}); err != nil {
		t.Fatalf("clear account profile: %v", err)
	}
	account, err := SMTPConfig(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if account.Host != "" || account.Password != "" {
		t.Errorf("clearing should drop the host and password, got %+v", account)
	}
	alerts, err := AlertSMTPConfig(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if alerts.Host != "relay.alerts.test" || !alerts.Enabled() {
		t.Errorf("alerts keep their own relay when account mail is turned off, got %+v", alerts)
	}
}

// An unknown profile must be refused rather than writing to an arbitrary key
// prefix and overwriting unrelated configuration.
func TestSaveSMTPRejectsUnknownProfileIntegration(t *testing.T) {
	ctx, st, cfg := smtpStore(t)
	if _, err := SaveSMTP(ctx, st, cfg, "ai.", SMTPSaveRequest{Host: "x.test", Port: 25, From: "a@b.test", TLSMode: "none"}); err == nil {
		t.Fatal("an unknown profile should be rejected")
	}
}
