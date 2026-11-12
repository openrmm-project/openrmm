package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestServerRulePrecedenceAndAgentBoundaryIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "rule precedence")
	ctx := context.Background()
	create := func(policyID *string, cfg string, proposal bool) *models.Check {
		t.Helper()
		c := &models.Check{PolicyID: policyID, Type: models.CheckConnectivity, Name: "connectivity", Config: json.RawMessage(cfg), Enabled: !proposal}
		if policyID == nil {
			c.AgentID = &f.agent.ID
		}
		if proposal {
			c.ProposedBy = "ai:test"
		}
		if err := models.ValidateCheck(c); err != nil {
			t.Fatal(err)
		}
		got, err := f.store.CreateCheck(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	assertWinner := func(id string) {
		t.Helper()
		rules, err := f.store.GoverningRules(ctx, models.CheckConnectivity)
		if err != nil {
			t.Fatal(err)
		}
		if rules[f.agent.ID].CheckID != id {
			t.Fatalf("winner = %+v, want %s", rules[f.agent.ID], id)
		}
	}
	direct := create(nil, `{"after_min":3}`, false)
	assertWinner(direct.ID)
	// More specific rules cannot override an existing broad policy.
	for _, scope := range []struct{ kind, id string }{
		{"agent", f.agent.ID}, {"group", f.group.ID}, {"site", f.site.ID}, {"client", f.client.ID},
	} {
		p, err := f.store.CreatePolicy(ctx, scope.kind, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.AssignPolicy(ctx, p.ID, scope.kind, scope.id); err != nil {
			t.Fatal(err)
		}
		ignore := create(&p.ID, `{"mode":"ignore"}`, false)
		assertWinner(ignore.ID)
		if scope.kind == "client" {
			// At the same level, alert wins over ignore, then the shorter delay.
			slow := create(&p.ID, `{"after_min":60}`, false)
			assertWinner(slow.ID)
			fast := create(&p.ID, `{"after_min":5}`, false)
			assertWinner(fast.ID)
			create(&p.ID, `{"after_min":3}`, true)
			assertWinner(fast.ID) // unreviewed proposal is inert
			fast.Enabled = false
			if err := f.store.UpdateCheck(ctx, fast); err != nil {
				t.Fatal(err)
			}
			assertWinner(slow.ID)
		}
	}
	if allowed, err := f.store.CheckAppliesToAgent(ctx, direct.ID, f.agent.ID); err != nil || allowed {
		t.Fatalf("server rule accepted as agent check: %v, %v", allowed, err)
	}
	if allowed, err := f.store.CheckAppliesToAgent(ctx, f.check.ID, f.agent.ID); err != nil || !allowed {
		t.Fatalf("normal agent check refused: %v, %v", allowed, err)
	}
}

func TestRuleChangeRetainsIncidentWithoutDuplicatingObservationIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "rule incident")
	ctx := context.Background()
	obs := AlertObservation{AgentID: &f.agent.ID, SourceType: models.AlertSourceRule, SourceID: &f.agent.ID,
		ConditionKey: models.CheckPatchFailure, Severity: models.SeverityError, Message: "failed patch", ObservationID: "patch:1", ObservedAt: time.Now()}
	first, err := f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, first.Alert.ID, first.Alert.NotificationSeq, time.Now()); err != nil {
		t.Fatal(err)
	}
	obs.RuleID = &f.check.ID
	obs.Severity = models.SeverityWarning
	second, err := f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	if second.Alert.ID != first.Alert.ID || second.Alert.OccurrenceCount != first.Alert.OccurrenceCount || second.Alert.RuleID == nil || *second.Alert.RuleID != f.check.ID || second.Alert.Severity != models.SeverityWarning {
		t.Fatalf("same observation changed rule incorrectly: first=%+v second=%+v", first.Alert, second.Alert)
	}
	if second.Opened || second.Notify {
		t.Fatal("rule replacement reopened/notified the incident")
	}
}

