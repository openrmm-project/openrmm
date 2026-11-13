package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// A patch approval is audited against the patch's id with an empty detail, so
// the audit screen's Target column showed a bare UUID. The audit list now names
// the patch, as it does for legacy alert actions, without touching the stored
// entry.
func TestPatchDecisionsAreNamedInTheAuditLogIntegration(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()
	agent, err := f.store.CreateAgent(ctx, "AUDIT-"+uuid.NewString()[:8], "windows", "amd64", "0.10.0", "tok-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	titled, untitled := "KB"+uuid.NewString()[:7], "pkg-"+uuid.NewString()[:8]
	title := "Security update " + titled
	if err := f.store.UpsertPatchReport(ctx, agent.ID, &protocol.PatchReportMsg{OS: "windows", Patches: []protocol.PatchItem{
		{Identifier: titled, Title: title, Classification: protocol.PatchSecurity},
		{Identifier: untitled, Classification: protocol.PatchOptional},
	}}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	rows, err := f.store.ListAgentPatches(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		ids[r.Identifier] = r.PatchID
	}
	for identifier, decision := range map[string]string{titled: "approve", untitled: "decline"} {
		rec := f.do(http.MethodPost, "/api/patches/"+ids[identifier]+"/approve", `{"decision":"`+decision+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", decision, identifier, rec.Code, rec.Body)
		}
	}
	// A patch the catalog no longer has keeps its bare id rather than a guess.
	gone := uuid.NewString()
	if err := f.store.AddAudit(ctx, f.admin.ID, "patch.approve", gone, nil); err != nil {
		t.Fatal(err)
	}

	rec := f.do(http.MethodGet, "/api/audit?action=patch.&limit=200", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Entries []struct {
			models.AuditEntry
			TargetLabel string `json:"target_label"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ids[titled]: title, ids[untitled]: untitled, gone: ""}
	seen := map[string]bool{}
	for _, e := range body.Entries {
		label, ok := want[e.Target]
		if !ok {
			continue
		}
		seen[e.Target] = true
		if e.TargetLabel != label {
			t.Errorf("%s on %s is labelled %q, want %q", e.Action, e.Target, e.TargetLabel, label)
		}
		if string(e.Detail) != "{}" {
			t.Errorf("the stored detail of %s changed: %s", e.Action, e.Detail)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("found %d of the %d patch audit entries", len(seen), len(want))
	}
}
