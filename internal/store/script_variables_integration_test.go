package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/scriptvars"
)

func TestScriptVariablesInheritanceAndEncryptedStorageIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, "script_vars", url)
	ctx := context.Background()
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-instance-key"}
	client, err := st.CreateClient(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "Main")
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateGroup(ctx, "IT", &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, "PC-A", "linux", "amd64", "0.11.0", "fake-agent-token", &site.ID, &group.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateClient(ctx, "Other")
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.CreateScriptVariable(ctx, "CUSTOM_KEY", "fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	scopes := []struct{ kind, id string }{{"global", ""}, {"client", client.ID}, {"site", site.ID}, {"group", group.ID}, {"agent", agent.ID}}
	for i, scope := range scopes {
		value := "synthetic-" + scope.kind + "-credential"
		if err := st.SetScriptVariableValue(ctx, cfg, v.ID, scope.kind, scope.id, value); err != nil {
			t.Fatal(err)
		}
		env, secrets, err := st.ResolveScriptVariables(ctx, cfg, agent.ID, []string{v.Name})
		if err != nil {
			t.Fatal(err)
		}
		if env[v.Name] != value || len(secrets) != 1 || secrets[0] != value || env[scriptvars.BuiltinClientName] != "A" {
			t.Fatalf("scope priority %d failed", i)
		}
	}
	if err := st.SetScriptVariableValue(ctx, cfg, v.ID, "client", other.ID, "other-customer-credential"); err != nil {
		t.Fatal(err)
	}
	for i := len(scopes) - 1; i >= 0; i-- {
		env, _, err := st.ResolveScriptVariables(ctx, cfg, agent.ID, []string{v.Name})
		if err != nil {
			t.Fatal(err)
		}
		if env[v.Name] != "synthetic-"+scopes[i].kind+"-credential" {
			t.Fatal("inheritance or tenant isolation failed")
		}
		if err := st.ClearScriptVariableValue(ctx, v.ID, scopes[i].kind, scopes[i].id); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.ResolveScriptVariables(ctx, cfg, agent.ID, []string{v.Name}); err == nil {
		t.Fatal("fell back to another customer's secret")
	}
	var stored string
	if err := st.pool.QueryRow(ctx, `SELECT value FROM script_variable_values WHERE variable_id=$1`, v.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "enc:v1:") || strings.Contains(stored, "other-customer") {
		t.Fatal("secret not encrypted")
	}
	list, err := st.ListScriptVariables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Values[0].Value != "" || !list[0].Values[0].Set {
		t.Fatal("secret read-back")
	}
	if err := st.UpdateScriptVariable(ctx, cfg, v.ID, "updated", false); err == nil {
		t.Fatal("secret downgraded")
	}
	// Conversion must encrypt all existing values; names stay stable and secret can't revert.
	plain, err := st.CreateScriptVariable(ctx, "PUBLIC_IDENTIFIER", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetScriptVariableValue(ctx, cfg, plain.ID, "global", "", "synthetic-public-identifier"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateScriptVariable(ctx, cfg, plain.ID, "", true); err != nil {
		t.Fatal(err)
	}
	env, _, err := st.ResolveScriptVariables(ctx, cfg, agent.ID, []string{plain.Name})
	if err != nil || env[plain.Name] != "synthetic-public-identifier" {
		t.Fatal("conversion failed")
	}
}
func TestScriptVariablesMigrationUpgradeAndRedispatchIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newMigrationTestStore(t, url)
	ctx := context.Background()
	if err := st.migrate(ctx, migrations[:len(migrations)-1]); err != nil {
		t.Fatal(err)
	}
	client, err := st.CreateClient(ctx, "Before upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clients, err := st.ListClients(ctx)
	if err != nil || len(clients) != 1 || clients[0].ID != client.ID {
		t.Fatal("upgrade lost data")
	}
	for _, role := range []string{models.RoleAdmin, models.RoleTech, models.RoleReadOnly} {
		perms, err := st.RolePermissions(ctx, role)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(perms, models.PermScriptsVariables) != (role == models.RoleAdmin) {
			t.Fatal("wrong default grant")
		}
	}
	agent, err := st.CreateAgent(ctx, "PC", "linux", "amd64", "0.11.0", "synthetic-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := st.CreateJob(ctx, &models.Job{AgentID: agent.ID, Type: models.JobRunScript, Shell: "sh", Script: "echo", CreatedBy: "user", Status: models.JobPending, Variables: []string{"CUSTOM_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-key"}
	for _, key := range []string{"original-customer-key", "rotated-customer-key"} {
		if err := st.SaveJobRedactions(ctx, cfg, j.ID, []string{key}); err != nil {
			t.Fatal(err)
		}
	}
	blob, err := st.JobRedactions(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := scriptvars.Open(cfg, blob)
	if err != nil || len(keys) != 2 {
		t.Fatal("redispatch lost earlier secret redactions")
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil || !slices.Equal(got.Variables, j.Variables) {
		t.Fatal("job declaration snapshot lost")
	}
}

func TestScriptVariableSecretConversionSerializesWritersIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, "script_vars_race", url)
	ctx := context.Background()
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-key"}
	for i := 0; i < 8; i++ {
		v, err := st.CreateScriptVariable(ctx, fmt.Sprintf("KEY_%d", i), "", false)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetScriptVariableValue(ctx, cfg, v.ID, "global", "", "initial-synthetic-key"); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			<-start
			done <- st.SetScriptVariableValue(ctx, cfg, v.ID, "global", "", "replacement-synthetic-key")
		}()
		go func() { <-start; done <- st.UpdateScriptVariable(ctx, cfg, v.ID, "", true) }()
		close(start)
		for j := 0; j < 2; j++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		var value string
		if err := st.pool.QueryRow(ctx, `SELECT value FROM script_variable_values WHERE variable_id=$1`, v.ID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(value, "enc:v1:") || strings.Contains(value, "synthetic-key") {
			t.Fatal("conversion raced a plaintext write")
		}
	}
}
