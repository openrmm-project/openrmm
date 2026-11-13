package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

// What the Patches screen reads: a decision shows in the catalog's approval
// whatever the install did since, and the catalog lists approved and declined
// patches on request.
func TestPatchCatalogApprovalThroughTheAPIIntegration(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()
	agent, err := f.store.CreateAgent(ctx, "PATCH-"+uuid.NewString()[:8], "linux", "amd64", "0.10.0", "tok-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The catalog is fleet-wide and this schema is shared, so the patches are
	// named for this test alone.
	approveMe, declineMe := "apitest-approve-"+uuid.NewString()[:8], "apitest-decline-"+uuid.NewString()[:8]
	if err := f.store.UpsertPatchReport(ctx, agent.ID, &protocol.PatchReportMsg{OS: "linux", Patches: []protocol.PatchItem{
		{Identifier: approveMe, Title: approveMe, Classification: protocol.PatchSecurity},
		{Identifier: declineMe, Title: declineMe, Classification: protocol.PatchOptional},
	}}); err != nil {
		t.Fatal(err)
	}

	list := func(filter string) map[string]store.PatchCatalogRow {
		t.Helper()
		target := "/api/patches"
		if filter != "" {
			target += "?approval=" + filter
		}
		rec := f.do(http.MethodGet, target, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", target, rec.Code, rec.Body)
		}
		var body struct {
			Patches []store.PatchCatalogRow `json:"patches"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := map[string]store.PatchCatalogRow{}
		for _, p := range body.Patches {
			out[p.Identifier] = p
		}
		return out
	}
	decide := func(identifier, decision string) int {
		t.Helper()
		rec := f.do(http.MethodPost, "/api/patches/"+list("")[identifier].ID+"/approve", `{"scope_type":"","scope_id":"","decision":"`+decision+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", decision, identifier, rec.Code, rec.Body)
		}
		var body struct {
			Devices int `json:"devices"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Devices
	}

	pending := list("pending")
	if pending[approveMe].Approval != store.PatchApprovalPending || pending[declineMe].Approval != store.PatchApprovalPending {
		t.Fatalf("undecided patches: %+v", pending)
	}
	if n := decide(approveMe, "approve"); n != 1 {
		t.Fatalf("approval reports %d devices, want 1", n)
	}
	if n := decide(declineMe, "decline"); n != 1 {
		t.Fatalf("decline reports %d devices, want 1", n)
	}

	// The install is claimed: the patch is still approved.
	if _, err := f.store.ClaimApprovedPatches(ctx, agent.ID, []string{approveMe}); err != nil {
		t.Fatal(err)
	}
	approved, declined, stillPending := list("approved"), list("declined"), list("pending")
	if row, ok := approved[approveMe]; !ok || row.Approval != store.PatchApprovalApproved || row.InstallingCount != 1 {
		t.Fatalf("approved list: %+v (present %v)", row, ok)
	}
	if _, ok := approved[declineMe]; ok {
		t.Fatal("the declined patch is in the approved list")
	}
	if row, ok := declined[declineMe]; !ok || row.Approval != store.PatchApprovalDeclined || row.DeclinedCount != 1 {
		t.Fatalf("declined list: %+v (present %v)", row, ok)
	}
	if _, ok := declined[approveMe]; ok {
		t.Fatal("the approved patch is in the declined list")
	}
	for _, id := range []string{approveMe, declineMe} {
		if _, ok := stillPending[id]; ok {
			t.Fatalf("%s is still pending after a decision", id)
		}
	}

	if rec := f.do(http.MethodGet, "/api/patches?approval=partial", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown approval filter: %d %s", rec.Code, rec.Body)
	}
}
