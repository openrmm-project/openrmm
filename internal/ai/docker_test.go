package ai

import (
	"encoding/json"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

func TestDockerCheckProposalRemainsDisabled(t *testing.T) {
	id := "device"
	c, err := (proposedCheckArgs{Type: models.CheckDockerContainer, Name: "Web health", Config: json.RawMessage(`{"container":"web","mode":"healthy"}`)}).check(ToolContext{Actor: "mcp:test"}, &id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled || c.ApprovedBy != "" || c.ProposedBy != "mcp:test" {
		t.Fatalf("Docker proposal bypassed review: %+v", c)
	}
}
