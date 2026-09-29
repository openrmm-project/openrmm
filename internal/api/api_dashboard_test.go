package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// A principal without devices.view or jobs.view gets an empty object, and the
// store is never asked: a nil store would panic.
func TestDashboardWithoutPermissionsIsEmpty(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req = req.WithContext(context.WithValue(req.Context(), permsKey, map[string]bool{models.PermAlertsView: true}))
	rr := httptest.NewRecorder()

	s.getDashboard(rr, req)

	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "{}" {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
}

// Through the router: the counts follow the caller's scope, and the waiting
// jobs come without their script or output.
func TestDashboardFollowsScopeIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	c, err := e.store.CreateClient(e.ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	site, err := e.store.CreateSite(e.ctx, c.ID, "hq")
	if err != nil {
		t.Fatal(err)
	}
	inSite, err := e.store.CreateAgent(e.ctx, "in-site", "linux", "amd64", "1", "tok-in", &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	outside, err := e.store.CreateAgent(e.ctx, "outside", "linux", "amd64", "1", "tok-out", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{inSite.ID, outside.ID} {
		if _, err := e.store.CreateJob(e.ctx, &models.Job{AgentID: a, Type: models.JobRunScript, Shell: "bash",
			Script: "echo the-script-body", Description: "proposed", Status: models.JobAwaitingApproval, CreatedBy: "ai:test"}); err != nil {
			t.Fatal(err)
		}
	}

	_, all := e.do(http.MethodGet, "/api/dashboard", "", e.admin)
	if d := all["devices"].(map[string]any); d["total"] != float64(2) {
		t.Errorf("admin devices: %v", d)
	}
	if j := all["jobs"].(map[string]any); j["awaiting"] != float64(2) || j["failed_window"] != float64(24) {
		t.Errorf("admin jobs: %v", j)
	}

	tech := e.session(models.RoleTech, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: site.ID}})
	_, mine := e.do(http.MethodGet, "/api/dashboard", "", tech)
	if d := mine["devices"].(map[string]any); d["total"] != float64(1) {
		t.Errorf("scoped devices: %v", d)
	}
	jobs := mine["jobs"].(map[string]any)
	items := jobs["awaiting_items"].([]any)
	if jobs["awaiting"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["hostname"] != "in-site" {
		t.Errorf("scoped jobs: %v", jobs)
	}
	body, _ := json.Marshal(mine)
	if strings.Contains(string(body), "the-script-body") || strings.Contains(string(body), `"script"`) || strings.Contains(string(body), `"stdout"`) {
		t.Errorf("the dashboard carried job content: %s", body)
	}
}
