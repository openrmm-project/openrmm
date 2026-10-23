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

// -verify-own-signature: install.sh's check on a host whose OpenSSL cannot
// check Ed25519. The running binary (here the test binary) must verify under
// the pinned key, and a wrong key or a malformed signature file must fail.
func TestVerifyOwnArtifactSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sigPath := filepath.Join(dir, "agent.sig")
	body := fmt.Sprintf("%s %s\n", signatureVersion, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:])))
	if err := os.WriteFile(sigPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	old := runtimeUpdatePublicKey
	t.Cleanup(func() { runtimeUpdatePublicKey = old })
	runtimeUpdatePublicKey = base64.StdEncoding.EncodeToString(pub)
	if err := verifyOwnArtifactSignature(sigPath); err != nil {
		t.Fatalf("own signature rejected: %v", err)
	}
	runtimeUpdatePublicKey = base64.StdEncoding.EncodeToString(other)
	if err := verifyOwnArtifactSignature(sigPath); err == nil {
		t.Fatal("own signature verified under a key that did not sign it")
	}
	runtimeUpdatePublicKey = base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(sigPath, []byte("not a signature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyOwnArtifactSignature(sigPath); err == nil {
		t.Fatal("a malformed signature file verified")
	}
	if err := verifyOwnArtifactSignature(filepath.Join(dir, "missing.sig")); err == nil {
		t.Fatal("a missing signature file verified")
	}
}
