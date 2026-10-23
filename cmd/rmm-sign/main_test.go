package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The P-256 manifest signature round-trips, refuses a changed manifest and a
// different key, and is what `openssl dgst -sha256 -verify` accepts — the
// check install.sh runs on a Mac without OpenSSL 3.
func TestP256ManifestSignature(t *testing.T) {
	priv, pub, err := generateP256()
	if err != nil {
		t.Fatal(err)
	}
	key, err := decodeP256PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, again, err := encodeP256(key); err != nil || again != pub {
		t.Fatalf("the public key does not follow from the private one: %v", err)
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "SHA256SUMS")
	body := []byte("0123abcd  rmm-agent-darwin-arm64\n")
	if err := os.WriteFile(manifest, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := signFileP256(manifest, key); err != nil {
		t.Fatal(err)
	}
	line, err := os.ReadFile(manifest + ".p256.sig")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(line))
	if len(fields) != 2 || fields[0] != p256SignatureVersion {
		t.Fatalf("signature file: %q", line)
	}
	if !verifyP256(body, fields[1], pub) {
		t.Fatal("a fresh signature does not verify")
	}
	if verifyP256([]byte("0123abcd  rmm-agent-darwin-amd64\n"), fields[1], pub) {
		t.Fatal("a changed manifest verified")
	}
	_, otherPub, _ := generateP256()
	if verifyP256(body, fields[1], otherPub) {
		t.Fatal("another key verified")
	}

	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("no openssl to check against")
	}
	pem := "-----BEGIN PUBLIC KEY-----\n" + pub + "\n-----END PUBLIC KEY-----\n"
	if err := os.WriteFile(filepath.Join(dir, "pub.pem"), []byte(pem), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(openssl, "base64", "-d", "-A", "-out", filepath.Join(dir, "sig.der"))
	cmd.Stdin = strings.NewReader(fields[1])
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("decode: %v %s", err, out)
	}
	if out, err := exec.Command(openssl, "dgst", "-sha256", "-verify", filepath.Join(dir, "pub.pem"),
		"-signature", filepath.Join(dir, "sig.der"), manifest).CombinedOutput(); err != nil || !strings.Contains(string(out), "Verified OK") {
		t.Fatalf("openssl refused the signature: %v %s", err, out)
	}
}

// Only an ECDSA P-256 key signs the manifest.
func TestP256RefusesOtherKeys(t *testing.T) {
	for _, bad := range []string{"", "not base64", "AAAA"} {
		if _, err := decodeP256PrivateKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// verifyP256 checks a signature written by signFileP256 against a base64 SPKI
// DER public key, as install.sh does with openssl.
func verifyP256(data []byte, sigB64, pubB64 string) bool {
	der, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return false
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return false
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(data)
	return ecdsa.VerifyASN1(pub, sum[:], sig)
}
