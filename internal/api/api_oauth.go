package api

import (
	"net/http"
	"strings"
)

// MCP OAuth 2.1 Authorization Server — the RMM acts as its own AS + Resource
// Server so cloud MCP clients (Claude web, ChatGPT) can connect via the standard
// OAuth connector flow instead of a pasted static token. See docs/17-mcp-oauth.md.
//
// This file currently implements the unauthenticated discovery metadata
// (RFC 8414 / RFC 9728), which is how a client learns where to authorize. The
// /oauth/register, /oauth/authorize and /oauth/token endpoints land in the
// following phases. All of it is gated by RMM_MCP_OAUTH_ENABLED (off by default).

// issuerBase is the OAuth issuer — the public origin, trimmed of a trailing slash.
func (s *Server) issuerBase() string {
	return strings.TrimRight(s.cfg.PublicURL, "/")
}

// oauthAuthServerMeta serves RFC 8414 Authorization Server Metadata at
// /.well-known/oauth-authorization-server.
func (s *Server) oauthAuthServerMeta(w http.ResponseWriter, r *http.Request) {
	base := s.issuerBase()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"}, // PKCE required
		"token_endpoint_auth_methods_supported": []string{"none"}, // public clients (PKCE)
		"scopes_supported":                      []string{"mcp"},
	})
}

// oauthProtectedResourceMeta serves RFC 9728 Protected Resource Metadata at
// /.well-known/oauth-protected-resource, pointing MCP clients at this AS.
func (s *Server) oauthProtectedResourceMeta(w http.ResponseWriter, r *http.Request) {
	base := s.issuerBase()
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              base + "/mcp",
		"authorization_servers": []string{base},
	})
}
