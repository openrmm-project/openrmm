package settings

import (
	"testing"

	"github.com/p1ngu/rmm/internal/notify"
)

// Alerts never inherit from the account transport — not the relay, not its
// credentials, not its sender. An alerts profile without a host is no transport.
func TestAlertTransportWithoutHostIsOff(t *testing.T) {
	for _, alerts := range []notify.Config{
		{},
		// A sender stored by an older version, which rode the account relay.
		{From: "billing@operator.test", FromName: "Operator Billing"},
	} {
		if got := AlertTransport(alerts); got.Enabled() || got.Host != "" || got.From != "" {
			t.Errorf("AlertTransport(%+v) = %+v, want no transport", alerts, got)
		}
	}
}

func TestAlertTransportIsTheTenantsOwnRelay(t *testing.T) {
	alerts := notify.Config{Host: "relay.tenant.test", Port: 2525, Username: "u", Password: "p", From: "alerts@tenant.test", TLSMode: "tls"}
	if got := AlertTransport(alerts); got != alerts {
		t.Errorf("AlertTransport changed the tenant's relay: %+v", got)
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
