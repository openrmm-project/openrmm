package notify

import (
	"strings"
	"testing"
)

func TestSendValidatesBothMailboxesBeforeConnecting(t *testing.T) {
	const valid = "ops@example.test"
	for _, test := range []struct {
		name, from, to, want string
	}{
		{"sender", "no-reply", valid, "sender: invalid mailbox address"},
		{"recipient list", valid, "ana@example.test, bruno@example.test", "recipient: invalid mailbox address"},
		{"multiline address", valid, "Ana\r\n <ana@example.test>", "recipient: invalid mailbox address"},
		{"empty recipient", valid, "", "recipient is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Host: "127.0.0.1", Port: 1, TLSMode: "none", From: test.from, PublicOnly: true}
			err := Send(cfg, Message{To: test.to})
			if err == nil || err.Error() != test.want {
				t.Fatalf("Send() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEnvelopeMailboxPreservesQuotedLocalPart(t *testing.T) {
	addr, err := parseMailbox(`Ops Desk <"ops desk"@example.test>`)
	if err != nil {
		t.Fatal(err)
	}
	if addr.Name != "Ops Desk" {
		t.Fatalf("display name = %q", addr.Name)
	}
	if got, want := envelopeMailbox(addr), `"ops desk"@example.test`; got != want {
		t.Fatalf("envelope address = %q, want %q", got, want)
	}
}

func TestSanitizeHeaderFlattensLineBreaks(t *testing.T) {
	value := "Status\r\ndo\rserviço\nhoje"
	got := sanitizeHeader(value)
	if got != "Status do serviço hoje" {
		t.Errorf("sanitized header = %q", got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatal("sanitized header contains a line break")
	}
}

func TestValidateRejectsCredentialsWithoutTLS(t *testing.T) {
	cfg := Config{Host: "mail.example.test", Port: 25, From: "rmm@example.test",
		TLSMode: "none", Password: "hunter2"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected refusal to send credentials over an unencrypted link")
	}
}

func TestEnabledRequiresHostAndSender(t *testing.T) {
	if (Config{Host: "mail.example.test"}).Enabled() {
		t.Fatal("a config without a sender must not count as enabled")
	}
	if !(Config{Host: "mail.example.test", From: "rmm@example.test"}).Enabled() {
		t.Fatal("host + sender should be enough to attempt delivery")
	}
}
