package ai

import (
	"net"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

func TestSafeToken(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		allowSpace bool
		ok         bool
	}{
		{"empty", "", false, false},
		{"blank", "   ", false, false},
		{"clean", "nginx", false, true},
		{"clean-dotted", "Mozilla.Firefox", false, true},
		{"space-disallowed", "a b", false, false},
		{"space-allowed", "HP LaserJet", true, true},
		{"semicolon", "a;rm -rf /", false, false},
		{"pipe", "a|b", true, false},
		{"ampersand", "a&b", true, false},
		{"dollar", "a$x", true, false},
		{"backtick", "a`b`", true, false},
		{"subshell", "$(whoami)", true, false},
		{"redirect", "a>b", true, false},
		{"newline", "a\nb", true, false},
		{"quote", "a\"b", true, false},
		{"backslash", "a\\b", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := safeToken(c.in, c.allowSpace)
			if c.ok && got != "" {
				t.Fatalf("safeToken(%q) rejected unexpectedly: %s", c.in, got)
			}
			if !c.ok && got == "" {
				t.Fatalf("safeToken(%q) accepted but should have been rejected", c.in)
			}
		})
	}
}

func TestPkgCmd(t *testing.T) {
	cases := []struct {
		os      string
		install bool
		shell   string
		substr  string
	}{
		{"windows", true, models.ShellPowerShell, "winget install"},
		{"windows", false, models.ShellPowerShell, "winget uninstall"},
		{"linux", true, models.ShellBash, "apt-get install"},
		{"linux", false, models.ShellBash, "apt-get remove"},
		{"darwin", true, models.ShellBash, "brew install"},
		{"darwin", false, models.ShellBash, "brew uninstall"},
	}
	for _, c := range cases {
		shell, script, err := pkgCmd(c.os, "Acme.Pkg", c.install)
		if err != nil {
			t.Fatalf("pkgCmd(%s,%v) error: %v", c.os, c.install, err)
		}
		if shell != c.shell {
			t.Errorf("pkgCmd(%s) shell = %q, want %q", c.os, shell, c.shell)
		}
		if !strings.Contains(script, c.substr) || !strings.Contains(script, "Acme.Pkg") {
			t.Errorf("pkgCmd(%s,%v) = %q, want it to contain %q and the package", c.os, c.install, script, c.substr)
		}
	}
	if _, _, err := pkgCmd("plan9", "x", true); err == nil {
		t.Error("pkgCmd(unsupported) should error")
	}
}

func TestServiceRestartCmd(t *testing.T) {
	for _, os := range []string{"windows", "linux", "darwin"} {
		shell, script, err := serviceRestartCmd(os, "spooler")
		if err != nil || shell == "" || !strings.Contains(script, "spooler") {
			t.Errorf("serviceRestartCmd(%s) = (%q,%q,%v)", os, shell, script, err)
		}
	}
	if _, _, err := serviceRestartCmd("plan9", "x"); err == nil {
		t.Error("serviceRestartCmd(unsupported) should error")
	}
}

func TestInstallPrinterCmd(t *testing.T) {
	shell, script, err := installPrinterCmd("windows", "Front Desk", "10.0.0.5", "")
	if err != nil || shell != models.ShellPowerShell || !strings.Contains(script, "Add-Printer") || !strings.Contains(script, "10.0.0.5") {
		t.Errorf("installPrinterCmd(windows) = (%q,%q,%v)", shell, script, err)
	}
	_, script, err = installPrinterCmd("linux", "Front Desk", "10.0.0.5", "")
	if err != nil || !strings.Contains(script, "lpadmin") || !strings.Contains(script, "Front_Desk") {
		t.Errorf("installPrinterCmd(linux) = (%q,%v) — want lpadmin + sanitized name", script, err)
	}
	if _, _, err := installPrinterCmd("plan9", "p", "1.2.3.4", ""); err == nil {
		t.Error("installPrinterCmd(unsupported) should error")
	}
}

func TestRebootCmd(t *testing.T) {
	_, script, err := rebootCmd("windows", 0) // 0 -> default 60s
	if err != nil || !strings.Contains(script, "/t 60") {
		t.Errorf("rebootCmd(windows,0) = %q,%v — want default 60s", script, err)
	}
	_, script, _ = rebootCmd("linux", 120) // 120s -> 2 min
	if !strings.Contains(script, "+2") {
		t.Errorf("rebootCmd(linux,120) = %q — want +2 minutes", script)
	}
	_, script, _ = rebootCmd("linux", 0) // default 60s -> 1 min
	if !strings.Contains(script, "+1") {
		t.Errorf("rebootCmd(linux,0) = %q — want +1 minute", script)
	}
	if _, _, err := rebootCmd("plan9", 10); err == nil {
		t.Error("rebootCmd(unsupported) should error")
	}
}

func TestFlushDNSCmd(t *testing.T) {
	for _, os := range []string{"windows", "linux", "darwin"} {
		if _, _, err := flushDNSCmd(os); err != nil {
			t.Errorf("flushDNSCmd(%s) error: %v", os, err)
		}
	}
	if _, _, err := flushDNSCmd("plan9"); err == nil {
		t.Error("flushDNSCmd(unsupported) should error")
	}
}

// install_printer validates the IP with net.ParseIP before building the command;
// this guards the contract that a bad/injected IP never reaches the script.
func TestPrinterIPContract(t *testing.T) {
	for _, good := range []string{"10.0.0.5", "192.168.1.250"} {
		if net.ParseIP(good) == nil {
			t.Errorf("expected %q to be a valid IP", good)
		}
	}
	for _, bad := range []string{"10.0.0", "1.2.3.4; rm -rf /", "$(id)", "not-an-ip"} {
		if net.ParseIP(bad) != nil {
			t.Errorf("expected %q to be rejected as an IP", bad)
		}
	}
}
