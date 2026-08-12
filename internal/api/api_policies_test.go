package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// A policy's name and description were write-once: the console could create one
// and delete it, and nothing in between. Fixing a typo in a policy assigned
// across the fleet meant rebuilding it and losing its checks, tasks and
// assignments, so the round trip is asserted along with what it must not touch.
func TestUpdatePolicyEndpointIntegration(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()

	created := f.do(http.MethodPost, "/api/policies", `{"name":"Servers - baselin","description":"typo above"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", created.Code, created.Body.String())
	}
	var policy models.Policy
	if err := json.Unmarshal(created.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}

	client, err := f.store.CreateClient(ctx, "acme-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	assign := f.do(http.MethodPost, "/api/policies/"+policy.ID+"/assignments",
		`{"scope_type":"client","scope_id":"`+client.ID+`"}`)
	if assign.Code != http.StatusCreated && assign.Code != http.StatusOK {
		t.Fatalf("assignment status = %d, body=%s", assign.Code, assign.Body.String())
	}

	rec := f.do(http.MethodPut, "/api/policies/"+policy.ID, `{"name":"Servers - baseline","description":"every production server"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var updated models.Policy
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Servers - baseline" || updated.Description != "every production server" {
		t.Fatalf("response = %+v, want the new name and description", updated)
	}
	if updated.ID != policy.ID {
		t.Errorf("id changed from %s to %s", policy.ID, updated.ID)
	}

	stored := f.policies()
	if len(stored) != 1 || stored[0].Name != "Servers - baseline" || stored[0].Description != "every production server" {
		t.Fatalf("stored policies = %+v, want the rename persisted", stored)
	}

	// Renaming is a relabel, not a rebuild: what the policy applies to survives.
	kept := f.do(http.MethodGet, "/api/policies/"+policy.ID+"/assignments", "")
	if !strings.Contains(kept.Body.String(), client.ID) {
		t.Errorf("the assignment did not survive the rename: %s", kept.Body.String())
	}

	if entry := f.audit("policy.update"); entry == nil {
		t.Error("no policy.update audit entry")
	} else if entry.Target != policy.ID {
		t.Errorf("audit target = %q, want %q", entry.Target, policy.ID)
	}
}

// The rest of the policy surface over HTTP: listing, unassigning, and the
// delete the drawer's footer button reaches.
func TestPolicyListAssignAndDeleteEndpointsIntegration(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()

	client, err := f.store.CreateClient(ctx, "acme-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	created := f.do(http.MethodPost, "/api/policies", `{"name":"listed","description":"d"}`)
	var policy models.Policy
	if err := json.Unmarshal(created.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}

	list := f.do(http.MethodGet, "/api/policies", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", list.Code, list.Body.String())
	}
	var listed struct {
		Policies []models.Policy `json:"policies"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Policies) != 1 || listed.Policies[0].ID != policy.ID {
		t.Fatalf("listed = %+v, want the one policy", listed.Policies)
	}

	assigned := f.do(http.MethodPost, "/api/policies/"+policy.ID+"/assignments",
		`{"scope_type":"client","scope_id":"`+client.ID+`"}`)
	var assignment models.PolicyAssignment
	if err := json.Unmarshal(assigned.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}

	if rec := f.do(http.MethodDelete, "/api/policies/assignments/"+assignment.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("unassign status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if left, err := f.store.ListPolicyAssignments(ctx, policy.ID); err != nil || len(left) != 0 {
		t.Errorf("assignments after unassigning = %+v, err=%v", left, err)
	}

	if rec := f.do(http.MethodDelete, "/api/policies/"+policy.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if remaining := f.policies(); len(remaining) != 0 {
		t.Errorf("policies after delete = %+v, want none", remaining)
	}
	// Deleting what is already gone reports it rather than succeeding twice.
	if rec := f.do(http.MethodDelete, "/api/policies/"+policy.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestUpdatePolicyRejectsBadInputIntegration(t *testing.T) {
	f := newPolicyAPIFixture(t)

	created := f.do(http.MethodPost, "/api/policies", `{"name":"keep me"}`)
	var policy models.Policy
	if err := json.Unmarshal(created.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name, id, body string
		want           int
	}{
		{"empty name", policy.ID, `{"name":""}`, http.StatusBadRequest},
		{"blank name", policy.ID, `{"name":"   "}`, http.StatusBadRequest},
		{"unknown policy", uuid.NewString(), `{"name":"whatever"}`, http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(http.MethodPut, "/api/policies/"+tt.id, tt.body)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}

	// A rejected update must not have half-applied.
	if stored := f.policies(); len(stored) != 1 || stored[0].Name != "keep me" {
		t.Errorf("policies after rejected updates = %+v", stored)
	}

	// Surrounding whitespace is the operator's slip, not part of the name.
	if rec := f.do(http.MethodPut, "/api/policies/"+policy.ID, `{"name":"  padded  ","description":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("padded name status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if stored := f.policies(); stored[0].Name != "padded" {
		t.Errorf("stored name = %q, want it trimmed", stored[0].Name)
	}
}
