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

func TestOIDCRequiresIssuerClientAndHTTPS(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_OIDC_ENABLED", "true")

	if _, err := Load(); err == nil {
		t.Fatal("single sign-on started with no issuer")
	}

	t.Setenv("RMM_OIDC_ISSUER", "https://idp.example.test")
	if _, err := Load(); err == nil {
		t.Fatal("single sign-on started with no client id")
	}

	// Discovery, the token exchange and the JWKS fetch all use this URL. Plain
	// HTTP would put the client secret and the authorization code on the wire.
	t.Setenv("RMM_OIDC_CLIENT_ID", "rmm-console")
	t.Setenv("RMM_OIDC_ISSUER", "http://idp.example.test")
	if _, err := Load(); err == nil {
		t.Fatal("single sign-on started against a plain-HTTP issuer")
	}

	t.Setenv("RMM_OIDC_ISSUER", "https://idp.example.test/")
	t.Setenv("RMM_PUBLIC_URL", "https://rmm.example.test")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDCIssuer != "https://idp.example.test" {
		t.Errorf("issuer = %q, want the trailing slash trimmed to match discovery", cfg.OIDCIssuer)
	}
	// Derived rather than demanded: one fewer thing to get wrong, and it has to
	// match what was registered with the provider.
	if want := "https://rmm.example.test/api/auth/oidc/callback"; cfg.OIDCRedirectURL != want {
		t.Errorf("redirect URL = %q, want %q", cfg.OIDCRedirectURL, want)
	}
}

// Without the openid scope a provider returns no ID token at all, and there is
// nothing to verify. Adding it back beats failing at the first sign-in with a
// message nobody can act on.
func TestOIDCAlwaysRequestsTheOpenIDScope(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_OIDC_ENABLED", "true")
	t.Setenv("RMM_OIDC_ISSUER", "https://idp.example.test")
	t.Setenv("RMM_OIDC_CLIENT_ID", "rmm-console")
	t.Setenv("RMM_PUBLIC_URL", "https://rmm.example.test")
	t.Setenv("RMM_OIDC_SCOPES", "profile,email")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.OIDCScopes) == 0 || cfg.OIDCScopes[0] != "openid" {
		t.Errorf("scopes = %v, want openid first", cfg.OIDCScopes)
	}
}

// Auto-provisioning plus an admin default would make everyone the provider will
// authenticate an administrator here — for a Google Workspace or Entra tenant,
// the whole company. Refuse the combination rather than log a warning nobody
// reads.
func TestOIDCRefusesAutoProvisionedAdmins(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_OIDC_ENABLED", "true")
	t.Setenv("RMM_OIDC_ISSUER", "https://idp.example.test")
	t.Setenv("RMM_OIDC_CLIENT_ID", "rmm-console")
	t.Setenv("RMM_PUBLIC_URL", "https://rmm.example.test")
	t.Setenv("RMM_OIDC_AUTO_PROVISION", "true")
	t.Setenv("RMM_OIDC_DEFAULT_ROLE", "admin")

	if _, err := Load(); err == nil {
		t.Fatal("auto-provisioning into the admin role was accepted")
	}

	t.Setenv("RMM_OIDC_DEFAULT_ROLE", "readonly")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with a safe default role: %v", err)
	}
}

// Off is off: nothing about a half-configured provider should stop the server
// from starting when the feature was never turned on.
func TestOIDCDisabledIgnoresItsOwnSettings(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_OIDC_ENABLED", "false")
	t.Setenv("RMM_OIDC_ISSUER", "http://not-even-https")
	t.Setenv("RMM_OIDC_CLIENT_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDCEnabled {
		t.Error("OIDC reported as enabled")
	}
}
