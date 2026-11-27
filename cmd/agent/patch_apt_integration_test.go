package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt-in only: run in a disposable Debian container with --network none.
// The repository and installed package database are entirely synthetic.
func TestIsolatedAptRealFixture(t *testing.T) {
	if os.Getenv("OPENRMM_TEST_APT") != "1" {
		t.Skip("set OPENRMM_TEST_APT=1 only in a disposable test container")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Linux APT fixture only")
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		t.Skip("apt-get is unavailable")
	}

	base, err := os.MkdirTemp("/tmp", "openrmm-apt-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	if err = os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(base, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"repo", "host-lists", "cache"} {
		if err := os.Mkdir(filepath.Join(base, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write("status", "Package: openrmm-fixture\nStatus: install ok installed\nPriority: optional\nSection: misc\nArchitecture: all\nVersion: 1.0\nMaintainer: Fixture <fixture@example.invalid>\nDescription: isolated fixture\n\n")
	write("repo/Packages", "Package: openrmm-fixture\nVersion: 2.0\nArchitecture: all\nMaintainer: Fixture <fixture@example.invalid>\nFilename: ./fixture.deb\nSize: 100\nSHA256: "+strings.Repeat("0", 64)+"\nDescription: isolated fixture\n\n")
	write("sources.list", "deb [trusted=yes] file:"+base+"/repo ./\n")
	write("host-lists/canary", "host lists preserved")
	write("cache/pkgcache", "package cache preserved")
	write("cache/srcpkgcache", "source cache preserved")
	hook := filepath.Join(base, "hook-ran")
	config := fmt.Sprintf(`Dir::Etc::parts "-";
Dir::Etc::main "-";
Dir::Etc::sourcelist "%s/sources.list";
Dir::Etc::sourceparts "-";
Dir::State::status "%s/status";
Dir::State::lists "%s/host-lists";
Dir::Cache::pkgcache "%s/cache/pkgcache";
Dir::Cache::srcpkgcache "%s/cache/srcpkgcache";
APT::Update::Pre-Invoke { "touch %s"; };
APT::Update::Post-Invoke { "touch %s"; };
APT::Update::Post-Invoke-Success { "touch %s"; };
`, base, base, base, base, base, hook, hook, hook)
	write("apt.conf", config)
	t.Setenv("APT_CONFIG", filepath.Join(base, "apt.conf"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	patches, err := scanAptWithRunner(ctx, true, runPatchCommand, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].Identifier != "openrmm-fixture" || patches[0].Version != "2.0" {
		t.Fatalf("unexpected findings: %+v", patches)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatalf("APT update hook ran: %v", err)
	}
	for path, want := range map[string]string{"host-lists/canary": "host lists preserved", "cache/pkgcache": "package cache preserved", "cache/srcpkgcache": "source cache preserved"} {
		got, err := os.ReadFile(filepath.Join(base, path))
		if err != nil || string(got) != want {
			t.Fatalf("host state changed %s: %v", path, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(base, "host-lists"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("host lists changed: %v %v", entries, err)
	}
	dirs, _ := filepath.Glob(filepath.Join(base, "openrmm-scan-*"))
	if len(dirs) != 0 {
		t.Fatalf("private scan dirs remain: %v", dirs)
	}
	status, _ := os.ReadFile(filepath.Join(base, "status"))
	if !strings.Contains(string(status), "Version: 1.0") {
		t.Fatal("installed status changed")
	}
	t.Log("Real apt-get isolated update + simulation found v2; all three hooks blocked; host lists, caches and installed status preserved; private catalog removed")
}
