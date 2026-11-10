package api

import (
	"encoding/json"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

func TestAlertTransitionAuditSnapshotsReadableTarget(t *testing.T) {
	snapshot, live := "snapshot-agent", "live-agent"
	alert := &models.Alert{AgentHostname: "WINDOWS-TEST", AgentSnapshotID: &snapshot, AgentID: &live, Message: "Service stopped", Version: 2}
	var detail map[string]any
	if err := json.Unmarshal(alertTransitionAuditDetail(alert, "checked"), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["hostname"] != "WINDOWS-TEST" || detail["agent"] != snapshot || detail["name"] != "Service stopped" || detail["version"] != float64(2) || detail["note"] != "checked" {
		t.Fatalf("audit detail=%+v", detail)
	}
	alert.AgentHostname = ""
	alert.AgentSnapshotID = nil
	alert.AgentID = nil
	// Unmarshal into a fresh map for omission assertions.
	detail = nil
	if err := json.Unmarshal(alertTransitionAuditDetail(alert, ""), &detail); err != nil {
		t.Fatal(err)
	}
	if _, ok := detail["hostname"]; ok {
		t.Fatal("system alert invented hostname")
	}
	if detail["name"] != "Service stopped" {
		t.Fatal("system alert lost readable name")
	}
}
