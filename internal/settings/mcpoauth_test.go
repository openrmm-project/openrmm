package settings

import (
	"errors"
	"testing"
)

func TestNormalizeMCPHosts(t *testing.T) {
	got, err := NormalizeMCPHosts([]string{" Claude.AI ", "claude.ai", "*.Example.com", "", "localhost", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.example.com", "127.0.0.1", "claude.ai", "localhost"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	for _, bad := range []string{"https://claude.ai", "claude.ai/path", "claude.ai:443", "*", "*.", "a..b", "exa mple.com", "*.*.x.com"} {
		if _, err := NormalizeMCPHosts([]string{bad}); !errors.Is(err, ErrInvalidMCPHost) {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestMCPOAuthPolicyHosts(t *testing.T) {
	open := MCPOAuthPolicy{Registration: MCPRegistrationOpen}
	if !open.HostAllowed("anything.example") {
		t.Error("the open policy must admit any host")
	}
	list := MCPOAuthPolicy{Registration: MCPRegistrationAllowlist, AllowedHosts: []string{"claude.ai", "*.corp.example"}}
	for host, want := range map[string]bool{
		"claude.ai":           true,
		"CLAUDE.AI":           true,
		"evil-claude.ai":      false,
		"claude.ai.evil.test": false,
		"mcp.corp.example":    true,
		"a.b.corp.example":    true,
		"corp.example":        false, // "*." means subdomains, not the domain itself
		"xcorp.example":       false,
		"chatgpt.com":         false, // known, but not listed: known only silences the warning
	} {
		if got := list.HostAllowed(host); got != want {
			t.Errorf("allowlist HostAllowed(%s) = %v, want %v", host, got, want)
		}
	}
	for host, want := range map[string]bool{
		"claude.ai":               true, // built-in
		"chatgpt.com":             true, // built-in
		"mcp.corp.example":        true, // listed
		"attacker.test":           false,
		"claude.ai.attacker.test": false,
	} {
		if got := list.HostKnown(host); got != want {
			t.Errorf("HostKnown(%s) = %v, want %v", host, got, want)
		}
	}
}
