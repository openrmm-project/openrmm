package mcpserver

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// MCP is the surface where an *external* assistant — Claude Desktop, ChatGPT,
// anything the operator connects — drives the tools. TestToolCallReevaluatesPermissions
// covers permissions, but nothing covered scope: the Identity carries a ScopeUser
// that buildServer must thread into every ToolContext it constructs. Dropping
// that one field would compile, keep every permission test green, and hand an
// external client the whole fleet.
//
// These run the real tools against a real database, over a real MCP session.

type nilSender struct{}

func (nilSender) Send(string, []byte) bool { return false }

type mcpFixture struct {
	ctx        context.Context
	st         *store.Store
	js         *jobs.Service
	mineHost   string
	theirHost  string
	theirAgent string
	scopedUser string
}

func newMCPFixture(t *testing.T) mcpFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st, err := store.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	mine, err := st.CreateClient(ctx, "mine-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := st.CreateClient(ctx, "theirs-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	mineSite, err := st.CreateSite(ctx, mine.ID, "mine-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirSite, err := st.CreateSite(ctx, theirs.ID, "their-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	f := mcpFixture{ctx: ctx, st: st, js: jobs.New(st, nilSender{})}
	f.mineHost, f.theirHost = "mine-host-"+suffix, "their-host-"+suffix

	if _, err := st.CreateAgent(ctx, f.mineHost, "linux", "amd64", "1.0.0", "tok-mine-"+suffix, &mineSite.ID, nil); err != nil {
		t.Fatal(err)
	}
	theirAgent, err := st.CreateAgent(ctx, f.theirHost, "linux", "amd64", "1.0.0", "tok-theirs-"+suffix, &theirSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.theirAgent = theirAgent.ID

	scoped, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: mine.ID}})
	if err != nil {
		t.Fatal(err)
	}
	f.scopedUser = scoped.ID
	return f
}

func (f mcpFixture) identity() Identity {
	return Identity{
		Actor: "tech", ScopeUser: f.scopedUser, Principal: "token:test",
		Permissions: map[string]bool{
			models.PermMCPUse:      true,
			models.PermDevicesView: true,
			models.PermJobsCreate:  true,
			models.PermJobsView:    true,
			models.PermReportsView: true,
		},
	}
}

// session wires an in-memory MCP client to a server built for one identity, the
// way Handler does per connection.
func (f mcpFixture) session(t *testing.T, id Identity) *mcp.ClientSession {
	t.Helper()
	server := buildServer(f.st, f.js, nil, &config.Config{}, id, func(context.Context) (Identity, error) { return id, nil })
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(f.ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(f.ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientSession.Close() })
	return clientSession
}

func callText(t *testing.T, ctx context.Context, s *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	result, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s returned no content", name)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s returned %T, want text", name, result.Content[0])
	}
	return text.Text
}

// An external MCP client bound to a restricted credential enumerates only that
// credential's fleet.
func TestMCPToolsRespectTheCredentialScopeIntegration(t *testing.T) {
	f := newMCPFixture(t)
	s := f.session(t, f.identity())

	devices := callText(t, f.ctx, s, "list_devices", map[string]any{})
	if !strings.Contains(devices, f.mineHost) {
		t.Errorf("list_devices hid an in-scope machine over MCP: %s", devices)
	}
	if strings.Contains(devices, f.theirHost) || strings.Contains(devices, f.theirAgent) {
		t.Errorf("list_devices leaked an out-of-scope machine over MCP: %s", devices)
	}

	report := callText(t, f.ctx, s, "inventory_report", map[string]any{"report_type": "machines"})
	if strings.Contains(report, f.theirHost) {
		t.Errorf("inventory_report leaked an out-of-scope machine over MCP: %s", report)
	}
}

// And the mutating tool: naming another tenant's device id over MCP is refused,
// with nothing queued.
func TestMCPProposeScriptRefusesAnOutOfScopeDeviceIntegration(t *testing.T) {
	f := newMCPFixture(t)
	s := f.session(t, f.identity())

	out := callText(t, f.ctx, s, "propose_script", map[string]any{
		"agent_id": f.theirAgent, "shell": "bash", "script": "echo hi", "description": "test",
	})
	if !strings.Contains(out, "outside your access scope") {
		t.Fatalf("MCP accepted an out-of-scope device: %s", out)
	}
	queued, err := f.st.ListJobs(f.ctx, f.theirAgent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("the refused MCP proposal still queued %d job(s)", len(queued))
	}
}

// An unscoped credential is the control: the same session shape sees everything,
// so the assertions above measure the scope rather than an empty fixture.
func TestMCPUnscopedCredentialSeesTheWholeFleetIntegration(t *testing.T) {
	f := newMCPFixture(t)
	id := f.identity()
	id.ScopeUser = "" // unrestricted principal
	s := f.session(t, id)

	devices := callText(t, f.ctx, s, "list_devices", map[string]any{})
	if !strings.Contains(devices, f.mineHost) || !strings.Contains(devices, f.theirHost) {
		t.Errorf("an unscoped credential should see the whole fleet: %s", devices)
	}
}

// The catalog itself is filtered: a credential without a permission must not even
// be told the tool exists, or the model will keep proposing it.
func TestMCPCatalogHidesToolsTheCredentialCannotUseIntegration(t *testing.T) {
	f := newMCPFixture(t)
	id := f.identity()
	delete(id.Permissions, models.PermJobsCreate)
	s := f.session(t, id)

	tools, err := s.ListTools(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "propose_script" {
			t.Fatal("propose_script was advertised to a credential without jobs.create")
		}
	}
}
