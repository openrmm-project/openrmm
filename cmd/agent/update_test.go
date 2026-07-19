package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.4", "1.2.3", 1},
		{"1.2.2", "1.2.3", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.2.3", "1.2.3-rc.1", 1},
		{"1.2.3-rc.2", "1.2.3-rc.10", -1},
		{"1.2.3-alpha", "1.2.3-beta", -1},
		{"1.2.3+build.2", "1.2.3+build.1", 0},
	}
	for _, test := range tests {
		got, err := compareVersions(test.a, test.b)
		if err != nil {
			t.Fatalf("compareVersions(%q, %q): %v", test.a, test.b, err)
		}
		if got != test.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}

func TestValidateUpgradeVersionRejectsDowngrade(t *testing.T) {
	if err := validateUpgradeVersion("1.4.0", "1.3.9"); err == nil {
		t.Fatal("downgrade accepted")
	}
	if err := validateUpgradeVersion("1.4.0", "1.4.0"); err != nil {
		t.Fatalf("same version rejected: %v", err)
	}
	if err := validateUpgradeVersion("1.4.0", "1.5.0"); err != nil {
		t.Fatalf("upgrade rejected: %v", err)
	}
}

func TestValidateRuntimeSecurity(t *testing.T) {
	oldKey, oldMode := runtimeUpdatePublicKey, runtimeUpdateVerify
	t.Cleanup(func() {
		runtimeUpdatePublicKey, runtimeUpdateVerify = oldKey, oldMode
	})
	runtimeUpdatePublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	runtimeUpdateVerify = "strict"

	if err := validateRuntimeSecurity("wss://rmm.example/ws/agent", false, true); err != nil {
		t.Fatalf("secure production configuration rejected: %v", err)
	}
	if err := validateRuntimeSecurity("ws://rmm.example/ws/agent", false, false); err == nil {
		t.Fatal("insecure production transport accepted")
	}
	runtimeUpdateVerify = "off"
	if err := validateRuntimeSecurity("wss://rmm.example/ws/agent", false, true); err == nil {
		t.Fatal("disabled production verification accepted")
	}
	if err := validateRuntimeSecurity("ws://lab/ws/agent", true, false); err != nil {
		t.Fatalf("explicit lab configuration rejected: %v", err)
	}
}

func TestParseVersionRejectsInvalidInput(t *testing.T) {
	for _, version := range []string{"", "1.2", "1.2.3.4", "1.02.3", "release"} {
		if _, err := parseVersion(version); err == nil {
			t.Errorf("parseVersion(%q) succeeded", version)
		}
	}
}

func TestValidatePendingRollbackRequiresRecordedSignedArtifacts(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent")
	candidate := filepath.Join(dir, "candidate")
	if err := os.WriteFile(exe, []byte("old signed agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new signed agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSum, err := fileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLocalArtifactSignature(exe+".sig", ed25519.Sign(priv, oldSum[:])); err != nil {
		t.Fatal(err)
	}
	oldKey := runtimeUpdatePublicKey
	runtimeUpdatePublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { runtimeUpdatePublicKey = oldKey })
	sum, err := fileSHA256(candidate)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(priv, sum[:])
	if err := writePendingUpdate(exe, candidate, "etag", "1.0.0", "1.1.0", signature); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe, exe+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe+".sig", exe+".old.sig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(candidate, exe); err != nil {
		t.Fatal(err)
	}
	if err := writeLocalArtifactSignature(exe+".sig", signature); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingRollback(exe); err != nil {
		t.Fatalf("valid pending rollback rejected: %v", err)
	}
	if err := os.WriteFile(exe+".old", []byte("attacker downgrade"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePendingRollback(exe); err == nil {
		t.Fatal("tampered rollback artifact accepted")
	}
}
