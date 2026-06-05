package settings

import (
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
)

func TestEncryptRoundTrip(t *testing.T) {
	cfg := &config.Config{AdminToken: "a-long-admin-token-used-as-key-material"}
	const secret = "sk-super-secret-api-key"

	enc, err := encrypt(cfg, secret)
	if err != nil {
		t.Fatalf("encrypt error: %v", err)
	}
	if !strings.HasPrefix(enc, "enc:v1:") {
		t.Fatalf("ciphertext missing enc:v1: prefix: %q", enc)
	}
	if strings.Contains(enc, secret) {
		t.Fatal("plaintext leaked into ciphertext")
	}
	dec, err := decrypt(cfg, enc)
	if err != nil {
		t.Fatalf("decrypt error: %v", err)
	}
	if dec != secret {
		t.Fatalf("round-trip mismatch: got %q want %q", dec, secret)
	}
}

func TestEncryptNonceIsRandom(t *testing.T) {
	cfg := &config.Config{AdminToken: "key-material-key-material"}
	a, _ := encrypt(cfg, "same")
	b, _ := encrypt(cfg, "same")
	if a == b {
		t.Fatal("two encryptions of the same plaintext are identical (nonce not random)")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	enc, _ := encrypt(&config.Config{AdminToken: "key-one-key-one-key-one"}, "secret")
	if _, err := decrypt(&config.Config{AdminToken: "key-two-key-two-key-two"}, enc); err == nil {
		t.Fatal("decrypt with the wrong key should fail (GCM auth)")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	cfg := &config.Config{AdminToken: "tamper-key-tamper-key"}
	enc, _ := encrypt(cfg, "secret")
	// flip a character in the base64 body
	tampered := enc[:len(enc)-1] + "A"
	if tampered == enc {
		tampered = enc[:len(enc)-1] + "B"
	}
	if _, err := decrypt(cfg, tampered); err == nil {
		t.Fatal("decrypt of tampered ciphertext should fail")
	}
}

func TestDecryptLegacyPlaintextPassThrough(t *testing.T) {
	cfg := &config.Config{AdminToken: "k"}
	// values without the enc:v1: prefix are treated as legacy plaintext.
	got, err := decrypt(cfg, "legacy-plain-value")
	if err != nil || got != "legacy-plain-value" {
		t.Fatalf("legacy pass-through failed: got %q err %v", got, err)
	}
}

// A dedicated encryption key takes precedence over the admin token, so rotating
// the admin token doesn't invalidate stored secrets.
func TestDedicatedKeyDecouplesFromAdminToken(t *testing.T) {
	enc, _ := encrypt(&config.Config{SettingsEncryptionKey: "dedicated", AdminToken: "admin-1"}, "secret")
	dec, err := decrypt(&config.Config{SettingsEncryptionKey: "dedicated", AdminToken: "admin-2-rotated"}, enc)
	if err != nil || dec != "secret" {
		t.Fatalf("dedicated key should decrypt regardless of admin token: got %q err %v", dec, err)
	}
}
