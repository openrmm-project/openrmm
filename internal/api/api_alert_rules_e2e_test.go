package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// End-to-end through the real authenticated router, migrations and store.
// PostgreSQL runs in CI only; requests use a recorder, never a listening server.
func TestAlertRulesAndEmailPreferencesE2E(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()
	client, err := f.store.CreateClient(ctx, "rules e2e")
	if err != nil {
		t.Fatal(err)
	}
	site, err := f.store.CreateSite(ctx, client.ID, "rules e2e")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.store.CreateAgent(ctx, "rules-e2e", "linux", "amd64", "0.10.0", "test", &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		r := f.do(method, path, body)
		if r.Code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, r.Code, want, r.Body.String())
		}
		return r.Body.Bytes()
	}
	var check models.Check
	if err := json.Unmarshal(request(http.MethodPost, "/api/checks", `{"agent_id":"`+agent.ID+`","name":"sleeping laptop","type":"connectivity","config":{"mode":"ignore"}}`, http.StatusCreated), &check); err != nil {
		t.Fatal(err)
	}
	if !check.Enabled || check.Type != models.CheckConnectivity {
		t.Fatalf("invalid created rule: %+v", check)
	}
	governing, err := f.store.GoverningRules(ctx, check.Type)
	if err != nil || governing[agent.ID].CheckID != check.ID || governing[agent.ID].Config.Mode != "ignore" {
		t.Fatalf("rule did not govern: %+v %v", governing, err)
	}
	request(http.MethodGet, "/api/checks/"+check.ID+"/overrides", "", http.StatusOK)
	request(http.MethodPut, "/api/alert-defaults/connectivity", `{"mode":"alert","after_min":30,"severity":"warning"}`, http.StatusOK)
	if f.audit("alert_default.update") == nil || f.audit("alert_rule.create") == nil {
		t.Fatal("rule mutations were not audited")
	}
	request(http.MethodPut, "/api/alert-defaults/connectivity", `{"after_min":1}`, http.StatusBadRequest)
	request(http.MethodPut, "/api/me/notifications", `{"subscriptions":{"agent_offline":false},"rules":{"mode":"selected","include_defaults":false,"check_ids":["`+check.ID+`"]}}`, http.StatusOK)
	var prefs struct {
		Rules     store.NotificationRuleFilter `json:"rules"`
		Available []notificationRuleChoice     `json:"available_rules"`
	}
	if err := json.Unmarshal(request(http.MethodGet, "/api/me/notifications", "", http.StatusOK), &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.Rules.Mode != "selected" || len(prefs.Rules.CheckIDs) != 1 || prefs.Rules.CheckIDs[0] != check.ID || len(prefs.Available) != 1 {
		t.Fatalf("preferences did not round-trip: %+v", prefs)
	}
	// Legacy clients changing event preferences do not reset the rule filter.
	request(http.MethodPut, "/api/me/notifications", `{"subscriptions":{"agent_offline":true}}`, http.StatusOK)
	filter, err := f.store.GetNotificationRuleFilter(ctx, f.admin.ID)
	if err != nil || filter.Mode != "selected" {
		t.Fatalf("legacy request reset rules: %+v %v", filter, err)
	}
	request(http.MethodPut, "/api/me/notifications", `{"rules":{"mode":"selected","check_ids":["`+uuid.NewString()+`"]}}`, http.StatusBadRequest)
	// A restricted manager cannot edit fleet defaults or enumerate overrides.
	scoped, err := f.store.CreateUserWithScopes(ctx, "rules-scoped", "hash", models.RoleAdmin,
		[]models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	f.token = auth.NewToken()
	if err := f.store.CreateSession(ctx, scoped.ID, auth.Hash(f.token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPut, "/api/alert-defaults/connectivity", `{"mode":"ignore"}`, http.StatusForbidden)
	request(http.MethodGet, "/api/checks/"+check.ID+"/overrides", "", http.StatusForbidden)
	request(http.MethodPut, "/api/me/notifications", `{"rules":{"mode":"selected","check_ids":["`+check.ID+`"]}}`, http.StatusBadRequest)
	if err := json.Unmarshal(request(http.MethodGet, "/api/me/notifications", "", http.StatusOK), &prefs); err != nil {
		t.Fatal(err)
	}
	if len(prefs.Available) != 0 {
		t.Fatal("out-of-scope rule name exposed")
	}
}
