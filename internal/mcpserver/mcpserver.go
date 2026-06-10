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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/store"
)

const serverVersion = "0.1.0"

// IdentifyFunc extracts the authenticated principal from a request: the actor
// (for audit) and the scope-user id (empty = full access). The API auth
// middleware populates these before the MCP handler runs.
type IdentifyFunc func(*http.Request) (actor, scopeUser string)

// Handler returns an http.Handler serving the MCP endpoint over Streamable HTTP.
// Mount it behind the same bearer auth as the REST API.
func Handler(st *store.Store, js *jobs.Service, identify IdentifyFunc) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		actor, scopeUser := identify(r)
		return buildServer(st, js, actor, scopeUser)
	}, nil)
}

// buildServer constructs a per-request MCP server bound to the caller's identity.
func buildServer(st *store.Store, js *jobs.Service, actor, scopeUser string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "rmm-ai",
		Title:   "OpenRMM",
		Version: serverVersion,
	}, nil)

	tc := ai.ToolContext{Store: st, Jobs: js, Actor: "mcp:" + actor, ScopeUserID: scopeUser}

	for _, t := range ai.Tools() {
		def := t // capture per iteration
		tool := &mcp.Tool{
			Name:        def.Function.Name,
			Description: def.Function.Description,
			InputSchema: json.RawMessage(def.Function.Parameters),
		}
		s.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res := ai.Execute(ctx, tc, def.Function.Name, req.Params.Arguments)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: res.Output}},
			}, nil
		})
	}
	return s
}
