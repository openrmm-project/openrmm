package config

import (
	"fmt"
	"net/url"
	"os"
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
	DatabaseURL  string // PostgreSQL connection string

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

	// SMTP* configure the account mail transport: password resets, and the
	// fallback sender for alerts. Unlike the AI and MeshCentral settings this one
	// is env-only and has no console form. A hosted instance sends its tenants
	// password-reset mail from the operator's own domain, and that guarantee
	// cannot survive an editable copy in app_settings — a tenant admin could
	// simply point it elsewhere. Alert mail stays editable in the console.
	SMTPHost       string
	SMTPPort       int
	SMTPUsername   string
	SMTPPassword   string
	SMTPFrom       string
	SMTPFromName   string
	SMTPTLSMode    string
	SMTPSkipVerify bool

	// Security.
	// MCPAllowedIPs is an optional allowlist (comma-separated IPs/CIDRs) for the
	// /mcp endpoint. Empty = no app-level IP restriction (deployer's choice).
	MCPAllowedIPs []string
	// AllowUnapprovedJobs lets ad-hoc job creators set require_approval=false.
	// Default false: every human/AI-initiated script run requires approval.
	AllowUnapprovedJobs bool
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
	smtpPassword, err := secretEnv("RMM_SMTP_PASSWORD")
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
		MCPAllowedIPs:              splitCSV(os.Getenv("RMM_MCP_ALLOWED_IPS")),
		AllowUnapprovedJobs:        os.Getenv("RMM_ALLOW_UNAPPROVED_JOBS") == "true",
		TrustProxyHeaders:          os.Getenv("RMM_TRUST_PROXY_HEADERS") == "true",
		AllowLegacyAgentRelay:      os.Getenv("RMM_ALLOW_LEGACY_AGENT_RELAY") == "true",
		EnforceConsoleRelayIngress: os.Getenv("RMM_ENFORCE_CONSOLE_RELAY_INGRESS") == "true",
		MCPOAuthEnabled:            os.Getenv("RMM_MCP_OAUTH_ENABLED") == "true",
		CORSOrigins:                splitCSV(env("RMM_CORS_ORIGINS", "*")),
		SettingsEncryptionKey:      settingsKey,
		APIRateLimit:               envInt("RMM_API_RATE_LIMIT", 600),
		MetricsToken:               os.Getenv("RMM_METRICS_TOKEN"),
		AlertRetentionDays:         envInt("RMM_ALERT_RETENTION_DAYS", 90),
		CheckResultRetentionDays:   envInt("RMM_CHECK_RESULT_RETENTION_DAYS", 90),
		NotificationRetentionDays:  envInt("RMM_NOTIFICATION_RETENTION_DAYS", 90),
		AuditRetentionDays:         envInt("RMM_AUDIT_RETENTION_DAYS", 90),
	}
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
