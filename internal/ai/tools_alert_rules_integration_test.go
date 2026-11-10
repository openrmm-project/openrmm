package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

func TestServerRuleProposalsStayInertAndScopedIntegration(t *testing.T) {
	f := newProposalFixture(t)
	for _, kind := range models.ServerRuleTypes {
		t.Run(kind, func(t *testing.T) {
			args := func(agentID string) json.RawMessage {
				b, err := json.Marshal(map[string]any{"agent_id": agentID, "name": "AI rule", "type": kind, "enabled": true, "config": map[string]any{"mode": "ignore"}})
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			res := Execute(f.ctx, f.tc(), "propose_check", args(f.mineAgent))
			if strings.Contains(res.Output, `"error"`) {
				t.Fatalf("proposal refused: %s", res.Output)
			}
			checks, err := f.st.ListChecks(f.ctx, f.mineAgent, "")
			if err != nil {
				t.Fatal(err)
			}
			var found *models.Check
			for i := range checks {
				if checks[i].Type == kind {
					found = &checks[i]
				}
			}
			if found == nil || found.Enabled || found.ProposedBy == "" || found.ApprovedBy != "" {
				t.Fatalf("proposal not inert: %+v", found)
			}
			rules, err := f.st.GoverningRules(f.ctx, kind)
			if err != nil {
				t.Fatal(err)
			}
			if _, active := rules[f.mineAgent]; active {
				t.Fatal("unreviewed ignore proposal silenced a device")
			}
			res = Execute(f.ctx, f.tc(), "propose_check", args(f.theirAgent))
			if !strings.Contains(res.Output, `"error"`) {
				t.Fatal("out-of-scope proposal accepted")
			}
		})
	}
	other, err := f.st.ListChecks(f.ctx, f.theirAgent, "")
	if err != nil || len(other) != 0 {
		t.Fatalf("out-of-scope rows persisted: %+v %v", other, err)
	}
}
