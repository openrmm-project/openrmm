package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOAuthRequiresHTTPSMCPOrigin(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_MCP_OAUTH_ENABLED", "true")
	t.Setenv("RMM_MCP_PUBLIC_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("OAuth started without an MCP public URL")
	}

	t.Setenv("RMM_MCP_PUBLIC_URL", "http://mcp.example.test")
	if _, err := Load(); err == nil {
		t.Fatal("OAuth started with a non-HTTPS MCP public URL")
	}

	t.Setenv("RMM_MCP_PUBLIC_URL", "https://mcp.example.test/")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MCPPublicURL != "https://mcp.example.test" {
		t.Fatalf("MCPPublicURL = %q", cfg.MCPPublicURL)
	}
}

func TestConsoleIngressEnforcementRequiresTrustedProxy(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_MCP_OAUTH_ENABLED", "false")
	t.Setenv("RMM_ENFORCE_CONSOLE_RELAY_INGRESS", "true")
	t.Setenv("RMM_TRUST_PROXY_HEADERS", "false")
	if _, err := Load(); err == nil {
		t.Fatal("console relay enforcement started without a trusted proxy")
	}

	t.Setenv("RMM_TRUST_PROXY_HEADERS", "true")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with trusted proxy: %v", err)
	}
}

func TestLoadReadsFileBackedSecrets(t *testing.T) {
	dir := t.TempDir()
	writeSecret := func(name, value string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Setenv("RMM_ADMIN_TOKEN_FILE", writeSecret("admin", strings.Repeat("a", 32)))
	t.Setenv("RMM_SETTINGS_ENCRYPTION_KEY_FILE", writeSecret("settings", strings.Repeat("s", 32)))
	t.Setenv("RMM_DATABASE_PASSWORD_FILE", writeSecret("database", "p@ss/word"))
	t.Setenv("RMM_DATABASE_URL", "postgres://rmm@postgres:5432/rmm?sslmode=disable")
	t.Setenv("RMM_REQUIRE_SETTINGS_ENCRYPTION_KEY", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SettingsEncryptionKey != strings.Repeat("s", 32) {
		t.Fatal("settings encryption key was not read from its secret file")
	}
	if cfg.DatabaseURL != "postgres://rmm:p%40ss%2Fword@postgres:5432/rmm?sslmode=disable" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestLoadRequiresDedicatedSettingsKeyWhenConfigured(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_SETTINGS_ENCRYPTION_KEY", "")
	t.Setenv("RMM_REQUIRE_SETTINGS_ENCRYPTION_KEY", "true")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a missing production settings encryption key")
	}
}
