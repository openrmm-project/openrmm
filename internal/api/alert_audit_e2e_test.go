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

func TestAlertAcknowledgementReadableAuditTargetE2E(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()
	a, err := f.store.CreateAgent(ctx, "LAB-WINDOWS", "windows", "amd64", "0.10.0", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := f.store.ObserveAlert(ctx, store.AlertObservation{AgentID: &a.ID, SourceType: models.AlertSourceAgent, SourceID: &a.ID, ConditionKey: "offline", Severity: models.SeverityWarning, Message: "Device offline", ObservedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.Alert.ID
	legacy := []byte(`{"version":1}`)
	if err := f.store.AddAudit(ctx, f.admin.ID, "alert.ack", id, legacy); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddAudit(ctx, f.admin.ID, "alert.ack", uuid.NewString(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddAudit(ctx, f.admin.ID, "alert.ack", "alerts", legacy); err != nil {
		t.Fatal(err)
	}
	result := f.do(http.MethodPost, "/api/alerts/"+id+"/ack", `{}`)
	if result.Code != http.StatusOK {
		t.Fatalf("ack=%d %s", result.Code, result.Body.String())
	}
	result = f.do(http.MethodGet, "/api/audit?action=alert.ack", "")
	if result.Code != http.StatusOK {
		t.Fatalf("audit=%d %s", result.Code, result.Body.String())
	}
	var response struct {
		Entries []struct {
			models.AuditEntry
			TargetLabel string `json:"target_label"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	foundSnapshot, foundLegacy := false, false
	for _, e := range response.Entries {
		if e.Target != id {
			if e.TargetLabel != "" {
				t.Fatal("unknown alert invented a label")
			}
			continue
		}
		if e.TargetLabel != "LAB-WINDOWS" {
			t.Fatalf("legacy label=%q", e.TargetLabel)
		}
		var detail map[string]any
		if err := json.Unmarshal(e.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		if detail["hostname"] == "LAB-WINDOWS" {
			foundSnapshot = true
		} else {
			foundLegacy = true
			if string(e.Detail) != `{"version": 1}` && string(e.Detail) != string(legacy) {
				t.Fatalf("legacy audit detail was rewritten: %s", e.Detail)
			}
		}
	}
	if !foundSnapshot || !foundLegacy {
		t.Fatalf("snapshot=%v legacy=%v", foundSnapshot, foundLegacy)
	}
	if chain, err := f.store.VerifyAuditChain(ctx); err != nil || chain != nil {
		t.Fatalf("audit chain damaged: %+v %v", chain, err)
	}
	// A scoped principal still cannot use global audit label resolution.
	restricted, err := f.store.CreateUserWithScopes(ctx, "audit-scoped", "hash", models.RoleAdmin, []models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: uuid.NewString()}})
	if err != nil {
		t.Fatal(err)
	}
	f.token = auth.NewToken()
	if err := f.store.CreateSession(ctx, restricted.ID, auth.Hash(f.token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if out := f.do(http.MethodGet, "/api/audit", ""); out.Code != http.StatusForbidden {
		t.Fatalf("restricted audit=%d", out.Code)
	}
}
