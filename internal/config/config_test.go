package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
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

func TestOIDCAuthenticationContextSettings(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_OIDC_ENABLED", "false")
	t.Setenv("RMM_OIDC_ACR_VALUES", " urn:example:acr:mfa, another-context ,")
	t.Setenv("RMM_OIDC_REQUIRED_AMR", " otp, mfa ,")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.OIDCACRValues, "|"); got != "urn:example:acr:mfa|another-context" {
		t.Fatalf("ACR = %q", got)
	}
	if got := strings.Join(cfg.OIDCRequiredAMR, "|"); got != "otp|mfa" {
		t.Fatalf("AMR = %q", got)
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

// The console builds its install command from these keys, and the installers
// refuse a production install without one, so a malformed value must stop the
// server rather than reach a technician's clipboard.
// The ad-hoc macOS bundle is offered unless the operator turns it off.
func TestMacOSAdhocBundle(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	for value, want := range map[string]bool{"": true, "true": true, "false": false} {
		t.Setenv("RMM_MACOS_ADHOC_BUNDLE", value)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.MacOSAdhocBundle != want {
			t.Errorf("RMM_MACOS_ADHOC_BUNDLE=%q: offered=%v, want %v", value, c.MacOSAdhocBundle, want)
		}
	}
}

func TestUpdatePublicKeys(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	keyA := base64.StdEncoding.EncodeToString(make([]byte, 32))
	keyB := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))

	t.Setenv("RMM_UPDATE_PUBLIC_KEY", "")
	c, err := Load()
	if err != nil || len(c.UpdatePublicKeys) != 0 {
		t.Fatalf("unset: keys=%v err=%v", c.UpdatePublicKeys, err)
	}

	// A rotation carries two, in whatever separator the installers accept;
	// a repeated key is listed once.
	t.Setenv("RMM_UPDATE_PUBLIC_KEY", keyA+", "+keyB+";"+keyA)
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.UpdatePublicKeys, ","); got != keyA+","+keyB {
		t.Fatalf("keys = %q", got)
	}

	// Hex included: a 64-character hex key is valid base64 of the wrong length,
	// and the agent would reject it after the install command was copied.
	hexKey := strings.Repeat("ab", 32)
	for _, bad := range []string{"not-a-key", base64.StdEncoding.EncodeToString(make([]byte, 31)), keyA + ",zz", hexKey} {
		t.Setenv("RMM_UPDATE_PUBLIC_KEY", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RMM_UPDATE_PUBLIC_KEY") {
			t.Fatalf("%q: err = %v, want a refusal naming RMM_UPDATE_PUBLIC_KEY", bad, err)
		}
	}
}

// RMM_MANIFEST_PUBLIC_KEY: base64 SPKI DER ECDSA P-256 keys, separated like the
// update keys. Anything else stops the server, rather than reaching an install
// command that fails on every Mac without OpenSSL 3.
func TestManifestPublicKeys(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("RMM_UPDATE_PUBLIC_KEY", "")
	spki := func(curve elliptic.Curve) string {
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(der)
	}
	keyA, keyB := spki(elliptic.P256()), spki(elliptic.P256())

	t.Setenv("RMM_MANIFEST_PUBLIC_KEY", "")
	c, err := Load()
	if err != nil || len(c.ManifestPublicKeys) != 0 {
		t.Fatalf("unset: keys=%v err=%v", c.ManifestPublicKeys, err)
	}
	t.Setenv("RMM_MANIFEST_PUBLIC_KEY", keyA+", "+keyB+";"+keyA)
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.ManifestPublicKeys, ","); got != keyA+","+keyB {
		t.Fatalf("keys = %q", got)
	}

	ed := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, bad := range []string{"not-a-key", ed, spki(elliptic.P384()), keyA + ",zz"} {
		t.Setenv("RMM_MANIFEST_PUBLIC_KEY", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RMM_MANIFEST_PUBLIC_KEY") {
			t.Fatalf("%q: err = %v, want a refusal naming RMM_MANIFEST_PUBLIC_KEY", bad, err)
		}
	}
}

// The single sign-on button is shown unless RMM_OIDC_SHOW_BUTTON=false: the
// first login step sends linked and marked accounts to the provider, and the
// button is how a local account does its first single sign-on.
func TestOIDCShowButton(t *testing.T) {
	t.Setenv("RMM_ADMIN_TOKEN", strings.Repeat("a", 32))
	for value, want := range map[string]bool{"": true, "true": true, "false": false} {
		t.Setenv("RMM_OIDC_SHOW_BUTTON", value)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.OIDCShowButton != want {
			t.Errorf("RMM_OIDC_SHOW_BUTTON=%q: %v, want %v", value, c.OIDCShowButton, want)
		}
	}
}
