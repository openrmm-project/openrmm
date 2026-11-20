package config

import (
	"strings"
	"testing"
)

func TestNotificationConsoleURL(t *testing.T) {
	c := &Config{PublicURL: "https://agents.example", ConsolePublicURL: "https://console.example"}
	if c.NotificationConsoleURL() != "https://console.example" {
		t.Fatal("notification links used the agent origin")
	}
	c.ConsolePublicURL = ""
	if c.NotificationConsoleURL() != c.PublicURL {
		t.Fatal("single-origin fallback changed")
	}
}

func TestLoadNotificationConsoleURL(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_MCP_OAUTH_ENABLED", "false")
	t.Setenv("RMM_PUBLIC_URL", "https://agents.example.test")
	t.Setenv("RMM_CONSOLE_PUBLIC_URL", "https://console.example.test/")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NotificationConsoleURL() != "https://console.example.test" {
		t.Fatalf("console URL: %q", cfg.NotificationConsoleURL())
	}
	if cfg.PublicURL != "https://agents.example.test" {
		t.Fatal("agent origin changed")
	}
	t.Setenv("RMM_CONSOLE_PUBLIC_URL", "")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NotificationConsoleURL() != cfg.PublicURL {
		t.Fatal("unset console URL did not fall back")
	}
}
