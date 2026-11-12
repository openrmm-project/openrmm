package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/scriptvars"
	"github.com/p1ngu/rmm/internal/secretcrypto"
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
	// Keep testing the pre-variables schema, even when later migrations are added.
	if err := st.migrate(ctx, migrations[:28]); err != nil {
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

func TestScriptVariableNamesMigrationPreservesBindingsIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newMigrationTestStore(t, url)
	ctx := context.Background()
	if err := st.migrate(ctx, migrations[:29]); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-instance-key"}
	// Seed the version-29 rows with SQL: the store code targets the latest
	// schema (migration 031 added proposal columns it reads and writes), so it
	// cannot write a database that has not reached it yet.
	legacy := &ScriptVariable{ID: newID(), Name: "CUSTOM_KEY"}
	enc, err := secretcrypto.Encrypt(cfg, "legacy-synthetic-credential")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO script_variables (id, name, description, secret) VALUES ($1,$2,'before migration',true)`, legacy.ID, legacy.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO script_variable_values (variable_id, scope_type, scope_id, value) VALUES ($1,'global','',$2)`, legacy.ID, enc); err != nil {
		t.Fatal(err)
	}
	sc := &models.Script{ID: newID(), Name: "before migration", Shell: "sh", Body: `printf '%s' "$CUSTOM_KEY"`, Variables: []string{legacy.Name}}
	if _, err := st.pool.Exec(ctx, `INSERT INTO scripts (id, name, description, category, shell, body, default_args, params, variables)
		VALUES ($1,$2,'','',$3,$4,'','[]',$5)`, sc.ID, sc.Name, sc.Shell, sc.Body, jobVariablesJSON(sc.Variables)); err != nil {
		t.Fatal(err)
	}
	beforeHash := models.ScriptContentHash(sc.Shell, sc.Body, sc.Params, sc.Variables)
	for i := 0; i < 2; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var name, stored string
	if err := st.pool.QueryRow(ctx, `SELECT d.name, v.value FROM script_variables d JOIN script_variable_values v ON v.variable_id=d.id WHERE d.id=$1`, legacy.ID).Scan(&name, &stored); err != nil {
		t.Fatal(err)
	}
	if name != legacy.Name || !strings.HasPrefix(stored, "enc:v1:") || strings.Contains(stored, "legacy-synthetic-credential") {
		t.Fatal("migration changed legacy name or encryption")
	}
	got, err := st.GetScript(ctx, sc.ID)
	if err != nil || !slices.Equal(got.Variables, sc.Variables) || models.ScriptContentHash(got.Shell, got.Body, got.Params, got.Variables) != beforeHash {
		t.Fatal("migration changed existing script bindings or hash")
	}
	agent, err := st.CreateAgent(ctx, "PC", "linux", "amd64", "0.11.0", "synthetic-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var longName strings.Builder
	longName.WriteString("_longKey_")
	for i := 0; i < 128; i++ {
		// Avoid a compressible repeated name that would conceal the old raw
		// B-tree index's key-size ceiling.
		fmt.Fprintf(&longName, "%x", sha256.Sum256([]byte(fmt.Sprintf("name-%d", i))))
	}
	for _, tc := range []struct {
		name, value string
		secret      bool
	}{
		{legacy.Name, "legacy-synthetic-credential", true},
		{"customerKey", "x", true},
		{longName.String(), strings.Repeat("v", 65536), false},
	} {
		v := legacy
		if tc.name != legacy.Name {
			v, err = st.CreateScriptVariable(ctx, tc.name, "after migration", tc.secret)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.SetScriptVariableValue(ctx, cfg, v.ID, "global", "", tc.value); err != nil {
				t.Fatal(err)
			}
		}
		env, secrets, err := st.ResolveScriptVariables(ctx, cfg, agent.ID, []string{tc.name})
		if err != nil || env[tc.name] != tc.value || (len(secrets) == 1) != tc.secret {
			t.Fatal("name, value or secret resolution changed")
		}
		// The unique index must reject case aliases even for names larger than a
		// raw B-tree key. It must not truncate or rewrite the stored identifier.
		if _, err := st.CreateScriptVariable(ctx, strings.ToLower(tc.name), "alias", false); err == nil {
			t.Fatal("case-only alias inserted")
		} else {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
				t.Fatalf("expected unique constraint, got %v", err)
			}
		}
	}
	assertCurrentMigrationState(t, st)
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

func TestScriptVariablesReleaseFailureResetIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, "script_vars_breaker", url)
	ctx := context.Background()
	sc, err := st.CreateScript(ctx, &models.Script{Name: "reviewed", Shell: "sh", Body: "echo ok", Variables: []string{"CUSTOM_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetScriptAIRelease(ctx, sc.ID, "reviewer", sc.ContentHash, sc.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	outcome := func(success bool, want bool) {
		t.Helper()
		revoked, err := st.RecordAIScriptOutcome(ctx, sc.ID, success, 3)
		if err != nil || revoked != want {
			t.Fatalf("breaker result=%t err=%v", revoked, err)
		}
	}
	outcome(false, false)
	outcome(false, false)
	outcome(true, false)
	outcome(false, false)
	outcome(false, false)
	outcome(false, true)
	got, err := st.GetScript(ctx, sc.ID)
	if err != nil || got.AIReleased {
		t.Fatal("variable-bound release survived breaker")
	}
}

func TestScriptVariableDispatchLocksAndRechecksScopeIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, "script_vars_transfer", url)
	ctx := context.Background()
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-instance-key"}
	a, err := st.CreateClient(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateClient(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	sa, err := st.CreateSite(ctx, a.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := st.CreateSite(ctx, b.ID, "B")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, "PC", "linux", "amd64", "0.11.0", "synthetic-token", &sa.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.CreateScriptVariable(ctx, "CUSTOM_KEY", "", true)
	if err != nil {
		t.Fatal(err)
	}
	for id, value := range map[string]string{a.ID: "customer-A-synthetic-key", b.ID: "customer-B-synthetic-key"} {
		if err := st.SetScriptVariableValue(ctx, cfg, v.ID, "client", id, value); err != nil {
			t.Fatal(err)
		}
	}
	revisionA, err := st.ScriptVariableScopeRevision(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	envA, _, err := st.ResolveScriptVariables(ctx, cfg, agent.ID, []string{v.Name})
	if err != nil {
		t.Fatal(err)
	}
	if envA[v.Name] != "customer-A-synthetic-key" {
		t.Fatal("A resolution control failed")
	}
	if err := st.MoveAgents(ctx, []string{agent.ID}, sb.ID, nil); err != nil {
		t.Fatal(err)
	}
	sends := 0
	send := func() bool { sends++; return true }
	if sent, err := st.WithScriptVariablesDispatch(ctx, agent.ID, revisionA, send); err == nil || sent || sends != 0 {
		t.Fatal("stale A context dispatched after B move")
	}
	sc, err := st.CreateScript(ctx, &models.Script{Name: "reviewed", Shell: "sh", Body: "echo ok", Variables: []string{v.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetScriptAIRelease(ctx, sc.ID, "reviewer", sc.ContentHash, sc.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	scriptID := sc.ID
	j := &models.Job{AgentID: agent.ID, Autonomy: models.AutonomyPreapproved, ScriptID: &scriptID, Shell: sc.Shell, Script: sc.Body, Variables: sc.Variables}
	if sent, err := st.WithAutonomousDispatch(ctx, j, sc.ContentHash, send, revisionA); err == nil || sent || sends != 0 {
		t.Fatal("autonomy bypassed variable scope guard")
	}
	revisionB, err := st.ScriptVariableScopeRevision(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	envB, _, err := st.ResolveScriptVariables(ctx, cfg, agent.ID, []string{v.Name})
	if err != nil || envB[v.Name] != "customer-B-synthetic-key" {
		t.Fatal("B resolution control failed")
	}
	if sent, err := st.WithScriptVariablesDispatch(ctx, agent.ID, revisionB, send); err != nil || !sent || sends != 1 {
		t.Fatal("fresh B context refused")
	}
	if sent, err := st.WithAutonomousDispatch(ctx, j, sc.ContentHash, send, revisionB); err != nil || !sent || sends != 2 {
		t.Fatal("fresh autonomous context refused")
	}
	if err := st.MoveAgents(ctx, []string{agent.ID}, sa.ID, nil); err != nil {
		t.Fatal(err)
	}
	if sent, err := st.WithScriptVariablesDispatch(ctx, agent.ID, revisionA, send); err == nil || sent || sends != 2 {
		t.Fatal("away-and-back move reused stale revision")
	}
	// Hold the guard at its enqueue callback. A real move must wait on the
	// agent row; bounded lock_timeout proves contention without scheduling sleeps.
	current, err := st.ScriptVariableScopeRevision(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	guardCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		sent, err := st.WithScriptVariablesDispatch(guardCtx, agent.ID, current, func() bool {
			close(entered)
			select {
			case <-release:
				return true
			case <-guardCtx.Done():
				return false
			}
		})
		if err == nil && !sent {
			err = fmt.Errorf("guard callback did not send")
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-guardCtx.Done():
		t.Fatal("guard did not acquire row lock")
	}
	tx, err := st.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE agents SET site_id=$2 WHERE id=$1`, agent.ID, sb.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatal("move was not blocked by the dispatch row lock")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := st.MoveAgents(ctx, []string{agent.ID}, sb.ID, nil); err != nil {
		t.Fatal("move failed after guard released its lock")
	}
}
