package settings

import (
	"context"
	"errors"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
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

// The alerts relay is typed into the console, so delivery dials only public
// addresses unless the operator allowed private ones.
func TestAlertDeliveryIsPublicOnlyUnlessAllowed(t *testing.T) {
	relay := notify.Config{Host: "relay.tenant.test", Port: 2587, From: "alerts@tenant.test", TLSMode: "starttls"}
	if got := alertDelivery(&config.Config{}, relay); !got.PublicOnly {
		t.Error("a console-set alerts relay must be public-only by default")
	}
	if got := alertDelivery(&config.Config{SMTPAlertsAllowPrivate: true}, relay); got.PublicOnly {
		t.Error("RMM_SMTP_ALERTS_ALLOW_PRIVATE should lift the restriction")
	}
	if got := alertDelivery(&config.Config{}, notify.Config{}); got.Enabled() || got.PublicOnly {
		t.Errorf("no alerts relay is no transport: %+v", got)
	}
}

func TestCheckAlertsHostRefusesPrivateAddresses(t *testing.T) {
	ctx := context.Background()
	deny := &config.Config{}
	for _, host := range []string{"127.0.0.1", "10.0.0.5", "192.168.0.10", "169.254.169.254", "::1", "localhost"} {
		if err := checkAlertsHost(ctx, deny, host); !errors.Is(err, ErrAlertsHostNotPublic) {
			t.Errorf("checkAlertsHost(%s) = %v, want ErrAlertsHostNotPublic", host, err)
		}
	}
	for _, host := range []string{"8.8.8.8", "nonexistent.invalid"} {
		if err := checkAlertsHost(ctx, deny, host); err != nil {
			t.Errorf("checkAlertsHost(%s) = %v, want nil (public, or left to the dial)", host, err)
		}
	}
	if err := checkAlertsHost(ctx, &config.Config{SMTPAlertsAllowPrivate: true}, "10.0.0.5"); err != nil {
		t.Errorf("allowed private relay refused: %v", err)
	}
}
