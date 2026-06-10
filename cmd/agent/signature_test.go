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
