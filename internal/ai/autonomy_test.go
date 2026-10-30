package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/p1ngu/rmm/internal/models"
)

// Every tool the model can call has a risk class, and no class is kept for a
// tool that no longer exists: a new tool cannot ship undecided.
func TestEveryToolHasARiskClass(t *testing.T) {
	advertised := map[string]bool{}
	for _, tool := range Tools() {
		name := tool.Function.Name
		advertised[name] = true
		if _, ok := ToolRiskClass(name); !ok {
			t.Errorf("tool %q has no risk class", name)
		}
	}
	for name := range toolRiskClass {
		if !advertised[name] {
			t.Errorf("risk class kept for %q, which is not a tool", name)
		}
	}
}

// Only the two autonomy classes can ever act without approval, and every tool
// that proposes a change stays a change.
func TestOnlyQueriesAndReleasesAreAutonomous(t *testing.T) {
	for name, class := range toolRiskClass {
		if autonomyPermission(ChannelChat, class) != "" && name != "query_device" && name != "run_preapproved_action" {
			t.Errorf("%s is in an autonomous class (%s)", name, class)
		}
	}
	for _, name := range []string{"propose_script", "propose_bulk_deploy", "install_software", "uninstall_software",
		"restart_service", "reboot_device", "install_patches", "propose_delete_devices", "propose_move_devices"} {
		if class, _ := ToolRiskClass(name); class != ClassChange {
			t.Errorf("%s must stay a change, is %s", name, class)
		}
	}
}

func TestExecuteRefusesAnUnclassifiedTool(t *testing.T) {
	res := Execute(context.Background(), ToolContext{}, "run_anything", json.RawMessage(`{}`))
	if !strings.Contains(res.Output, "unknown tool") {
		t.Fatalf("output = %s", res.Output)
	}
}

// The chat and MCP switches are independent, a missing permission map grants
// nothing, and an unreadable pause setting counts as paused.
func TestAutonomyDecisionIsPerChannelAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	chatOnly := map[string]bool{models.PermAIAutonomyQuery: true, models.PermAIAutonomyRun: true}
	mcpOnly := map[string]bool{models.PermMCPAutonomyQuery: true, models.PermMCPAutonomyRun: true}
	for _, c := range []struct {
		name    string
		channel string
		perms   map[string]bool
		class   string
		wantWhy string
	}{
		{"mcp with chat permissions", ChannelMCP, chatOnly, ClassDeviceQuery, "mcp.autonomy.query"},
		{"chat with mcp permissions", ChannelChat, mcpOnly, ClassPreapproved, "ai.autonomy.run"},
		{"no permission map", ChannelChat, nil, ClassDeviceQuery, "ai.autonomy.query"},
		{"query permission does not run scripts", ChannelChat, map[string]bool{models.PermAIAutonomyQuery: true}, ClassPreapproved, "ai.autonomy.run"},
		{"granted but the pause cannot be read", ChannelChat, chatOnly, ClassDeviceQuery, "paused"},
	} {
		tc := ToolContext{Channel: c.channel, Permissions: c.perms}
		ok, why := tc.autonomyDecision(ctx, c.class)
		if ok || !strings.Contains(why, c.wantWhy) {
			t.Errorf("%s: ok=%v why=%q", c.name, ok, why)
		}
	}
	if autonomyPermission(ChannelChat, ClassChange) != "" || autonomyPermission(ChannelMCP, ClassConsoleRead) != "" {
		t.Fatal("a non-autonomous class maps to a permission")
	}
}

func TestPrincipalNamesTheChannel(t *testing.T) {
	if p := (ToolContext{Actor: "ana"}).principal(); p != "ai:ana" {
		t.Fatalf("chat principal = %q", p)
	}
	if p := (ToolContext{Actor: "mcp:ana", Channel: ChannelMCP}).principal(); p != "mcp:ana" {
		t.Fatalf("mcp principal = %q", p)
	}
}

func TestRedactForModelRemovesCredentials(t *testing.T) {
	in := strings.Join([]string{
		"DB_PASSWORD=hunter2",
		`"api_key": "abc123def456"`,
		"Authorization: Bearer abcdefghijklmnop",
		"postgres://app:s3cr3t@db.internal:5432/app",
		"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"aws AKIAIOSFODNN7EXAMPLE",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
		"ghp_" + strings.Repeat("a", 36),
		"service Spooler is running",
	}, "\n")
	out, truncated := redactForModel(in)
	for _, secret := range []string{"hunter2", "abc123def456", "abcdefghijklmnop", "s3cr3t", "dozjgNryP4J3jVmNHl0w5N",
		"AKIAIOSFODNN7EXAMPLE", "MIIEow", strings.Repeat("a", 36)} {
		if strings.Contains(out, secret) {
			t.Errorf("%q survived redaction:\n%s", secret, out)
		}
	}
	if truncated || !strings.Contains(out, "service Spooler is running") || !strings.Contains(out, "DB_PASSWORD=[REDACTED]") {
		t.Fatalf("ordinary output was damaged:\n%s", out)
	}
}

func TestRedactForModelBoundsTheOutputOnACharacter(t *testing.T) {
	long := strings.Repeat("é", maxModelOutput) // two bytes each
	out, truncated := redactForModel(long)
	if !truncated || len(out) > maxModelOutput || !utf8.ValidString(out) {
		t.Fatalf("truncated=%v len=%d valid=%v", truncated, len(out), utf8.ValidString(out))
	}
}

func TestStringParamsKeepsNumbersExact(t *testing.T) {
	got, err := stringParams(map[string]any{"limit": float64(10), "sort": "cpu", "flag": true})
	if err != nil || got["limit"] != "10" || got["sort"] != "cpu" || got["flag"] != "true" {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := stringParams(map[string]any{"x": []any{"a"}}); err == nil {
		t.Fatal("a list was accepted")
	}
}

// get_job_status hands the model the same bounded, redacted output as the
// autonomy tools: a job that ran on its own must not leak through the back door
// of a status check.
func TestJobForModelRedactsAndMarksDeviceOutput(t *testing.T) {
	code := 0
	j := &models.Job{ID: "j1", Status: models.JobCompleted, ExitCode: &code, Autonomy: models.AutonomyQuery,
		Stdout: "ok\nAPI_TOKEN=abcdef123456\n" + strings.Repeat("x", maxModelOutput), Stderr: "pwd: hunter2"}
	out := jobForModel(j)
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"abcdef123456", "hunter2"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%q reached the model: %s", secret, raw[:200])
		}
	}
	if out["truncated"] != true || out["note"] != untrustedNote || out["ran_automatically"] != true {
		t.Fatalf("out = %v", out)
	}
	if _, raw := out["stdout"]; raw {
		t.Fatal("the raw stdout field is still there")
	}
}
