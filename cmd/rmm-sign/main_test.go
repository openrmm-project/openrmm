package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"io"
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

// The private keys live in the environment while a release signs. Help, a
// mistyped flag, a missing argument and a bad key must never print them: the
// flag package used to show them as the flags' defaults.
func TestRunNeverPrintsPrivateKeys(t *testing.T) {
	const edCanary = "CANARY-ED25519-PRIVATE-KEY-0123456789"
	const p256Canary = "CANARY-P256-PRIVATE-KEY-0123456789"
	getenv := func(name string) string {
		switch name {
		case "RMM_SIGNING_KEY":
			return edCanary
		case "RMM_SIGNING_KEY_P256":
			return p256Canary
		}
		return ""
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"-h"}, 0},
		{[]string{"--help"}, 0},
		{[]string{"-no-such-flag"}, 2},
		{[]string{"-key"}, 2},
		{[]string{"-key-p256"}, 2},
		{[]string{"-p256"}, 1},
		{[]string{}, 1},
		{[]string{"-print-public-key"}, 1},
		{[]string{"-print-public-key-p256"}, 1},
		{[]string{"-p256", filepath.Join(t.TempDir(), "SHA256SUMS")}, 1},
	} {
		var stdout, stderr strings.Builder
		code := run(tc.args, getenv, &stdout, &stderr)
		out := stdout.String() + stderr.String()
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d (%s)", tc.args, code, tc.code, out)
		}
		if strings.Contains(out, edCanary) || strings.Contains(out, p256Canary) {
			t.Errorf("%v printed a private key:\n%s", tc.args, out)
		}
	}
	// Control: help is really printed, with the flags it describes.
	var help strings.Builder
	run([]string{"-h"}, getenv, io.Discard, &help)
	if !strings.Contains(help.String(), "-key-p256") || !strings.Contains(help.String(), "RMM_SIGNING_KEY_P256") {
		t.Fatalf("help does not describe the key flags:\n%s", help.String())
	}
}

// The key still comes from the environment when no flag gives one, and a flag
// given explicitly wins over it.
func TestRunKeyFromEnvironmentOrFlag(t *testing.T) {
	envPriv, envPub, err := generateP256()
	if err != nil {
		t.Fatal(err)
	}
	flagPriv, flagPub, err := generateP256()
	if err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string {
		if name == "RMM_SIGNING_KEY_P256" {
			return envPriv
		}
		return ""
	}
	var out strings.Builder
	if code := run([]string{"-print-public-key-p256"}, getenv, &out, io.Discard); code != 0 || strings.TrimSpace(out.String()) != envPub {
		t.Fatalf("from the environment: exit %d, %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"-key-p256", flagPriv, "-print-public-key-p256"}, getenv, &out, io.Discard); code != 0 || strings.TrimSpace(out.String()) != flagPub {
		t.Fatalf("from the flag: exit %d, %q", code, out.String())
	}

	manifest := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(manifest, []byte("0123abcd  rmm-agent-linux-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-p256", manifest}, getenv, io.Discard, io.Discard); code != 0 {
		t.Fatalf("signing with the environment's key: exit %d", code)
	}
	line, err := os.ReadFile(manifest + ".p256.sig")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(line))
	if len(fields) != 2 || !verifyP256([]byte("0123abcd  rmm-agent-linux-amd64\n"), fields[1], envPub) {
		t.Fatalf("the signature does not verify under the environment's key: %q", line)
	}
}
