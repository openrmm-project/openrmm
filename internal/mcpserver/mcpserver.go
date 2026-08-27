// Package mcpserver exposes the RMM's AI tools as a Model Context Protocol (MCP)
// server over Streamable HTTP, so external MCP clients — Claude (Desktop/web),
// ChatGPT, and any other MCP-capable assistant — can manage the fleet directly.
//
// It reuses the EXACT same tool catalog and implementations as the built-in AI
// chat (ai.Tools / ai.Execute), so the two surfaces never drift: the human
// approval guardrail on propose_script applies identically here.
package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/store"
)

const serverVersion = "0.1.0"

const sessionTimeout = 15 * time.Minute

// Identity is the current authorization state of an MCP credential.
type Identity struct {
	Actor       string
	ScopeUser   string
	Principal   string
	Permissions map[string]bool
}

// IdentifyFunc extracts the authenticated principal from a request: the actor
// (for audit), the scope-user id (empty = full access), and permission set. The
// API auth middleware populates these before the MCP handler runs.
type IdentifyFunc func(*http.Request) Identity

// AuthorizeFunc reloads the credential's current role, permissions and scope.
// Tool calls use it so role changes and token revocation take effect immediately.
type AuthorizeFunc func(context.Context) (Identity, error)

// Handler returns an http.Handler serving the MCP endpoint over Streamable HTTP.
// Mount it behind the same bearer auth as the REST API.
func Handler(st *store.Store, js *jobs.Service, ps *patches.Service, identify IdentifyFunc, authorize func(string) AuthorizeFunc) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		identity := identify(r)
		return buildServer(st, js, ps, identity, authorize(r.Header.Get("Authorization")))
	}, &mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})

	// SDK v1.6.1 binds stateful sessions to TokenInfo.UserID. Use a unique
	// credential principal rather than the generic audit actor.
	return mcpauth.RequireBearerToken(func(_ context.Context, _ string, r *http.Request) (*mcpauth.TokenInfo, error) {
		identity := identify(r)
		if identity.Principal == "" {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{UserID: identity.Principal, Expiration: time.Now().Add(sessionTimeout)}, nil
	}, nil)(h)
}

// buildServer constructs a per-session MCP server bound to one credential.
func buildServer(st *store.Store, js *jobs.Service, ps *patches.Service, identity Identity, authorize AuthorizeFunc) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "rmm-ai",
		Title:   "OpenRMM",
		Version: serverVersion,
	}, nil)

	for _, t := range ai.ToolsForPermissions(identity.Permissions) {
		def := t // capture per iteration
		tool := &mcp.Tool{
			Name:        def.Function.Name,
			Description: def.Function.Description,
			InputSchema: json.RawMessage(def.Function.Parameters),
		}
		s.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			current, err := authorize(ctx)
			if err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: `{"error":"authorization expired"}`}},
					IsError: true,
				}, nil
			}
			tc := ai.ToolContext{Store: st, Jobs: js, Patches: ps, Actor: "mcp:" + current.Actor, ScopeUserID: current.ScopeUser, Permissions: current.Permissions}
			res := ai.Execute(ctx, tc, def.Function.Name, req.Params.Arguments)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: res.Output}},
			}, nil
		})
	}
	return s
}
