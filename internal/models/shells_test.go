package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shell list is a contract between three places: this package validates it,
// the agent has an execution path for each value, and the console offers them in
// a picker. A value added here without the other two is a job that fails on the
// endpoint; a value offered in the console but not here is a form that 400s.
func TestSupportedShellsMatchTheConsolePicker(t *testing.T) {
	shells := []string{ShellBash, ShellSh, ShellPowerShell, ShellCmd, ShellPython}
	for _, shell := range shells {
		if !SupportedShell(shell) {
			t.Errorf("%q should be supported", shell)
		}
	}
	for _, shell := range []string{"", "ruby", "perl", "node", "PYTHON", " python"} {
		if SupportedShell(shell) {
			t.Errorf("%q must not be accepted", shell)
		}
	}

	screens, err := os.ReadFile(filepath.Join("..", "..", "openrmm", "src", "screens-rmm2.jsx"))
	if err != nil {
		// A moved or renamed console is a real signal, not a reason to skip: the
		// picker still exists somewhere and this test has stopped guarding it.
		t.Fatalf("cannot read the scripts screen: %v", err)
	}
	for _, shell := range shells {
		if !strings.Contains(string(screens), `<option value="`+shell+`">`) {
			t.Errorf("the console script editor does not offer %q", shell)
		}
	}
}

// Only shells added after the first release carry a version floor. Gating one
// that has always worked would refuse work that would in fact succeed.
func TestMinAgentVersionForOnlyGatesNewShells(t *testing.T) {
	if got := MinAgentVersionFor(ShellPython); got != MinAgentVersionPython {
		t.Errorf("python floor = %q, want %q", got, MinAgentVersionPython)
	}
	for _, shell := range []string{ShellBash, ShellSh, ShellPowerShell, ShellCmd, "ruby"} {
		if got := MinAgentVersionFor(shell); got != "" {
			t.Errorf("%q should carry no version floor, got %q", shell, got)
		}
	}
}

func TestAgentVersionAtLeast(t *testing.T) {
	for _, tt := range []struct {
		reported, minimum string
		want              bool
	}{
		{"0.4.9", "0.4.9", true},
		{"0.4.10", "0.4.9", true},
		{"0.5.0", "0.4.9", true},
		{"1.0.0", "0.4.9", true},
		{"v0.4.9", "0.4.9", true}, // a leading v is tolerated
		{"0.4.8", "0.4.9", false},
		{"0.3.99", "0.4.9", false},
		// Self-reported and unparseable: "unknown" must not read as "new enough",
		// or the endpoints whose state is least trustworthy walk through the gate.
		{"", "0.4.9", false},
		{"unknown", "0.4.9", false},
		{"0.4", "0.4.9", false},
		{"0.4.9-rc1", "0.4.9", false},
		{"dev", "0.4.9", false},
	} {
		if got := AgentVersionAtLeast(tt.reported, tt.minimum); got != tt.want {
			t.Errorf("AgentVersionAtLeast(%q, %q) = %t, want %t", tt.reported, tt.minimum, got, tt.want)
		}
	}
}

// The compiled-in agent version must name a release whose capabilities this
// source actually has. An agent that under-reports is refused work it can do;
// one that over-reports is handed work it cannot.
func TestAgentDefaultVersionClearsTheShellFloors(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "cmd", "agent", "main.go"))
	if err != nil {
		t.Fatalf("cannot read the agent: %v", err)
	}
	const marker = `var agentVersion = "`
	i := strings.Index(string(source), marker)
	if i < 0 {
		t.Fatal("cannot find the compiled-in agent version")
	}
	rest := string(source)[i+len(marker):]
	version := rest[:strings.Index(rest, `"`)]
	if !AgentVersionAtLeast(version, MinAgentVersionPython) {
		t.Errorf("the agent reports %q by default, below the python floor %q — a source-built agent would be refused python it can run",
			version, MinAgentVersionPython)
	}
}
