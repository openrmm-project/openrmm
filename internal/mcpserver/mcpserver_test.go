package mcpserver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/p1ngu/rmm/internal/models"
)

func TestToolCallReevaluatesPermissions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initial := Identity{Permissions: map[string]bool{models.PermJobsCreate: true}}
	server := buildServer(nil, nil, nil, initial, func(context.Context) (Identity, error) {
		return Identity{Permissions: map[string]bool{}}, nil
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "propose_script",
		Arguments: map[string]any{
			"agent_id": "agent", "shell": "bash", "script": "true", "description": "test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "missing permission: "+models.PermJobsCreate) {
		t.Fatalf("tool used stale permissions: %#v", result.Content)
	}
}

func TestSessionTimeoutIsBounded(t *testing.T) {
	if sessionTimeout <= 0 || sessionTimeout > time.Hour {
		t.Fatalf("unsafe MCP session timeout: %v", sessionTimeout)
	}
}

func TestSessionIsBoundToCredentialPrincipal(t *testing.T) {
	identify := func(r *http.Request) Identity {
		return Identity{Principal: r.Header.Get("Authorization"), Permissions: map[string]bool{}}
	}
	h := Handler(nil, nil, nil, identify, func(string) AuthorizeFunc {
		return func(context.Context) (Identity, error) { return Identity{Permissions: map[string]bool{}}, nil }
	})
	initialize := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(initialize))
	req.Header.Set("Authorization", "Bearer principal-a")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", w.Code, w.Body.String())
	}
	sessionID := w.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize did not issue a session id")
	}

	ping := []byte(`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`)
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(ping))
	req.Header.Set("Authorization", "Bearer principal-b")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-principal session reuse status=%d, want 403", w.Code)
	}
}