func TestRuleEmailSelectionPreservesScopeAndDeletedRuleIdentityIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "rule email")
	ctx := context.Background()
	u, err := f.store.CreateUserWithScopes(ctx, "rule-email", "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: f.client.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateUserEmail(ctx, u.ID, "rule@example.invalid"); err != nil {
		t.Fatal(err)
	}
	obs := AlertObservation{AgentID: &f.agent.ID, SourceType: models.AlertSourceRule, SourceID: &f.agent.ID,
		ConditionKey: models.CheckPatchFailure, Severity: models.SeverityWarning, Message: "failed patch", ObservedAt: time.Now()}
	opened, err := f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	receives := func(want bool) {
		t.Helper()
		got, err := f.store.RecipientsForAlertEvent(ctx, opened.Alert.ID, NotifyCheckFailed, true)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range got {
			found = found || r.UserID == u.ID
		}
		if found != want {
			t.Fatalf("received=%v, want %v", found, want)
		}
	}
	set := func(include bool, ids ...string) {
		t.Helper()
		if err := f.store.SetNotificationRuleFilter(ctx, u.ID, NotificationRuleFilter{Mode: "selected", IncludeDefaults: include, CheckIDs: ids}); err != nil {
			t.Fatal(err)
		}
	}
	receives(true) // no preference preserves old delivery
	set(false)
	receives(false)
	set(true)
	receives(true) // fleet default
	obs.RuleID = &f.check.ID
	opened, err = f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	receives(false) // custom rule is not a fleet default
	set(false, f.check.ID)
	receives(true)
	// Even a stored selection cannot bypass the immutable incident scope.
	if _, err := f.store.pool.Exec(ctx, `UPDATE user_scopes SET scope_id=gen_random_uuid() WHERE user_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	receives(false)
	if _, err := f.store.pool.Exec(ctx, `UPDATE user_scopes SET scope_id=$2 WHERE user_id=$1`, u.ID, f.client.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteCheck(ctx, f.check.ID); err != nil {
		t.Fatal(err)
	}
	set(true)
	receives(false) // deleting a rule cannot reclassify its incident as a default
}

func TestAlertRuleMigrationUpgradeAndReapplyIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "rule migration")
	ctx := context.Background()
	defaults, err := f.store.AlertRuleDefaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != len(models.ServerRuleTypes) || !defaults[models.CheckConnectivity].Config.Alerts() || defaults[models.CheckConnectivity].Config.AfterMin != 3 {
		t.Fatalf("upgrade defaults changed: %+v", defaults)
	}
	for _, kind := range models.ServerRuleTypes {
		if kind != models.CheckConnectivity && defaults[kind].Config.Alerts() {
			t.Fatalf("upgrade enabled %s", kind)
		}
	}
	// Reapply preserves an operator's choice and backfills legacy check incidents.
	if _, err := f.store.pool.Exec(ctx, `INSERT INTO alerts (id,agent_id,source_type,source_id,condition_key,severity,message,first_seen_at,last_seen_at,updated_at) VALUES (gen_random_uuid(),$1,'check',$2,'health','warning','legacy',now(),now(),now())`, f.agent.ID, f.check.ID); err != nil {
		t.Fatal(err)
	}
	cfg, err := models.ParseServerRuleConfig(models.CheckConnectivity, json.RawMessage(`{"mode":"ignore"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetAlertRuleDefault(ctx, models.CheckConnectivity, cfg, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(ctx, migrationV28SQL); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.store.pool.QueryRow(ctx, `SELECT rule_id FROM alerts WHERE source_id=$1`, f.check.ID).Scan(&id); err != nil || id != f.check.ID {
		t.Fatalf("legacy rule backfill: %s %v", id, err)
	}
	defaults, err = f.store.AlertRuleDefaults(ctx)
	if err != nil || defaults[models.CheckConnectivity].Config.Alerts() {
		t.Fatalf("reapply overwrote defaults: %+v %v", defaults, err)
	}
}

func TestAlertRuleMigrationFromPreviousSchemaIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newMigrationTestStore(t, url)
	ctx := context.Background()
	if err := st.migrate(ctx, migrations[:27]); err != nil {
		t.Fatal(err)
	}
	// Seeded with SQL: the store code targets the latest schema (migration 032
	// added the policy switch it reads and writes).
	policy := &models.Policy{ID: newID()}
	if _, err := st.pool.Exec(ctx, `INSERT INTO policies (id, name) VALUES ($1, 'old policy')`, policy.ID); err != nil {
		t.Fatal(err)
	}
	check, err := st.CreateCheck(ctx, &models.Check{PolicyID: &policy.ID, Name: "legacy", Type: models.CheckMemory,
		Config: json.RawMessage(`{}`), IntervalSec: 300, TimeoutSec: 30, FailureCount: 2, RecoveryCount: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO alerts (id,source_type,source_id,condition_key,severity,message,first_seen_at,last_seen_at,updated_at)
		VALUES (gen_random_uuid(),'check',$1,'health','warning','legacy',now(),now(),now())`, check.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var ruleID string
	if err := st.pool.QueryRow(ctx, `SELECT rule_id FROM alerts WHERE source_id=$1`, check.ID).Scan(&ruleID); err != nil || ruleID != check.ID {
		t.Fatalf("legacy backfill: %s %v", ruleID, err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
