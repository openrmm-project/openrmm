//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMacSidecarDetectsPrivacyIdentityChange(t *testing.T) {
	makeBundle := func(name, source string) string {
		t.Helper()
		bundle := filepath.Join(t.TempDir(), name+".app")
		exe := filepath.Join(bundle, "Contents", "MacOS", "rmm-remote")
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>io.openrmm.remote</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleExecutable</key><string>rmm-remote</string>
</dict></plist>`)
		if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), plist, 0o644); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, body, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("/usr/bin/codesign", "--sign", "-", "--force", "--identifier", "io.openrmm.remote", bundle).CombinedOutput(); err != nil {
			t.Fatalf("sign test bundle: %v: %s", err, out)
		}
		return exe
	}
	old := makeBundle("old", "/usr/bin/true")
	same := makeBundle("same", "/usr/bin/true")
	changed := makeBundle("changed", "/usr/bin/false")
	if err := validateSidecarCandidate("", old); err != nil {
		t.Fatalf("first install refused: %v", err)
	}
	if err := validateSidecarCandidate(old, same); err != nil {
		t.Fatalf("same identity refused: %v", err)
	}
	if err := validateSidecarCandidate(old, changed); err == nil {
		t.Fatal("changed ad-hoc identity reported as compatible")
	}
}

// A stock Mac has no Command Line Tools. The identity check must step aside
// there rather than block the sidecar from being installed at all.
func TestMacSidecarInstallsWithoutCodesign(t *testing.T) {
	previous := codesignExe
	codesignExe = filepath.Join(t.TempDir(), "no-codesign")
	defer func() { codesignExe = previous }()
	if err := validateSidecarCandidate("/nonexistent/current", "/nonexistent/candidate"); err != nil {
		t.Fatalf("update refused without codesign: %v", err)
	}
}
