package store

import (
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestComplianceStatus(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		row  models.ComplianceRow
		want string
	}{
		{"never scanned", models.ComplianceRow{}, "unknown"},
		{"missing security -> critical", models.ComplianceRow{LastScan: &now, MissingTotal: 3, MissingSec: 1}, "critical"},
		{"failed install -> critical", models.ComplianceRow{LastScan: &now, Failed: 1}, "critical"},
		{"missing non-security -> at_risk", models.ComplianceRow{LastScan: &now, MissingTotal: 2}, "at_risk"},
		{"reboot pending -> at_risk", models.ComplianceRow{LastScan: &now, RebootPending: true}, "at_risk"},
		{"clean -> compliant", models.ComplianceRow{LastScan: &now}, "compliant"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := complianceStatus(tc.row); got != tc.want {
				t.Fatalf("complianceStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentScopeClause(t *testing.T) {
	if c, a := agentScopeClause("", "", 2); c != "TRUE" || a != nil {
		t.Fatalf("global = (%q,%v)", c, a)
	}
	if c, a := agentScopeClause("agent", "x", 2); c != "agent_id = $2" || len(a) != 1 {
		t.Fatalf("agent = (%q,%v)", c, a)
	}
	if c, _ := agentScopeClause("site", "s", 3); c != "agent_id IN (SELECT id FROM agents WHERE site_id = $3)" {
		t.Fatalf("site = %q", c)
	}
}
