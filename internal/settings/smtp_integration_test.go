package settings

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/store"
)

// These run against a real Postgres because the behaviour under test *is* the
// persistence: which rows a save writes, and which it leaves alone.
//
// Only the alerts profile is persisted at all. The account transport comes from
// the environment, so it is set here as config fields rather than saved rows.
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
	cfg := &config.Config{
		AdminToken:   "a-long-admin-token-used-as-key-material",
		SMTPHost:     "smtp.account.test",
		SMTPPort:     587,
		SMTPUsername: "rmm@account.test",
		SMTPPassword: "account-secret",
		SMTPFrom:     "no-reply@account.test",
		SMTPFromName: "OpenRMM",
		SMTPTLSMode:  "starttls",
	}
	// Start from a known state: the one persisted profile cleared.
	for _, key := range SMTPKeys(ProfileAlerts) {
		if err := st.UpsertAppSetting(ctx, key, "", smtpSecret(key)); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, st, cfg
}

// The account transport is controlled by the server operator. A save that
// could reach it would let a settings administrator redirect authentication
// mail, so the write is refused outright rather than merely hidden in the UI.
func TestSaveSMTPRefusesTheAccountProfileIntegration(t *testing.T) {
	ctx, st, cfg := smtpStore(t)

	_, err := SaveSMTP(ctx, st, cfg, ProfileAccount, SMTPSaveRequest{
		Host: "attacker.relay.test", Port: 587, From: "no-reply@attacker.test", TLSMode: "starttls",
	})
	if !errors.Is(err, ErrAccountProfileReadOnly) {
		t.Fatalf("err = %v, want ErrAccountProfileReadOnly", err)
	}
	// And it wrote nothing on the way out.
	if got := AccountSMTPConfig(cfg); got.Host != "smtp.account.test" || got.From != "no-reply@account.test" {
		t.Fatalf("the refused save still changed the account transport: %+v", got)
	}
	rows, err := st.ListAppSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range SMTPKeys(ProfileAccount) {
		if row, ok := rows[key]; ok && row.Value != "" {
			t.Errorf("%s was persisted (%q); the account profile has no database form", key, row.Value)
		}
	}
}

// Saving the alerts profile must not reach the account keys — their prefixes
// overlap (`smtp.` vs `smtp.alerts.`), which is exactly how a sloppy write would
// silently take out password-reset delivery.
func TestSaveSMTPAlertsDoesNotTouchAccountKeysIntegration(t *testing.T) {
	ctx, st, cfg := smtpStore(t)

	if _, err := SaveSMTP(ctx, st, cfg, ProfileAlerts, SMTPSaveRequest{
		Host: "relay.alerts.test", Port: 2525, Username: "alerts@alerts.test",
		Password: "alerts-secret", From: "alerts@alerts.test", TLSMode: "tls",
	}); err != nil {
		t.Fatalf("save alerts profile: %v", err)
	}

	account := AccountSMTPConfig(cfg)
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

// With no account transport in the environment, an alerts profile that names its
// own relay still works. Email being "off" instance-wide is an account-profile
// fact, and must not be inferred onto a relay the operator configured.
func TestAlertsKeepTheirRelayWhenAccountMailIsOffIntegration(t *testing.T) {
	ctx, st, cfg := smtpStore(t)
	if _, err := SaveSMTP(ctx, st, cfg, ProfileAlerts, SMTPSaveRequest{
		Host: "relay.alerts.test", Port: 2525, From: "alerts@alerts.test", TLSMode: "tls",
	}); err != nil {
		t.Fatal(err)
	}

	// The environment supplies no account transport at all.
	cfg.SMTPHost, cfg.SMTPPassword, cfg.SMTPFrom = "", "", ""
	if got := AccountSMTPConfig(cfg); got.Enabled() {
		t.Fatalf("account mail should be off with no RMM_SMTP_HOST, got %+v", got)
	}
	alerts, err := AlertSMTPConfig(ctx, st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if alerts.Host != "relay.alerts.test" || !alerts.Enabled() {
		t.Errorf("alerts keep their own relay when account mail is off, got %+v", alerts)
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
