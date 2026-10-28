package config

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Config holds all runtime configuration, loaded from environment variables.
type Config struct {
	HTTPAddr string // address the HTTP/API server listens on
	// PublicURL is the externally reachable agents origin, returned to the console
	// for enrollment commands. Installed agents derive relay URLs from the
	// WebSocket origin passed as their -server setting.
	PublicURL string
	// MCPPublicURL is the externally reachable OAuth issuer/resource origin.
	// It is intentionally separate from PublicURL in split-ingress deployments.
	MCPPublicURL string
	// UpdatePublicKeys are the Ed25519 keys this server's /dl artifacts are
	// signed with (RMM_UPDATE_PUBLIC_KEY, comma/space separated during a key
	// rotation). The console puts them in the install command it shows, because
	// the installers refuse a production install without a key to pin — and a
	// command a technician copies must work as copied.
	UpdatePublicKeys []string
	// ManifestPublicKeys are the ECDSA P-256 keys the /dl manifest's second
	// signature (SHA256SUMS.p256.sig) is made with (RMM_MANIFEST_PUBLIC_KEY,
	// base64 SPKI DER, separated like the update keys). The console adds them to
	// the Linux/macOS install command: install.sh checks that signature where
	// the shell's OpenSSL cannot check Ed25519 (a stock macOS, OpenSSL 1.1).
	ManifestPublicKeys []string
	DatabaseURL        string // PostgreSQL connection string

	// Bootstrap admin API token. On first start this is accepted as a valid
	// bearer token so you can create real tokens / enroll agents.
	AdminToken string

	// AI / LLM provider (OpenAI-compatible). Works with OpenAI, Anthropic via a
	// proxy, or local models served by Ollama / vLLM (BYO-model).
	AIProvider string
	AIBaseURL  string // e.g. https://api.openai.com/v1 or http://localhost:11434/v1
	AIAPIKey   string
	AIModel    string

	// Remote access defaults.
	DefaultRemoteProvider string // native | meshcentral
	MeshCentralURL        string
	// MeshCentralTokenKey is the server's loginTokenKey (hex). When set, the
	// meshcentral provider mints a login token for seamless iframe embedding
	// (no second login). When empty, it falls back to a deep link.
	MeshCentralTokenKey string
	// MeshCentralUser is the MeshCentral username the login token authenticates
	// as (default "admin"); the userid becomes "user//<user>".
	MeshCentralUser string
	// MeshCentralEnabled toggles the MeshCentral remote provider. On by default;
	// set RMM_MESHCENTRAL_ENABLED=false (or the Settings toggle) to hide it.
	MeshCentralEnabled bool
	// RustDeskEnabled turns on the RustDesk remote provider. The RustDesk client
	// is installed out-of-band; the agent only reads its id on demand and the UI
	// opens rustdesk://<id>. Off by default.
	RustDeskEnabled bool

	// SMTP* configure the account mail transport: password resets only. Unlike
	// the AI and MeshCentral settings this one is env-only and has no console
	// form. The server operator controls the password-reset sender; an editable
	// copy in app_settings would let a settings administrator redirect
	// authentication mail. Alert mail is the tenant's own, set in the console.
	SMTPHost       string
	SMTPPort       int
	SMTPUsername   string
	SMTPPassword   string
	SMTPFrom       string
	SMTPFromName   string
	SMTPTLSMode    string
	SMTPSkipVerify bool
	// SMTPAlertsAllowPrivate lets the console's alerts relay be a private,
	// loopback or link-local address. Off by default: that relay is typed in by
	// a settings administrator, and the server dialling wherever they point it
	// — with the answer shown back by the test button — is a way to probe the
	// network the server sits in. A self-hosted instance with an internal relay
	// turns it on.
	SMTPAlertsAllowPrivate bool

	// AlertsSMTP* configure the operator's shared alert service: the relay an
	// instance's alert mail uses when its administrators set none of their
	// own. Env-only, like the account transport, and with a fixed sender the
	// tenant cannot change — the alert text is the tenant's, the domain is the
	// operator's. Through it, mail goes only to confirmed addresses, and at
	// most AlertsHourlyLimit messages an hour leave an instance.
	AlertsSMTPHost     string
	AlertsSMTPPort     int
	AlertsSMTPUsername string
	AlertsSMTPPassword string
	AlertsSMTPFrom     string
	AlertsSMTPFromName string
	AlertsSMTPTLSMode  string
	AlertsHourlyLimit  int

	// Security.
	// MCPAllowedIPs is an optional allowlist (comma-separated IPs/CIDRs) for the
	// /mcp endpoint. Empty = no app-level IP restriction (deployer's choice).
	MCPAllowedIPs []string
	// AllowUnapprovedJobs lets ad-hoc job creators set require_approval=false.
	// Default false: every human/AI-initiated script run requires approval.
	AllowUnapprovedJobs bool
	// MacOSAdhocBundle offers the ad-hoc macOS remote bundle next to the signed
	// one in the console's install command (docs/08-remote-access.md). On by
	// default, for an operator who builds the sidecar without the project's
	// certificate; a managed service whose Macs all take the signed bundle
	// turns it off (RMM_MACOS_ADHOC_BUNDLE=false).
	MacOSAdhocBundle bool
	// TrustProxyHeaders enables reading X-Forwarded-For for the IP allowlist
	// (only enable when behind a trusted reverse proxy).
	TrustProxyHeaders bool
	// AllowLegacyAgentRelay permits pre-ticket sidecars to send their agent ticket
	// in a relay URL during a controlled rollout. Keep false outside that window.
	AllowLegacyAgentRelay bool
	// EnforceConsoleRelayIngress requires browser relay peers to enter through a
	// proxy-marked console door. Enable it for the split-ingress deployment.
	EnforceConsoleRelayIngress bool
	// MCPOAuthEnabled turns on the built-in OAuth 2.1 Authorization Server for
	// MCP (RMM acts as its own AS + Resource Server), so cloud MCP clients
	// (Claude web, ChatGPT) can connect via the standard OAuth connector flow
	// instead of a pasted static token. Off by default. MCPPublicURL MUST be an
	// HTTPS URL when enabled.
	MCPOAuthEnabled bool
	// CORSOrigins is the allowed browser origins for the API (comma-separated).
	// Default ["*"] (fine for bearer-token auth, which doesn't use cookies).
	CORSOrigins []string

	// Encryption key for secrets stored in app_settings. If empty, runtime
	// settings fall back to deriving a key from AdminToken.
	SettingsEncryptionKey string

	// APIRateLimit is the per-IP request budget per minute for the general
	// authenticated API. It's a safety net against token-guessing, not a
	// per-user quota — the single-page UI is chatty (polling + many parallel
	// queries on navigation), so this needs headroom. Default 600.
	APIRateLimit int

	// MetricsToken guards the Prometheus /metrics endpoint. When empty (default)
	// the endpoint is disabled; when set, scrapers must send it as a bearer token.
	MetricsToken string

	// SupportEndpoint is where a self-hosted console with an OpenRMM support key
	// reports its device count (internal/supportlink). Without a key nothing is
	// ever sent. RMM_SUPPORT_ENDPOINT points it at a staging portal.
	SupportEndpoint string

	// Version is the release the server binary was built from (main.version,
	// stamped at build time). Not read from the environment: it describes the
	// binary, and an operator setting it would only make it wrong.
	Version string

	// Monitoring history retention. Only resolved/terminal rows are purged;
	// active alerts, pending executions and queued mail are never age-deleted.
	AlertRetentionDays        int
	CheckResultRetentionDays  int
	NotificationRetentionDays int
	// AuditRetentionDays bounds only the MACHINE-written audit entries
	// (job.automated, job.result). Everything a person did is kept regardless of
	// age — that record is the reason the log exists, and it is small. 0 keeps
	// even the machine entries forever.
	AuditRetentionDays int

	// Sign in through an external identity provider (OpenID Connect). Off by
	// default: a console that suddenly accepts a third party's word about who
	// someone is should do so because an operator said to.
	//
	// It answers "who is this" and never "what may they do". Roles and access
	// scope stay in this database, so a provider cannot promote itself to admin.
	OIDCEnabled bool
	// Issuer URL, exactly as the provider publishes it. Discovery appends
	// /.well-known/openid-configuration; the value in that document must match
	// this string or the tokens are rejected.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCScopes       []string
	OIDCACRValues    []string
	OIDCRequiredAMR  []string
	// Where the provider sends the browser back. Defaults to PublicURL +
	// /api/auth/oidc/callback, and must be registered with the provider.
	OIDCRedirectURL string
	// Which claim becomes the console username. 'email' for most providers;
	// 'preferred_username' where addresses are not stable.
	OIDCUsernameClaim string
	// Create a console account the first time someone authenticates
	// successfully. Off by default, and that default is the security-relevant
	// one: with it on, everyone your provider will authenticate has an account
	// here, which for a Google Workspace or Entra tenant is the whole company.
	OIDCAutoProvision bool
	// The role such an account is created with. Never taken from a claim.
	OIDCDefaultRole string
	// Label on the console's sign-in button, so it reads "Sign in with Okta"
	// rather than something generic.
	OIDCButtonLabel string
	// Whether the sign-in screen shows that button (RMM_OIDC_SHOW_BUTTON, on by
	// default). The first step already sends every linked or marked account to
	// the provider; the button is how a local account that is neither yet does
	// its first single sign-on and gets linked. A managed service whose only
	// single sign-on account is its marked owner turns it off. With automatic
	// accounts it is always shown: a first-time user has nothing to recognise.
	OIDCShowButton bool
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt reads an integer env var, falling back to def when unset or invalid.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// secretEnv reads either KEY or KEY_FILE. File-backed values keep secrets out
// of container metadata and process environments.
func secretEnv(key string) (string, error) {
	value, valueSet := os.LookupEnv(key)
	path, fileSet := os.LookupEnv(key + "_FILE")
	if valueSet && fileSet && path != "" {
		return "", fmt.Errorf("%s and %s_FILE are mutually exclusive", key, key)
	}
	if !fileSet || path == "" {
		return value, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", key, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func databaseURLWithPassword(raw, password string) (string, error) {
	if password == "" {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse RMM_DATABASE_URL: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return "", fmt.Errorf("RMM_DATABASE_URL must include a username when RMM_DATABASE_PASSWORD_FILE is set")
	}
	u.User = url.UserPassword(u.User.Username(), password)
	return u.String(), nil
}

// Load reads configuration from the environment and validates required fields.
func Load() (*Config, error) {
	adminToken, err := secretEnv("RMM_ADMIN_TOKEN")
	if err != nil {
		return nil, err
	}
	settingsKey, err := secretEnv("RMM_SETTINGS_ENCRYPTION_KEY")
	if err != nil {
		return nil, err
	}
	aiAPIKey, err := secretEnv("RMM_AI_API_KEY")
	if err != nil {
		return nil, err
	}
	meshTokenKey, err := secretEnv("RMM_MESHCENTRAL_TOKEN_KEY")
	if err != nil {
		return nil, err
	}
	// _FILE-capable, like every other secret here: a client secret in a
	// container's environment is a client secret in `docker inspect`.
	oidcSecret, err := secretEnv("RMM_OIDC_CLIENT_SECRET")
	if err != nil {
		return nil, err
	}
	smtpPassword, err := secretEnv("RMM_SMTP_PASSWORD")
	if err != nil {
		return nil, err
	}
	alertsSMTPPassword, err := secretEnv("RMM_ALERTS_SMTP_PASSWORD")
	if err != nil {
		return nil, err
	}
	databasePassword, err := secretEnv("RMM_DATABASE_PASSWORD")
	if err != nil {
		return nil, err
	}
	databaseURL, err := databaseURLWithPassword(env("RMM_DATABASE_URL", "postgres://rmm:rmm@localhost:5432/rmm?sslmode=disable"), databasePassword)
	if err != nil {
		return nil, err
	}

	c := &Config{
		HTTPAddr:                   env("RMM_HTTP_ADDR", ":8080"),
		PublicURL:                  env("RMM_PUBLIC_URL", "http://localhost:8080"),
		MCPPublicURL:               strings.TrimRight(os.Getenv("RMM_MCP_PUBLIC_URL"), "/"),
		DatabaseURL:                databaseURL,
		AdminToken:                 adminToken,
		AIProvider:                 env("RMM_AI_PROVIDER", "openai-compatible"),
		AIBaseURL:                  env("RMM_AI_BASE_URL", "https://api.openai.com/v1"),
		AIAPIKey:                   aiAPIKey,
		AIModel:                    env("RMM_AI_MODEL", "gpt-4o-mini"),
		DefaultRemoteProvider:      env("RMM_REMOTE_PROVIDER", "native"),
		MeshCentralURL:             os.Getenv("RMM_MESHCENTRAL_URL"),
		MeshCentralTokenKey:        meshTokenKey,
		MeshCentralUser:            env("RMM_MESHCENTRAL_USER", "admin"),
		MeshCentralEnabled:         os.Getenv("RMM_MESHCENTRAL_ENABLED") != "false",
		RustDeskEnabled:            os.Getenv("RMM_RUSTDESK_ENABLED") == "true",
		SMTPHost:                   strings.TrimSpace(os.Getenv("RMM_SMTP_HOST")),
		SMTPPort:                   envInt("RMM_SMTP_PORT", 587),
		SMTPUsername:               os.Getenv("RMM_SMTP_USERNAME"),
		SMTPPassword:               smtpPassword,
		SMTPFrom:                   strings.TrimSpace(os.Getenv("RMM_SMTP_FROM")),
		SMTPFromName:               env("RMM_SMTP_FROM_NAME", "OpenRMM"),
		SMTPTLSMode:                env("RMM_SMTP_TLS_MODE", "starttls"),
		SMTPSkipVerify:             os.Getenv("RMM_SMTP_SKIP_VERIFY") == "true",
		SMTPAlertsAllowPrivate:     os.Getenv("RMM_SMTP_ALERTS_ALLOW_PRIVATE") == "true",
		AlertsSMTPHost:             strings.TrimSpace(os.Getenv("RMM_ALERTS_SMTP_HOST")),
		AlertsSMTPPort:             envInt("RMM_ALERTS_SMTP_PORT", 587),
		AlertsSMTPUsername:         os.Getenv("RMM_ALERTS_SMTP_USERNAME"),
		AlertsSMTPPassword:         alertsSMTPPassword,
		AlertsSMTPFrom:             strings.TrimSpace(os.Getenv("RMM_ALERTS_SMTP_FROM")),
		AlertsSMTPFromName:         env("RMM_ALERTS_SMTP_FROM_NAME", "OpenRMM Alerts"),
		AlertsSMTPTLSMode:          env("RMM_ALERTS_SMTP_TLS_MODE", "starttls"),
		AlertsHourlyLimit:          envInt("RMM_ALERTS_HOURLY_LIMIT", 120),
		MCPAllowedIPs:              splitCSV(os.Getenv("RMM_MCP_ALLOWED_IPS")),
		AllowUnapprovedJobs:        os.Getenv("RMM_ALLOW_UNAPPROVED_JOBS") == "true",
		MacOSAdhocBundle:           os.Getenv("RMM_MACOS_ADHOC_BUNDLE") != "false",
		TrustProxyHeaders:          os.Getenv("RMM_TRUST_PROXY_HEADERS") == "true",
		AllowLegacyAgentRelay:      os.Getenv("RMM_ALLOW_LEGACY_AGENT_RELAY") == "true",
		EnforceConsoleRelayIngress: os.Getenv("RMM_ENFORCE_CONSOLE_RELAY_INGRESS") == "true",
		MCPOAuthEnabled:            os.Getenv("RMM_MCP_OAUTH_ENABLED") == "true",
		CORSOrigins:                splitCSV(env("RMM_CORS_ORIGINS", "*")),
		SettingsEncryptionKey:      settingsKey,
		APIRateLimit:               envInt("RMM_API_RATE_LIMIT", 600),
		MetricsToken:               os.Getenv("RMM_METRICS_TOKEN"),
		SupportEndpoint:            env("RMM_SUPPORT_ENDPOINT", "https://openrmm.io/api/support/usage"),
		AlertRetentionDays:         envInt("RMM_ALERT_RETENTION_DAYS", 90),
		CheckResultRetentionDays:   envInt("RMM_CHECK_RESULT_RETENTION_DAYS", 90),
		NotificationRetentionDays:  envInt("RMM_NOTIFICATION_RETENTION_DAYS", 90),
		AuditRetentionDays:         envInt("RMM_AUDIT_RETENTION_DAYS", 90),

		OIDCEnabled:       os.Getenv("RMM_OIDC_ENABLED") == "true",
		OIDCIssuer:        strings.TrimRight(os.Getenv("RMM_OIDC_ISSUER"), "/"),
		OIDCClientID:      os.Getenv("RMM_OIDC_CLIENT_ID"),
		OIDCClientSecret:  oidcSecret,
		OIDCScopes:        splitCSV(env("RMM_OIDC_SCOPES", "openid,profile,email")),
		OIDCACRValues:     splitCSV(os.Getenv("RMM_OIDC_ACR_VALUES")),
		OIDCRequiredAMR:   splitCSV(os.Getenv("RMM_OIDC_REQUIRED_AMR")),
		OIDCRedirectURL:   os.Getenv("RMM_OIDC_REDIRECT_URL"),
		OIDCUsernameClaim: env("RMM_OIDC_USERNAME_CLAIM", "email"),
		OIDCAutoProvision: os.Getenv("RMM_OIDC_AUTO_PROVISION") == "true",
		OIDCDefaultRole:   env("RMM_OIDC_DEFAULT_ROLE", "readonly"),
		OIDCButtonLabel:   env("RMM_OIDC_BUTTON_LABEL", "Single sign-on"),
		OIDCShowButton:    os.Getenv("RMM_OIDC_SHOW_BUTTON") != "false",
	}
	keys, err := parseUpdatePublicKeys(os.Getenv("RMM_UPDATE_PUBLIC_KEY"))
	if err != nil {
		return nil, err
	}
	c.UpdatePublicKeys = keys
	manifestKeys, err := parseManifestPublicKeys(os.Getenv("RMM_MANIFEST_PUBLIC_KEY"))
	if err != nil {
		return nil, err
	}
	c.ManifestPublicKeys = manifestKeys
	if c.AdminToken == "" {
		return nil, fmt.Errorf("RMM_ADMIN_TOKEN is required (set a long random string, e.g. `openssl rand -hex 32`)")
	}
	if len(c.AdminToken) < 32 {
		return nil, fmt.Errorf("RMM_ADMIN_TOKEN is too weak: use at least 32 characters (e.g. `openssl rand -hex 32`)")
	}
	if os.Getenv("RMM_REQUIRE_SETTINGS_ENCRYPTION_KEY") == "true" && len(c.SettingsEncryptionKey) < 32 {
		return nil, fmt.Errorf("RMM_SETTINGS_ENCRYPTION_KEY is required and must contain at least 32 characters")
	}
	if c.MCPOAuthEnabled {
		if c.MCPPublicURL == "" {
			return nil, fmt.Errorf("RMM_MCP_PUBLIC_URL is required when RMM_MCP_OAUTH_ENABLED=true")
		}
		if !strings.HasPrefix(strings.ToLower(c.MCPPublicURL), "https://") {
			return nil, fmt.Errorf("RMM_MCP_PUBLIC_URL must use https:// when RMM_MCP_OAUTH_ENABLED=true")
		}
	}
	if c.OIDCEnabled {
		if c.OIDCIssuer == "" || c.OIDCClientID == "" {
			return nil, fmt.Errorf("RMM_OIDC_ISSUER and RMM_OIDC_CLIENT_ID are required when RMM_OIDC_ENABLED=true")
		}
		// Discovery, the token exchange and the JWKS fetch all run over this
		// URL. Plain HTTP would put the client secret and the code on the wire.
		if !strings.HasPrefix(strings.ToLower(c.OIDCIssuer), "https://") {
			return nil, fmt.Errorf("RMM_OIDC_ISSUER must use https://")
		}
		if c.OIDCRedirectURL == "" {
			if c.PublicURL == "" {
				return nil, fmt.Errorf("RMM_OIDC_REDIRECT_URL is required when RMM_PUBLIC_URL is not set")
			}
			c.OIDCRedirectURL = strings.TrimRight(c.PublicURL, "/") + "/api/auth/oidc/callback"
		}
		if u, err := url.Parse(c.OIDCRedirectURL); err != nil || !u.IsAbs() {
			return nil, fmt.Errorf("RMM_OIDC_REDIRECT_URL must be an absolute URL")
		}
		// 'openid' is what makes it OIDC rather than plain OAuth: without it a
		// provider returns no ID token and there is nothing to verify.
		if !slices.Contains(c.OIDCScopes, "openid") {
			c.OIDCScopes = append([]string{"openid"}, c.OIDCScopes...)
		}
		// A role arriving from configuration is one thing; a role arriving from
		// the provider is another, and is never read. Refuse a default that
		// would silently hand out more than intended.
		if c.OIDCAutoProvision && c.OIDCDefaultRole == "admin" {
			return nil, fmt.Errorf("RMM_OIDC_DEFAULT_ROLE=admin with RMM_OIDC_AUTO_PROVISION=true would make every account your provider authenticates an administrator here; create the first admins by hand")
		}
	}
	if c.EnforceConsoleRelayIngress && !c.TrustProxyHeaders {
		return nil, fmt.Errorf("RMM_ENFORCE_CONSOLE_RELAY_INGRESS requires RMM_TRUST_PROXY_HEADERS=true behind a trusted proxy")
	}
	return c, nil
}

// splitCSV splits a comma-separated list, trimming spaces and dropping blanks.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseManifestPublicKeys takes base64 SPKI DER ECDSA P-256 public keys,
// separated like the update keys. A key of another type or curve is refused
// here, rather than by every installer the console's command reaches.
func parseManifestPublicKeys(raw string) ([]string, error) {
	var keys []string
	for i, k := range strings.FieldsFunc(raw, keySeparator) {
		der, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			return nil, fmt.Errorf("RMM_MANIFEST_PUBLIC_KEY: entry %d is not base64", i+1)
		}
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, fmt.Errorf("RMM_MANIFEST_PUBLIC_KEY: entry %d is not a public key: %v", i+1, err)
		}
		if ec, ok := pub.(*ecdsa.PublicKey); !ok || ec.Curve != elliptic.P256() {
			return nil, fmt.Errorf("RMM_MANIFEST_PUBLIC_KEY: entry %d is not an ECDSA P-256 key", i+1)
		}
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func keySeparator(r rune) bool {
	return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// parseUpdatePublicKeys takes base64 Ed25519 public keys, separated by commas,
// semicolons or whitespace — the form every signing tool here prints. Hex is
// refused although the agent's flag mentions it: a 64-character hex key is also
// valid base64, which the agent tries first and then rejects as 48 bytes, so a
// hex key would produce a command that fails on every endpoint. A bad key stops
// the server instead of reaching a technician's clipboard.
func parseUpdatePublicKeys(raw string) ([]string, error) {
	var keys []string
	for i, k := range strings.FieldsFunc(raw, keySeparator) {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("RMM_UPDATE_PUBLIC_KEY: entry %d is not a base64 Ed25519 public key", i+1)
		}
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
