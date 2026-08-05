package settings

import (
	"testing"

	"github.com/p1ngu/rmm/internal/notify"
)

// account is the transport an instance must have: password resets ride it, and
// alerts do too unless the alerts profile says otherwise.
func account() notify.Config {
	return notify.Config{
		Host: "smtp.account.test", Port: 587, Username: "rmm@account.test",
		Password: "account-secret", From: "no-reply@account.test", FromName: "OpenRMM",
		TLSMode: "starttls",
	}
}

func TestMergeAlertProfileInheritsWholeTransport(t *testing.T) {
	got := MergeAlertProfile(account(), notify.Config{})
	if got.Host != "smtp.account.test" || got.From != "no-reply@account.test" {
		t.Fatalf("an empty alerts profile should be the account transport, got %+v", got)
	}
}

// The common case: one relay, two sender identities.
func TestMergeAlertProfileOverridesSenderOnly(t *testing.T) {
	got := MergeAlertProfile(account(), notify.Config{From: "alerts@account.test", FromName: "OpenRMM Alerts"})
	if got.From != "alerts@account.test" || got.FromName != "OpenRMM Alerts" {
		t.Errorf("the alert sender should win, got %q <%q>", got.FromName, got.From)
	}
	if got.Host != "smtp.account.test" || got.Password != "account-secret" {
		t.Errorf("the connection should still be the account one, got host=%q", got.Host)
	}
}

// A sender set without a display name must not keep the account profile's name,
// which would render as a mismatched "OpenRMM <alerts@…>".
func TestMergeAlertProfileDropsStaleFromName(t *testing.T) {
	if got := MergeAlertProfile(account(), notify.Config{From: "alerts@account.test"}); got.FromName != "" {
		t.Errorf("FromName = %q, want it cleared alongside a new From", got.FromName)
	}
}

// The security property of the whole design: naming a separate relay must not
// carry the account mailbox's credentials over to it.
func TestMergeAlertProfileNeverLeaksCredentialsToAnotherHost(t *testing.T) {
	alerts := notify.Config{
		Host: "relay.alerts.test", Port: 2525, From: "alerts@alerts.test", TLSMode: "tls",
	}
	got := MergeAlertProfile(account(), alerts)
	if got.Host != "relay.alerts.test" {
		t.Fatalf("host = %q, want the alerts relay", got.Host)
	}
	if got.Username != "" || got.Password != "" {
		t.Fatalf("the account credentials reached %s (user=%q, password set=%t)",
			got.Host, got.Username, got.Password != "")
	}
	if got.Port != 2525 || got.TLSMode != "tls" {
		t.Errorf("connection settings should come from the alerts profile, got port=%d tls=%q", got.Port, got.TLSMode)
	}
}

func TestValidSMTPProfile(t *testing.T) {
	if !ValidSMTPProfile(ProfileAccount) || !ValidSMTPProfile(ProfileAlerts) {
		t.Error("both known profiles should validate")
	}
	// A caller must not be able to point the settings writer at an arbitrary
	// key prefix and overwrite unrelated configuration.
	for _, bad := range []string{"", "smtp", "ai.", "smtp.alerts", "../"} {
		if ValidSMTPProfile(bad) {
			t.Errorf("%q should not be a valid profile", bad)
		}
	}
}

func TestSMTPKeysAreProfileScoped(t *testing.T) {
	keys := SMTPKeys(ProfileAlerts)
	if len(keys) != len(smtpFields) {
		t.Fatalf("got %d keys, want %d", len(keys), len(smtpFields))
	}
	var sawPassword bool
	for _, k := range keys {
		if k == "smtp.alerts.password" {
			sawPassword = true
			if !smtpSecret(k) {
				t.Error("the alerts password must be treated as a secret")
			}
		}
	}
	if !sawPassword {
		t.Error("the alerts profile should own a password key")
	}
	if smtpSecret("smtp.alerts.host") {
		t.Error("only password keys are secrets")
	}
}
