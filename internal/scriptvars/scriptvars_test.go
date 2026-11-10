package scriptvars

import (
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

func TestUserNames(t *testing.T) {
	if err := ValidUserName("HUNTRESS_ACCOUNT_KEY"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rmm_agent_id", "RMM_AGENT_ID", "OPENRMM_PARAM_X", "PATH", "1KEY", "has space", "key"} {
		if err := ValidUserName(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	names, err := Normalize([]string{"SCREENCONNECT_INSTANCE", "HUNTRESS_ACCOUNT_KEY", "HUNTRESS_ACCOUNT_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "HUNTRESS_ACCOUNT_KEY,SCREENCONNECT_INSTANCE" {
		t.Fatalf("normalized = %v", names)
	}
}

func TestSecretValueBounds(t *testing.T) {
	if err := ValidValue(true, "short"); err == nil {
		t.Fatal("short secret accepted")
	}
	if err := ValidValue(false, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := ValidValue(true, "abcd\nsecret"); err == nil {
		t.Fatal("newline accepted")
	}
}

func TestRedactRemovesTheLongerSecretFirst(t *testing.T) {
	got := Redact("key=abcdefghij extra=abcdefghijXX", []string{"abcdefghij", "abcdefghijXX"})
	if got != "key=******** extra=********" {
		t.Fatalf("redacted = %q", got)
	}
}

func TestSealRoundTrip(t *testing.T) {
	cfg := &config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}
	blob, err := Seal(cfg, []string{"huntress-account-key"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(blob, "huntress") {
		t.Fatalf("ciphertext contains the secret: %s", blob)
	}
	back, err := Open(cfg, blob)
	if err != nil || len(back) != 1 || back[0] != "huntress-account-key" {
		t.Fatalf("open = %v, %v", back, err)
	}
}

func TestEncryptedRedactionsRejectPlaintextAndCorruption(t *testing.T) {
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-key"}
	for _, blob := range []string{`["synthetic-secret"]`, "enc:v1:bad"} {
		if _, err := Open(cfg, blob); err == nil {
			t.Fatal("unsealed redactions accepted")
		}
	}
	blob, err := Seal(cfg, []string{"synthetic-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(&config.Config{SettingsEncryptionKey: "wrong-key"}, blob); err == nil {
		t.Fatal("wrong key accepted")
	}
}
