package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyFileSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("agent-build"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, sum[:])
	if err := verifyFileSignature(path, pub, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyFileSignature(path, pub, sig); err == nil {
		t.Fatal("tampered artifact accepted")
	}
}

func TestParseArtifactSignature(t *testing.T) {
	rawSig := make([]byte, ed25519.SignatureSize)
	sigText := base64.StdEncoding.EncodeToString(rawSig)
	got, err := parseArtifactSignature([]byte(fmt.Sprintf("%s %s\n", signatureVersion, sigText)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != ed25519.SignatureSize {
		t.Fatalf("signature length = %d", len(got))
	}

	if _, err := parseArtifactSignature([]byte("unknown " + sigText)); err == nil {
		t.Fatal("unsupported signature version accepted")
	}
}

func TestUpdateVerifyModeDefaultsToStrict(t *testing.T) {
	old := runtimeUpdateVerify
	runtimeUpdateVerify = ""
	t.Setenv("RMM_UPDATE_VERIFY", "")
	t.Cleanup(func() { runtimeUpdateVerify = old })
	if got := updateVerifyMode(); got != "strict" {
		t.Fatalf("updateVerifyMode() = %q, want strict", got)
	}
}

func TestConfiguredUpdatePublicKeysParsesRing(t *testing.T) {
	pubA, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)
	encA := base64.StdEncoding.EncodeToString(pubA)
	encB := base64.StdEncoding.EncodeToString(pubB)

	old := runtimeUpdatePublicKey
	t.Cleanup(func() { runtimeUpdatePublicKey = old })

	// Comma, whitespace and a duplicate: expect two distinct keys, order preserved.
	runtimeUpdatePublicKey = encA + ", " + encB + "\n" + encA
	keys, err := configuredUpdatePublicKeys()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	if !keys[0].Equal(pubA) || !keys[1].Equal(pubB) {
		t.Fatal("keys parsed in the wrong order or altered")
	}

	// A malformed entry must fail closed rather than silently drop.
	runtimeUpdatePublicKey = encA + ",not-a-key"
	if _, err := configuredUpdatePublicKeys(); err == nil {
		t.Fatal("malformed key in the ring was accepted")
	}
}

func TestVerifyLocalArtifactAcceptsAnyRingKey(t *testing.T) {
	pubA, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)

	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("rotated-build"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(privB, sum[:]) // signed by the second (new) key

	old := runtimeUpdatePublicKey
	t.Cleanup(func() { runtimeUpdatePublicKey = old })
	runtimeUpdatePublicKey = base64.StdEncoding.EncodeToString(pubA) + "," + base64.StdEncoding.EncodeToString(pubB)

	if err := verifyLocalArtifactSignature(path, sig); err != nil {
		t.Fatalf("signature from a trusted ring key rejected: %v", err)
	}

	// Drop the signing key from the ring: verification must now fail.
	runtimeUpdatePublicKey = base64.StdEncoding.EncodeToString(pubA)
	if err := verifyLocalArtifactSignature(path, sig); err == nil {
		t.Fatal("signature verified against a key not in the ring")
	}
}
