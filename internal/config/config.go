package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds all runtime configuration, loaded from environment variables.
type Config struct {
	HTTPAddr    string // address the HTTP/API server listens on
	PublicURL   string // externally reachable base URL (used to build connect URLs)
	DatabaseURL string // PostgreSQL connection string

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
	// RustDeskEnabled turns on the RustDesk remote provider. The RustDesk client
	// is installed out-of-band; the agent only reads its id on demand and the UI
	// opens rustdesk://<id>. Off by default.
	RustDeskEnabled bool

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
	// CORSOrigins is the allowed browser origins for the API (comma-separated).
	// Default ["*"] (fine for bearer-token auth, which doesn't use cookies).
	CORSOrigins []string

	// Encryption key for secrets stored in app_settings. If empty, runtime
	// settings fall back to deriving a key from AdminToken.
	SettingsEncryptionKey string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads configuration from the environment and validates required fields.
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:              env("RMM_HTTP_ADDR", ":8080"),
		PublicURL:             env("RMM_PUBLIC_URL", "http://localhost:8080"),
		DatabaseURL:           env("RMM_DATABASE_URL", "postgres://rmm:rmm@localhost:5432/rmm?sslmode=disable"),
		AdminToken:            os.Getenv("RMM_ADMIN_TOKEN"),
		AIProvider:            env("RMM_AI_PROVIDER", "openai-compatible"),
		AIBaseURL:             env("RMM_AI_BASE_URL", "https://api.openai.com/v1"),
		AIAPIKey:              os.Getenv("RMM_AI_API_KEY"),
		AIModel:               env("RMM_AI_MODEL", "gpt-4o-mini"),
		DefaultRemoteProvider: env("RMM_REMOTE_PROVIDER", "native"),
		MeshCentralURL:        os.Getenv("RMM_MESHCENTRAL_URL"),
		MeshCentralTokenKey:   os.Getenv("RMM_MESHCENTRAL_TOKEN_KEY"),
		MeshCentralUser:       env("RMM_MESHCENTRAL_USER", "admin"),
		RustDeskEnabled:       os.Getenv("RMM_RUSTDESK_ENABLED") == "true",
		MCPAllowedIPs:         splitCSV(os.Getenv("RMM_MCP_ALLOWED_IPS")),
		AllowUnapprovedJobs:   os.Getenv("RMM_ALLOW_UNAPPROVED_JOBS") == "true",
		TrustProxyHeaders:     os.Getenv("RMM_TRUST_PROXY_HEADERS") == "true",
		CORSOrigins:           splitCSV(env("RMM_CORS_ORIGINS", "*")),
		SettingsEncryptionKey: os.Getenv("RMM_SETTINGS_ENCRYPTION_KEY"),
	}
	if c.AdminToken == "" {
		return nil, fmt.Errorf("RMM_ADMIN_TOKEN is required (set a long random string, e.g. `openssl rand -hex 32`)")
	}
	if len(c.AdminToken) < 32 {
		return nil, fmt.Errorf("RMM_ADMIN_TOKEN is too weak: use at least 32 characters (e.g. `openssl rand -hex 32`)")
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
